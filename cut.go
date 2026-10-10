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
// expressed because the packet is not at the corresponding end of an
// output link.
type Span struct {
	Out       time.Duration
	In        time.Duration
	Length    time.Duration
	Synthetic bool
}

// Stream is the result of a Cut: packets on a continuous timeline starting at
// zero, with the headers they are to be written under and the map back to
// the source.
//
// A Stream has a link for each stretch of packets that came from one link of
// a chained Ogg source, in order, and one link for any other source. Each
// link keeps its source link's output gain; its pre-skip is carried by its
// leading packets and its end trimming by its last one, and Cut gives no
// other packet a Skip or Trim. WriteOgg writes the links as a chained Ogg
// stream, so a decoder resets between them as it did in the source. WebM has
// no links: WriteWebM writes a Stream whose links share their output gain as
// one track, a later link's pre-skip and an earlier link's end trimming as
// DiscardPadding, and the decoder does not reset between them.
type Stream struct {
	// Header is the stream's, with the pre-skip and output gain of its
	// first link.
	Header Header
	// Links holds each link's header, its PreSkip the Skip of its leading
	// packets, for Packet.Link to index; Cut fills it. A Stream with no
	// Links has one link, with Header.
	Links   []Header
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
	for i, p := range src.Packets {
		if p.Link < 0 || p.Link >= max(len(src.Links), 1) {
			return nil, fmt.Errorf("puropus: packet %d belongs to link %d of %d", i, p.Link, max(len(src.Links), 1))
		}
	}
	for i, h := range src.Links {
		if h.Channels != src.Header.Channels {
			return nil, fmt.Errorf("puropus: link %d has %d channels, the stream %d", i, h.Channels, src.Header.Channels)
		}
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
			// Leading silence belongs to the first region's link; silence
			// between regions to the link before it, so that the next
			// region, if it starts a link, starts with its own packets.
			b.startLink(packets[0].Link)
		}
		if gap > 0 {
			if err := b.silence(gap, b.bandwidthNear(reg.start)); err != nil {
				return nil, err
			}
		}
		for i, p := range packets {
			if i > 0 && p.Start > packets[i-1].End() {
				// Audio the container lost, which the decoder conceals:
				// silence of the same length keeps the timeline.
				if err := b.fillHole(p.Start-packets[i-1].End(), packets[i-1].End()); err != nil {
					return nil, err
				}
			}
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
	out.Header.PreSkip = 0
	if len(b.links) > 0 {
		out.Header = b.links[0]
		out.Links = b.links
	}
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
	hint     int // source packets before this index are settled
	// links holds the output links' headers, the last one's PreSkip still
	// growing; srcLink is the source link of the last, and audible reports
	// whether that link has a packet that contributes samples yet.
	links   []Header
	srcLink int
	audible bool
}

// startLink starts an output link for the source link srcLink.
func (b *builder) startLink(srcLink int) {
	h := b.src.Header
	if srcLink < len(b.src.Links) {
		h = b.src.Links[srcLink]
	}
	h.PreSkip = 0
	b.links = append(b.links, h)
	b.srcLink = srcLink
	b.audible = false
}

// push appends p at the cursor, in a new output link when it comes from
// another source link than the packet before it. Its pre-skip survives only
// at the head of its output link and its end trimming only until another
// packet of the link follows; anywhere else the container has no way to
// drop those samples, so they are emitted as audio and mapped as synthetic.
func (b *builder) push(p Packet) {
	if len(b.links) == 0 || p.Link != b.srcLink {
		b.startLink(p.Link) // the link before keeps its end trimming
	} else {
		b.settleTrim()
	}
	link := len(b.links) - 1
	out := Packet{Data: p.Data, Start: b.cursor, Duration: p.Duration, Skip: p.Skip, Trim: p.Trim, Link: link}
	if p.Skip > 0 && b.audible {
		b.synthetic(int64(p.Skip))
		out.Duration += p.Skip
		out.Skip = 0
	}
	b.links[link].PreSkip += out.Skip
	b.audible = b.audible || out.Duration > 0
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

// packetAt returns the source packet covering sample pos, or the last one.
func (b *builder) packetAt(pos int64) Packet {
	i, _ := slices.BinarySearchFunc(b.src.Packets, pos, func(p Packet, pos int64) int {
		if p.End() <= pos {
			return -1
		}
		if p.Start > pos {
			return 1
		}
		return 0
	})
	return b.src.Packets[min(i, len(b.src.Packets)-1)]
}

// bandwidthNear returns the Opus bandwidth of the packet covering sample pos,
// so synthesized silence matches its neighbours.
func (b *builder) bandwidthNear(pos int64) opus.Bandwidth {
	if parsed, err := opus.Parse(b.packetAt(pos).Data, false); err == nil {
		return parsed.Bandwidth
	}
	return opus.BandwidthFullband
}

// linkAt returns the source link of the audio at sample pos, for the
// packets that re-encode it.
func (b *builder) linkAt(pos int64) int { return b.packetAt(pos).Link }

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
		b.packets = append(b.packets, Packet{Data: data, Start: start, Duration: parsed.Duration48(), Link: len(b.links) - 1})
	}
	b.audible = true
	return nil
}

// fillHole inserts silence for a stretch of n samples at source position at
// that no packet covers, rounded up to the 2.5 ms grid of Opus frames: the
// gaps the Decoder and ReadPackets find are on it already.
func (b *builder) fillHole(n, at int64) error {
	const grid = 120
	return b.silence(samplesToDuration((n+grid-1)/grid*grid), b.bandwidthNear(at-1))
}

// maxWarmUp is the most decoder warm-up Cut keeps before a packet: 80 ms,
// the pre-roll RFC 7845 §4.6 recommends to start decoding before a seek
// target. Whole packets the container discards, such as WebM blocks a
// negative DiscardPadding removes, beyond that are dropped, which also keeps
// the pre-skip they would add within what an OpusHead holds.
const maxWarmUp = 80 * SampleRate / 1000

// selectPackets returns the source packets a region keeps: those overlapping
// it, or lying inside it when inward. A kept packet brings the packets of its
// link before it that lie entirely inside the pre-skip, up to maxWarmUp of
// them, so the decoder warm-up they carry is kept with it. Regions are
// disjoint and sorted, so no packet is selected twice.
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
	for warmUp := 0; warmUp < maxWarmUp && first > b.hint && first < after &&
		packets[first-1].Duration == 0 && packets[first-1].Link == packets[first].Link; {
		first--
		warmUp += packets[first].Coded()
	}
	b.hint = max(b.hint, after)
	return packets[first:after]
}

// exactRegion copies the packets that lie inside the region by at least the
// guard on each side and re-encodes the audio between them and the region
// boundaries; a region with no such packet is re-encoded whole. Each
// re-encoded stretch is rounded to the nearest 2.5 ms, so the output's edges
// land within 1.25 ms of the region's.
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
		return b.encode(reg.start, reg.start+gridLength(reg.end-reg.start))
	}
	copyStart, copyEnd := packets[first].Start, packets[after-1].End()
	lead, err := b.encode(copyStart-gridLength(copyStart-reg.start), copyStart)
	if err != nil {
		return nil, err
	}
	tail, err := b.encode(copyEnd, copyEnd+gridLength(reg.end-copyEnd))
	if err != nil {
		return nil, err
	}
	return slices.Concat(lead, packets[first:after], tail), nil
}

// defaultGuard is the Guard of an Edit that leaves it zero.
const defaultGuard = 80 * time.Millisecond

// gridLength rounds a guard length to the nearest 2.5 ms, the unit of
// Opus frames.
func gridLength(want int64) int64 {
	const grid = 120 // 2.5 ms at 48 kHz
	return (want + grid/2) / grid * grid
}

// encode re-encodes the source audio in [start, end) and returns the packets
// positioned there. Rounding to the frame grid can take the stretch up to
// 1.25 ms past an end of the source: the encoder gets silence for that part,
// which its packets then discard, as the pre-skip of the first or the end
// trimming of the last.
func (b *builder) encode(start, end int64) ([]Packet, error) {
	if end <= start {
		return nil, nil
	}
	ch := int64(b.channels)
	from, to := max(start, b.window.start), min(end, b.window.end)
	pcm := b.src.Native[(from-b.window.start)*ch : (to-b.window.start)*ch]
	if from != start || to != end {
		padded := make([]float32, (end-start)*ch)
		copy(padded[(from-start)*ch:], pcm)
		pcm = padded
	}
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
		coded := int64(parsed.Duration48())
		skip := min(max(from-pos, 0), coded)
		trim := min(max(pos+coded-to, 0), coded-skip)
		packets = append(packets, Packet{
			Data: data, Start: pos + skip, Duration: int(coded - skip - trim), Skip: int(skip), Trim: int(trim),
			Link: b.linkAt(max(pos, from)),
		})
		pos += coded
	}
	if pos != end {
		return nil, fmt.Errorf("puropus: encoder returned %d samples for a %d-sample region", pos-start, end-start)
	}
	return packets, nil
}
