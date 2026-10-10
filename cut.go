package puropus

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/fgn/puropus/opus"
)

// Snap chooses how interval boundaries that fall inside a packet are handled.
type Snap int

const (
	// SnapOutward keeps every packet that overlaps an interval, so nothing
	// inside the interval is lost and up to one packet of context is kept on
	// each side. It needs no decoding.
	SnapOutward Snap = iota
	// SnapInward keeps only packets that lie entirely inside an interval.
	// Intervals shorter than a packet may select nothing; such regions do not
	// contribute packets or requested silence to the output.
	SnapInward
	// SnapExact keeps whole interior packets and re-encodes the audio between
	// the interval boundary and the first or last kept packet with the
	// Edit's Encoder, so the output starts and ends within 1.25 ms of the
	// interval (Opus frames come in 2.5 ms units). It needs Source.Native.
	SnapExact
)

// Margin extends every kept interval with real audio on each side before
// packets are selected. Intervals that touch or overlap after extension are
// merged. Both durations must not be negative.
type Margin struct {
	Before time.Duration
	After  time.Duration
}

// Silence inserts synthesized digital silence around and between kept
// regions. Durations must be non-negative multiples of 2.5 ms.
type Silence struct {
	Leading  time.Duration
	Between  time.Duration
	Trailing time.Duration
}

// Encoder produces Opus packets for 48 kHz interleaved PCM. It is only used
// for SnapExact, whose guard regions are re-encoded from the decoded audio.
//
// The packets returned for pcm must decode, from a freshly reset decoder, to
// exactly the samples of pcm: the same count (len(pcm)/channels, always a
// multiple of 120) and time-aligned with no leading delay. An encoder with
// algorithmic delay, such as libopus (OPUS_GET_LOOKAHEAD, 312 samples by
// default), compensates by priming: it feeds enough leading zeros for the
// delay to end on a packet boundary, feeds pcm, flushes with as many zeros
// as the delay, and drops the leading packets that cover the priming.
type Encoder interface {
	Encode(pcm []float32, channels int) ([][]byte, error)
}

// Edit describes a cut.
type Edit struct {
	// Keep lists the intervals to retain on the source's timeline. Each must
	// be non-empty (End after Start); they may come in any order, and those
	// that overlap or touch are merged.
	Keep []Interval
	// Extend widens each interval with context before selection.
	Extend Margin
	// Snap chooses how boundaries inside a packet are handled; the zero
	// value is SnapOutward.
	Snap Snap
	// Guard is the least amount of audio SnapExact re-encodes at each
	// boundary before copying interior packets; zero selects 80 ms, and it
	// must not be negative.
	Guard time.Duration
	// Silence inserts synthesized silence before, between, and after regions.
	Silence Silence
	// Encoder is required for SnapExact.
	Encoder Encoder
}

// Span maps a stretch of the output timeline back to the input. Synthetic
// spans have no input position: inserted silence, and the encoder warm-up
// or padding of a kept packet whose pre-skip or end trimming could not be
// expressed because the packet is not at the corresponding end of the
// output.
type Span struct {
	Out       time.Duration
	In        time.Duration
	Length    time.Duration
	Synthetic bool
}

// Stream is the result of a Cut: packets on a continuous timeline starting at
// zero, with the header they are to be written under and the map back to the
// source. The pre-skip of the output is carried by its leading packets and
// the end trimming by its last one; no other packet has Skip or Trim.
type Stream struct {
	Header  Header
	Packets []Packet
	Spans   []Span
}

// Duration returns the total length of the stream.
func (s *Stream) Duration() time.Duration {
	if len(s.Packets) == 0 {
		return 0
	}
	return samplesToDuration(s.Packets[len(s.Packets)-1].End())
}

// InputTime maps an output time to the source time it came from. It returns
// false for times inside synthetic spans or past the end.
func (s *Stream) InputTime(out time.Duration) (time.Duration, bool) {
	for _, span := range s.Spans {
		if out >= span.Out && out < span.Out+span.Length {
			if span.Synthetic {
				return 0, false
			}
			return span.In + (out - span.Out), true
		}
	}
	return 0, false
}

// edges returns the pre-skip and end trimming the stream is written with.
// Skipped or trimmed samples elsewhere cannot be expressed in either
// container; Cut never produces them.
func (s *Stream) edges() (preSkip, trim int, err error) {
	for i, p := range s.Packets {
		if i > 0 && p.Skip > 0 && s.Packets[i-1].Duration > 0 {
			return 0, 0, fmt.Errorf("puropus: packet %d skips %d samples inside the stream", i, p.Skip)
		}
		if p.Trim > 0 && i < len(s.Packets)-1 {
			return 0, 0, fmt.Errorf("puropus: packet %d trims %d samples inside the stream", i, p.Trim)
		}
		preSkip += p.Skip
		trim = p.Trim
	}
	return preSkip, trim, nil
}

// region is a half-open sample range on the source timeline.
type region struct {
	start, end int64
}

// Cut applies edit to src and returns the resulting stream. Kept audio is
// copied packet by packet without decoding; only SnapExact re-encodes, and
// only the guard regions.
func Cut(src Source, edit Edit) (*Stream, error) {
	if err := validateEdit(edit); err != nil {
		return nil, err
	}
	if len(src.Packets) == 0 {
		return nil, fmt.Errorf("puropus: source has no packets")
	}
	window := region{src.Packets[0].Start, src.Packets[len(src.Packets)-1].End()}
	if edit.Snap == SnapExact {
		if edit.Encoder == nil {
			return nil, ErrEncoderRequired
		}
		if src.Native == nil || int64(len(src.Native)) != (window.end-window.start)*int64(src.Header.Channels) {
			return nil, ErrNativeRequired
		}
	}
	regions := resolveRegions(edit, window)

	b := &builder{src: src, edit: edit, window: window, channels: src.Header.Channels}
	kept := 0
	var last region
	for _, reg := range regions {
		var packets []Packet
		if edit.Snap == SnapExact {
			var err error
			packets, err = b.exactRegion(reg)
			if err != nil {
				return nil, err
			}
		} else {
			packets = b.selectPackets(reg, edit.Snap == SnapInward)
		}
		if len(packets) == 0 {
			continue
		}
		gap := edit.Silence.Between
		if kept == 0 {
			gap = edit.Silence.Leading
		}
		if gap > 0 {
			if err := b.silence(gap, b.bandwidthNear(reg.start)); err != nil {
				return nil, err
			}
		}
		for _, p := range packets {
			b.push(p)
		}
		kept++
		last = reg
	}
	if kept > 0 && edit.Silence.Trailing > 0 {
		if err := b.silence(edit.Silence.Trailing, b.bandwidthNear(last.end-1)); err != nil {
			return nil, err
		}
	}
	out := &Stream{Header: src.Header, Packets: b.packets, Spans: b.spans}
	out.Header.PreSkip = b.preSkip
	return out, nil
}

// validateEdit rejects the settings of an Edit that have no meaning, rather
// than giving them an accidental one: a negative Guard would copy packets
// far outside an interval, a negative margin would shrink it.
func validateEdit(edit Edit) error {
	if edit.Snap < SnapOutward || edit.Snap > SnapExact {
		return fmt.Errorf("puropus: unknown snap mode %d", edit.Snap)
	}
	if edit.Guard < 0 {
		return fmt.Errorf("puropus: negative guard %v", edit.Guard)
	}
	if edit.Extend.Before < 0 || edit.Extend.After < 0 {
		return fmt.Errorf("puropus: negative margin %+v", edit.Extend)
	}
	for _, d := range []time.Duration{edit.Silence.Leading, edit.Silence.Between, edit.Silence.Trailing} {
		if d < 0 || d%(2500*time.Microsecond) != 0 {
			return fmt.Errorf("puropus: silence %v is not a non-negative multiple of 2.5 ms", d)
		}
	}
	for i, iv := range edit.Keep {
		if iv.End <= iv.Start {
			return fmt.Errorf("puropus: interval %d [%v, %v) is empty", i, iv.Start, iv.End)
		}
	}
	return nil
}

// resolveRegions converts, extends, clamps, sorts, and merges the intervals.
func resolveRegions(edit Edit, window region) []region {
	regions := make([]region, 0, len(edit.Keep))
	for _, iv := range edit.Keep {
		start := max(durationToSamples(addSaturating(iv.Start, -edit.Extend.Before)), window.start)
		end := min(durationToSamples(addSaturating(iv.End, edit.Extend.After)), window.end)
		if end <= start {
			continue
		}
		regions = append(regions, region{start, end})
	}
	slices.SortFunc(regions, func(a, b region) int { return cmp.Compare(a.start, b.start) })
	merged := regions[:0]
	for _, r := range regions {
		if n := len(merged); n > 0 && r.start <= merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, r.end)
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// addSaturating returns a+b, clamped to the range of time.Duration.
func addSaturating(a, b time.Duration) time.Duration {
	s := a + b
	if (s > a) != (b > 0) {
		if b > 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return s
}

// builder lays packets on the output timeline. Packets arrive positioned on
// the source timeline and leave positioned on the output.
type builder struct {
	src      Source
	edit     Edit
	window   region
	channels int
	packets  []Packet
	spans    []Span
	cursor   int64
	preSkip  int // Skip carried by the leading packets
	hint     int // source packets before this index are settled
}

// push appends p at the cursor. Its pre-skip survives only at the head of the
// output and its end trimming only until another packet follows; anywhere
// else the container has no way to drop those samples, so they are emitted
// as audio and mapped as synthetic.
func (b *builder) push(p Packet) {
	b.settleTrim()
	out := Packet{Data: p.Data, Start: b.cursor, Duration: p.Duration, Skip: p.Skip, Trim: p.Trim}
	if p.Skip > 0 && b.cursor > 0 {
		b.synthetic(int64(p.Skip))
		out.Duration += p.Skip
		out.Skip = 0
	}
	b.preSkip += out.Skip
	if p.Duration > 0 {
		b.appendSpan(Span{Out: samplesToDuration(b.cursor), In: samplesToDuration(p.Start), Length: samplesToDuration(int64(p.Duration))})
		b.cursor += int64(p.Duration)
	}
	b.packets = append(b.packets, out)
}

// settleTrim turns the end trimming of the last packet into audio, because
// another packet is about to follow it.
func (b *builder) settleTrim() {
	n := len(b.packets)
	if n == 0 || b.packets[n-1].Trim == 0 {
		return
	}
	last := &b.packets[n-1]
	b.synthetic(int64(last.Trim))
	last.Duration += last.Trim
	last.Trim = 0
}

// synthetic advances the output by n samples that come from nowhere.
func (b *builder) synthetic(n int64) {
	b.appendSpan(Span{Out: samplesToDuration(b.cursor), Length: samplesToDuration(n), Synthetic: true})
	b.cursor += n
}

// appendSpan adds s to the span map, extending the last span instead when s
// continues it on both timelines.
func (b *builder) appendSpan(s Span) {
	if n := len(b.spans); n > 0 {
		prev := &b.spans[n-1]
		contiguous := prev.Synthetic == s.Synthetic && prev.Out+prev.Length == s.Out &&
			(s.Synthetic || prev.In+prev.Length == s.In)
		if contiguous {
			prev.Length += s.Length
			return
		}
	}
	b.spans = append(b.spans, s)
}

// bandwidthNear returns the Opus bandwidth of the packet covering sample pos,
// so synthesized silence matches its neighbours.
func (b *builder) bandwidthNear(pos int64) opus.Bandwidth {
	i, _ := slices.BinarySearchFunc(b.src.Packets, pos, func(p Packet, pos int64) int {
		if p.End() <= pos {
			return -1
		}
		if p.Start > pos {
			return 1
		}
		return 0
	})
	if i >= len(b.src.Packets) {
		i = len(b.src.Packets) - 1
	}
	if parsed, err := opus.Parse(b.src.Packets[i].Data, false); err == nil {
		return parsed.Bandwidth
	}
	return opus.BandwidthFullband
}

// silence appends d of synthesized silence at the cursor, coded at the
// given bandwidth.
func (b *builder) silence(d time.Duration, bandwidth opus.Bandwidth) error {
	packets, err := opus.Silence(b.channels, bandwidth, d)
	if err != nil {
		return err
	}
	b.settleTrim()
	for _, data := range packets {
		parsed, err := opus.Parse(data, false)
		if err != nil {
			return err
		}
		start := b.cursor
		b.synthetic(int64(parsed.Duration48()))
		b.packets = append(b.packets, Packet{Data: data, Start: start, Duration: parsed.Duration48()})
	}
	return nil
}

// selectPackets returns the source packets a region keeps: those overlapping
// it, or lying inside it when inward. A kept packet at the start of the
// source brings the packets before it that lie entirely inside the pre-skip,
// so the decoder warm-up they carry is kept with it. Regions are disjoint
// and sorted, so no packet is selected twice.
func (b *builder) selectPackets(reg region, inward bool) []Packet {
	packets := b.src.Packets
	i := b.hint
	for i < len(packets) && (packets[i].End() <= reg.start || inward && packets[i].Start < reg.start) {
		i++
	}
	first := i
	for i < len(packets) && packets[i].Start < reg.end && (!inward || packets[i].End() <= reg.end) {
		i++
	}
	after := i
	for first > b.hint && first < after && packets[first-1].Duration == 0 {
		first--
	}
	b.hint = max(b.hint, after)
	return packets[first:after]
}

// exactRegion copies the packets that lie inside the region by at least the
// guard on each side and re-encodes the audio between them and the region
// boundaries; a region with no such packet is re-encoded whole.
func (b *builder) exactRegion(reg region) ([]Packet, error) {
	guard := durationToSamples(cmp.Or(b.edit.Guard, defaultGuard))
	packets := b.src.Packets
	i := b.hint
	for i < len(packets) && packets[i].Start < reg.start+guard {
		i++
	}
	first := i
	for i < len(packets) && packets[i].End() <= reg.end-guard {
		i++
	}
	after := i
	b.hint = max(b.hint, after)
	if first == after {
		return b.encode(reg.start, reg.start+gridLength(reg.end-reg.start, b.window.end-reg.start))
	}
	copyStart, copyEnd := packets[first].Start, packets[after-1].End()
	lead, err := b.encode(copyStart-gridLength(copyStart-reg.start, copyStart-b.window.start), copyStart)
	if err != nil {
		return nil, err
	}
	tail, err := b.encode(copyEnd, copyEnd+gridLength(reg.end-copyEnd, b.window.end-copyEnd))
	if err != nil {
		return nil, err
	}
	return slices.Concat(lead, packets[first:after], tail), nil
}

// defaultGuard is the Guard of an Edit that leaves it zero.
const defaultGuard = 80 * time.Millisecond

// gridLength rounds a guard length to the nearest 2.5 ms, never exceeding
// the audio available.
func gridLength(want, available int64) int64 {
	const grid = 120 // 2.5 ms at 48 kHz
	return min((want+grid/2)/grid, available/grid) * grid
}

// encode re-encodes the source audio in [start, end) and returns the packets
// positioned there.
func (b *builder) encode(start, end int64) ([]Packet, error) {
	if end <= start {
		return nil, nil
	}
	ch := int64(b.channels)
	pcm := b.src.Native[(start-b.window.start)*ch : (end-b.window.start)*ch]
	encoded, err := b.edit.Encoder.Encode(pcm, b.channels)
	if err != nil {
		return nil, fmt.Errorf("puropus: encoder: %w", err)
	}
	packets := make([]Packet, 0, len(encoded))
	pos := start
	for _, data := range encoded {
		parsed, err := opus.Parse(data, false)
		if err != nil {
			return nil, fmt.Errorf("puropus: encoder produced an invalid packet: %w", err)
		}
		packets = append(packets, Packet{Data: data, Start: pos, Duration: parsed.Duration48()})
		pos += int64(parsed.Duration48())
	}
	if pos != end {
		return nil, fmt.Errorf("puropus: encoder returned %d samples for a %d-sample region", pos-start, end-start)
	}
	return packets, nil
}
