package puropus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/fgn/puropus/internal/webm"
	"github.com/fgn/puropus/opus"
)

// gapStream is a stream to lose packets from, with a run of packets of the
// same duration whose first packet after it is FEC-capable when fec is set.
type gapStream struct {
	name   string
	stream *Stream
	fec    bool
}

// gapStreams returns mono CELT without in-band FEC, mono SILK with it, and
// stereo SILK with it.
func gapStreams(t *testing.T) []gapStream {
	t.Helper()
	streamOf := func(name string) *Stream {
		src := sourceFor(t, name)
		return &Stream{Header: src.Header, Packets: slices.Clone(src.Packets)}
	}
	var stereo *Stream
	for _, c := range tolerantFECCases(t) {
		if c.Source.Vector == "stereo-vectors/channels-silk-fec" && c.Channels == 2 {
			// Four times over, for Ogg pages enough to lose one inside.
			once := decodeLogStream(t, c).Packets
			packets := make([][]byte, 0, 4*len(once))
			for range 4 {
				for _, p := range once {
					packets = append(packets, p.Data)
				}
			}
			stereo = packetStream(t, 2, packets)
		}
	}
	if stereo == nil {
		t.Fatal("no stereo SILK FEC sequence")
	}
	return []gapStream{
		{"mono CELT", streamOf("ogg-celt-fb.opus"), false},
		{"mono SILK with FEC", streamOf("ogg-silk-wb-fec.opus"), true},
		{"stereo SILK with FEC", stereo, true},
	}
}

// oggPagePackets returns, for each audio page of an Ogg Opus file the
// Stream writer made, the index of its first packet and how many it has.
func oggPagePackets(t *testing.T, data []byte) (first, count []int) {
	t.Helper()
	n := 0
	for _, p := range oggPages(t, data)[2:] {
		c := 0
		for _, l := range p.lacing {
			if l < 255 {
				c++
			}
		}
		first, count = append(first, n), append(count, c)
		n += c
	}
	return first, count
}

// webmWithout writes s as WebM without the packets lost, the blocks after
// them keeping their timestamps, as a recorder that dropped them would.
func webmWithout(t *testing.T, s *Stream, lost map[int]bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := webm.NewWriter(&buf, webm.WriterOptions{
		Channels:     s.Header.Channels,
		CodecPrivate: opus.Head{Version: 1, Channels: s.Header.Channels, PreSkip: s.Packets[0].Skip, InputSampleRate: SampleRate}.Marshal(),
		CodecDelay:   samplesToDuration(int64(s.Packets[0].Skip)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(s.Packets); i++ {
		p := s.Packets[i]
		advance := p.Coded()
		for i+1 < len(s.Packets) && lost[i+1] {
			i++
			advance += s.Packets[i].Coded()
		}
		discard := 0
		if i == len(s.Packets)-1 {
			discard = p.Trim
		}
		if err := w.WriteBlock(p.Data, advance, discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Audio a container lost, with an Ogg page dropped as corrupt or WebM blocks
// a recorder left out, is concealed, so the output keeps its length and
// every later sample its position: the decoder finds the gap from the next
// Ogg page's granule position (RFC 7845 §4.1 makes it count exactly the
// samples on the page) or the next WebM block's timestamp. The gap comes out
// as the lost packets would in Tolerant mode: concealed, and its last part
// decoded from the in-band FEC of the packet after it when that packet has
// some. That holds mono and stereo, at 48 and 16 kHz, from a byte at a time.
// The retained packets keep their positions around the hole, the native PCM
// covers the concealment, and a cut across the gap fills it with silence
// mapped as synthetic. ffmpeg drops the lost audio from its output instead.
func TestContainerGapsAreConcealed(t *testing.T) {
	for _, gs := range gapStreams(t) {
		var ogg bytes.Buffer
		if err := gs.stream.WriteOgg(&ogg); err != nil {
			t.Fatal(err)
		}
		first, count := oggPagePackets(t, ogg.Bytes())
		// Lose an audio page other than the first, whose granule position
		// would be an offset, and the last two, where the end trimming
		// mixes in: one followed by a packet of the run's duration, with
		// FEC data for the FEC streams.
		page := -1
		for q := 1; q+2 < len(first) && page < 0; q++ {
			next := gs.stream.Packets[first[q+1]]
			uniform := true
			for _, p := range gs.stream.Packets[first[q]:first[q+1]] {
				uniform = uniform && p.Coded() == next.Coded() && p.Skip == 0 && p.Trim == 0
			}
			if uniform && opus.HasLBRR(next.Data) == gs.fec {
				page = q
			}
		}
		if page < 0 {
			t.Fatalf("%s: no page to lose", gs.name)
		}
		lost := map[int]bool{}
		withLost := &Stream{Header: gs.stream.Header, Packets: slices.Clone(gs.stream.Packets)}
		var kept []Packet
		for i, p := range gs.stream.Packets {
			if i >= first[page] && i < first[page]+count[page] {
				lost[i] = true
				withLost.Packets[i].Data = invalidHybridPacket(t, p.Coded())
				continue
			}
			kept = append(kept, p)
		}
		want := tolerantReplay(t, withLost)
		want16 := resampleTo16k(want, gs.stream.Header.Channels)
		gapStart := gs.stream.Packets[first[page]].Start
		gapEnd := gs.stream.Packets[first[page]+count[page]].Start

		oggData := ogg.Bytes()
		pages := oggPages(t, oggData)
		target := pages[2+page]
		corrupt := bytes.Clone(oggData)
		corrupt[target.start+target.size-1] ^= 0x01
		for format, data := range map[string][]byte{"Ogg": corrupt, "WebM": webmWithout(t, gs.stream, lost)} {
			name := gs.name + ", " + format
			d, err := NewDecoder(&chunkReader{data: data, size: 1}, DecodeOptions{KeepNative: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := readAll(d, make([]float32, 97*gs.stream.Header.Channels))
			if !errors.Is(err, io.EOF) {
				t.Fatalf("%s: %v", name, err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s: %d samples, want the %d of the lost packets concealed", name, len(got), len(want))
			}
			if d.Position() != gs.stream.Duration() {
				t.Fatalf("%s: position %v, want %v", name, d.Position(), gs.stream.Duration())
			}
			src := d.Source()
			assertSamePackets(t, name, src.Packets, kept)
			if !slices.Equal(src.Native, want) {
				t.Fatalf("%s: the native PCM does not cover the concealment", name)
			}
			read, err := ReadPackets(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			assertSamePackets(t, name+", ReadPackets", read.Packets, kept)
			got16, _ := decodeAll(t, &chunkReader{data: data, size: 5}, DecodeOptions{SampleRate: 16000})
			if !slices.Equal(got16, want16) {
				t.Fatalf("%s: the 16 kHz decode is not the 48 kHz one resampled", name)
			}

			out, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
			if err != nil {
				t.Fatal(err)
			}
			assertSpans(t, out, []Span{
				{Out: 0, In: 0, Length: samplesToDuration(gapStart)},
				{Out: samplesToDuration(gapStart), Length: samplesToDuration(gapEnd - gapStart), Synthetic: true},
				{Out: samplesToDuration(gapEnd), In: samplesToDuration(gapEnd), Length: src.Duration() - samplesToDuration(gapEnd)},
			})
			if _, ok := out.InputTime(samplesToDuration((gapStart + gapEnd) / 2)); ok {
				t.Fatalf("%s: the gap maps to the input", name)
			}
			if pcm := decodeStream(t, out, FormatOgg); int64(len(pcm)/gs.stream.Header.Channels) != durationToSamples(src.Duration()) {
				t.Fatalf("%s: the cut decodes to %d samples for %v", name, len(pcm)/gs.stream.Header.Channels, src.Duration())
			}
		}
	}
}

// A gap is found only where the container shows one: WebM timestamps
// rounded to the millisecond, on 2.5 ms packets, or starting late, are not
// gaps. A granule position that jumps without a lost page, against RFC 7845
// §4.1, by half a packet or more, is filled up to the jump, so positions
// follow the granule positions; a smaller jump, a muxer's granule positions
// slightly off, is not; and no gap is filled beyond 10 minutes, against a
// corrupt or hostile granule position.
func TestGapsAreOnlyWhereTheContainerShowsThem(t *testing.T) {
	short, err := opus.Silence(1, opus.BandwidthFullband, 2500*time.Microsecond)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := webm.NewWriter(&buf, webm.WriterOptions{Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	for range 400 {
		if err := w.WriteBlock(short[0], 120, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if src, err := ReadPackets(bytes.NewReader(buf.Bytes())); err != nil || src.Duration() != time.Second {
		t.Fatalf("400 packets of 2.5 ms with rounded timestamps: %v, %v", src.Duration(), err)
	}

	clean := readFixture(t, "ogg-celt-fb.opus")
	for _, tc := range []struct {
		jump, want time.Duration
	}{
		{2500 * time.Microsecond, 3000 * ms}, // an eighth of the 20 ms packets
		{20 * ms, 3020 * ms},
		{11 * time.Minute, 3*time.Second + 10*time.Minute},
	} {
		data := bytes.Clone(clean)
		for _, p := range oggPages(t, data)[3:] { // every page after the first audio page
			g := int64(binary.LittleEndian.Uint64(data[p.start+6:]))
			binary.LittleEndian.PutUint64(data[p.start+6:], uint64(g+durationToSamples(tc.jump)))
			fixOggCRC(data, p)
		}
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil || src.Duration() != tc.want {
			t.Fatalf("granule positions %v later: %v, %v, want %v", tc.jump, src.Duration(), err, tc.want)
		}
		if tc.jump < time.Second {
			if pcm, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{}); int64(len(pcm)) != durationToSamples(tc.want) {
				t.Fatalf("granule positions %v later: %d samples decoded", tc.jump, len(pcm))
			}
		}
	}
}

// The gap rules: a WebM block is late only by half a packet or more and by
// more than a timestamp tick; then the gap is the lateness on the 2.5 ms
// grid, or whole packets of the next packet's duration when the lateness is
// within 8 ms of them. An Ogg page's missing audio counts exactly, once it
// reaches half a packet.
func TestGapRules(t *testing.T) {
	for _, tc := range []struct {
		late, next, tick int64
		want             int
	}{
		{0, 2880, 48, 0},
		{96, 2880, 48, 0},      // Chrome's 2 ms of jitter
		{288, 2880, 48, 0},     // and its 6 ms
		{-288, 2880, 48, 0},    // early
		{1439, 2880, 48, 0},    // just under half a packet
		{1440, 2880, 48, 1440}, // half: a gap, of its length
		{1920, 2880, 48, 1920}, // a 40 ms block lost before a 60 ms one
		{1930, 2880, 48, 1920}, // on the grid
		{2880 - 384, 2880, 48, 2880},
		{2880 + 384, 2880, 48, 2880}, // within 8 ms of a packet: one packet
		{2880 + 385, 2880, 48, 3240},
		{2*2880 + 96, 2880, 48, 2 * 2880},
		{479, 960, 48, 0},
		{480, 960, 48, 480},
		{960 + 300, 960, 48, 960},
		{480, 960, 480, 0}, // a tick of 10 ms is rounding, not loss
		{481, 960, 480, 960},
		{1000, 0, 48, 0}, // a packet that does not parse has no duration to count in
		{1 << 40, 2880, 48, maxGap},
	} {
		if got := webmGap(tc.late, tc.next, tc.tick); got != tc.want {
			t.Errorf("webmGap(%d, %d, %d) = %d, want %d", tc.late, tc.next, tc.tick, got, tc.want)
		}
	}
	for _, tc := range []struct {
		missing int64
		next    int
		want    int
	}{
		{479, 960, 0},
		{480, 960, 480},
		{1000, 960, 1000},
		{1 << 40, 960, maxGap},
	} {
		if got := concealedGap(tc.missing, tc.next); got != tc.want {
			t.Errorf("concealedGap(%d, %d) = %d, want %d", tc.missing, tc.next, got, tc.want)
		}
	}
}

// timestampedWebM writes mono packets as a WebM file with the given
// TimecodeScale (nanoseconds per tick), each block in a Cluster of its own at
// ticks[i], the timestamps a recorder chose rather than the samples' clock.
func timestampedWebM(t *testing.T, packets [][]byte, scale uint64, ticks []int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := webm.NewWriter(&buf, webm.WriterOptions{Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	file := buf.Bytes()
	msScale := []byte{0x2a, 0xd7, 0xb1, 0x83, 0x0f, 0x42, 0x40} // TimecodeScale 1000000
	at := bytes.Index(file, msScale)
	if at < 0 || scale >= 1<<24 {
		t.Fatal("cannot set the TimecodeScale")
	}
	file[at+4], file[at+5], file[at+6] = byte(scale>>16), byte(scale>>8), byte(scale)
	element := func(dst []byte, id []byte, payload []byte) []byte {
		dst = append(dst, id...)
		size := uint64(len(payload))
		dst = append(dst, 0x01, byte(size>>48), byte(size>>40), byte(size>>32), byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
		return append(dst, payload...)
	}
	for i, p := range packets {
		ts := uint64(ticks[i])
		cluster := element(nil, []byte{0xe7}, []byte{byte(ts >> 24), byte(ts >> 16), byte(ts >> 8), byte(ts)})
		cluster = element(cluster, []byte{0xa3}, append([]byte{0x81, 0, 0, 0x80}, p...))
		file = element(file, []byte{0x1f, 0x43, 0xb6, 0x75}, cluster)
	}
	return file
}

// Recorders timestamp WebM blocks with their capture clock, rounded to a
// tick: Chrome's lands milliseconds off the samples' clock (0, 61, 120, 181,
// ..., 422 for 60 ms packets), which is not lost audio. With such jitter, up
// to ±3 ms around the true times, at 1 ms, 100 µs and 10 ms ticks, a stream
// of 60 or 20 ms packets decodes to exactly its packets, as ffmpeg does and
// bit for bit as with exact timestamps; a lost block among them still
// conceals exactly one packet's duration. puropus used to read every 2 ms
// excess as 2.5 ms of lost audio.
func TestWebMTimestampJitterIsNotAGap(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		repeat  int
	}{
		{"webm-celt-60ms.webm", 6}, // 300 packets
		{"webm-celt-20ms.webm", 2}, // 300 packets
	} {
		once := fixturePackets(t, tc.fixture)
		packets := make([][]byte, 0, tc.repeat*len(once))
		for range tc.repeat {
			packets = append(packets, once...)
		}
		parsed, err := opus.Parse(packets[0], false)
		if err != nil {
			t.Fatal(err)
		}
		d := int64(parsed.Duration48())
		exact, _ := decodeAll(t, bytes.NewReader(timestampedWebM(t, packets, 1000000, multiples(len(packets), d/48))), DecodeOptions{})
		if int64(len(exact)) != int64(len(packets))*d {
			t.Fatalf("%s: %d samples from exact timestamps", tc.fixture, len(exact))
		}
		for _, scale := range []uint64{1000000, 100000, 10000000} {
			rng := rand.New(rand.NewPCG(uint64(d), scale))
			ticks := make([]int64, len(packets))
			for i := range ticks {
				ns := float64(int64(i)*d*int64(time.Second)/SampleRate) + (rng.Float64()*6-3)*1e6
				ticks[i] = max(int64(math.Round(ns/float64(scale))), 0)
			}
			name := fmt.Sprintf("%s, %d ns ticks", tc.fixture, scale)
			got, _ := decodeAll(t, bytes.NewReader(timestampedWebM(t, packets, scale, ticks)), DecodeOptions{})
			if !slices.Equal(got, exact) {
				t.Fatalf("%s: %d samples, want the %d of the packets", name, len(got), len(exact))
			}
			// Lose block 100, keeping the others' timestamps.
			lostPackets := slices.Concat(packets[:100], packets[101:])
			lostTicks := slices.Concat(ticks[:100], ticks[101:])
			src, err := ReadPackets(bytes.NewReader(timestampedWebM(t, lostPackets, scale, lostTicks)))
			if err != nil {
				t.Fatal(err)
			}
			if hole := src.Packets[100].Start - src.Packets[99].End(); hole != d || src.Duration() != samplesToDuration(int64(len(packets))*d) {
				t.Fatalf("%s: a lost block leaves a hole of %d samples, want %d", name, hole, d)
			}
		}
	}
}

// multiples returns n multiples of step, from 0.
func multiples(n int, step int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i) * step
	}
	return out
}

// The Chrome capture in testdata (MediaRecorder through Web Audio, the
// recorder's default settings; see testdata/manifest.json) has timestamps up
// to 6 ms off the samples' clock. It decodes to its packets and nothing
// more: 717 120 samples at 48 kHz and 239 040 at 16 kHz, as with ffmpeg 6.1.1
// and n9.0.2, bit for bit libopus's generic build on amd64, and its retained
// packets tile the timeline.
func TestChromeMediaRecorderCaptureHasNoGaps(t *testing.T) {
	data := readFixture(t, "webm-chrome-mediarecorder.webm")
	r, err := webm.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	worst, expected := int64(0), int64(-1)
	for {
		b, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		at := durationToSamples(b.Timestamp)
		if expected >= 0 {
			worst = max(worst, abs(at-expected))
		}
		parsed, err := opus.Parse(b.Data, false)
		if err != nil {
			t.Fatal(err)
		}
		expected = at + int64(parsed.Duration48())
	}
	if worst < 5*48 {
		t.Fatalf("the capture's timestamps are at most %d samples off: not the jitter this fixture is for", worst)
	}
	got, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepPackets: true})
	if len(got) != 717120 {
		t.Fatalf("%d samples at 48 kHz, ffmpeg's 717120", len(got))
	}
	if runtime.GOARCH == "amd64" || runtime.GOARCH == "386" {
		if !slices.Equal(got, readGolden48kPCM(t, "webm-chrome-mediarecorder.webm")) {
			t.Fatal("not libopus's generic decode bit for bit")
		}
	}
	got16, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: 16000})
	if len(got16) != 239040 {
		t.Fatalf("%d samples at 16 kHz, ffmpeg's 239040", len(got16))
	}
	packets := dec.Source().Packets
	for i := 1; i < len(packets); i++ {
		if packets[i].Start != packets[i-1].End() {
			t.Fatalf("a hole of %d samples before packet %d", packets[i].Start-packets[i-1].End(), i)
		}
	}
}

// Source.Native starts at the first retained packet and ends with the last,
// however ReleasePackets is paced around a gap: with every packet released
// before a gap, the gap's concealment used to be retained ahead of the next
// packet, so Native started early and an exact cut of what was retained
// failed with ErrNativeRequired. Releasing after every Read, before, during
// and after a gap, mono and stereo, at 48 and 16 kHz, from a byte at a time,
// the retained native PCM must be the full decode's for the retained
// packets, and an exact cut of them must succeed.
func TestReleaseAroundAGapKeepsNativeAligned(t *testing.T) {
	for _, gs := range gapStreams(t) {
		if gs.name == "mono SILK with FEC" {
			continue // mono and stereo are covered by the others
		}
		ch := gs.stream.Header.Channels
		lost := map[int]bool{60: true, 61: true, 62: true}
		data := webmWithout(t, gs.stream, lost)
		_, ref := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
		full := ref.Source().Native
		for _, rate := range []int{SampleRate, 16000} {
			for _, lag := range []time.Duration{0, 30 * ms} {
				name := fmt.Sprintf("%s, %d Hz, releasing %v behind", gs.name, rate, lag)
				d, err := NewDecoder(&chunkReader{data: data, size: 1}, DecodeOptions{SampleRate: rate, KeepNative: true})
				if err != nil {
					t.Fatal(err)
				}
				buf := make([]float32, rate/100*ch) // 10 ms per Read
				for reads := 0; ; reads++ {
					_, err := d.Read(buf)
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					d.ReleasePackets(d.Position() - lag)
					src := d.Source()
					if len(src.Packets) == 0 {
						if len(src.Native) != 0 {
							t.Fatalf("%s, read %d: %d native samples for no packets", name, reads, len(src.Native))
						}
						continue
					}
					from, to := src.Packets[0].Start, src.Packets[len(src.Packets)-1].End()
					if !slices.Equal(src.Native, full[from*int64(ch):to*int64(ch)]) {
						t.Fatalf("%s, read %d: %d native values for packets covering [%d, %d)", name, reads, len(src.Native), from, to)
					}
					if reads%9 == 0 && to-from >= 2*120 {
						if _, err := Cut(src, Edit{Keep: []Interval{{samplesToDuration(from), samplesToDuration(to)}}, Snap: SnapExact, Encoder: silenceEncoder{}}); err != nil {
							t.Fatalf("%s, read %d: an exact cut of what is retained: %v", name, reads, err)
						}
					}
				}
			}
		}
	}
}

// A lost WebM block need not last as long as the block after it. With a
// 20 ms block at 0 ms and a 60 ms block at 60 ms, 40 ms are missing; the
// gap used to be rounded to whole packets of the next block's duration,
// 60 ms, so the next block landed at 80 ms. A gap is now as long as the
// lateness, on the 2.5 ms grid, and whole packets only when the lateness is
// within 8 ms of them. In a stream of 20, 40 and 60 ms packets with ±3 ms
// of timestamp jitter, nothing is concealed; a lost block that the next
// block's duration divides comes out exactly, any other within the jitter
// of its length; and a lost block shorter than half the next one is not
// found, the price of not mistaking jitter for loss.
func TestWebMGapsOfOtherDurations(t *testing.T) {
	short, err := opus.Silence(1, opus.BandwidthFullband, 20*ms)
	if err != nil {
		t.Fatal(err)
	}
	long, err := opus.Silence(1, opus.BandwidthFullband, 60*ms)
	if err != nil {
		t.Fatal(err)
	}
	data := timestampedWebM(t, [][]byte{short[0], long[0]}, 1000000, []int64{0, 60})
	src, err := ReadPackets(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if pcm, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{}); len(pcm) != 5760 || src.Packets[1].Start != 2880 {
		t.Fatalf("%d samples, the 60 ms block at %d, want 5760 and 2880", len(pcm), src.Packets[1].Start)
	}

	byDuration := map[int64][][]byte{}
	for _, name := range []string{"webm-celt-20ms.webm", "webm-celt-40ms.webm", "webm-celt-60ms.webm"} {
		for _, p := range fixturePackets(t, name) {
			parsed, err := opus.Parse(p, false)
			if err != nil {
				t.Fatal(err)
			}
			d := int64(parsed.Duration48())
			byDuration[d] = append(byDuration[d], p)
		}
	}
	pattern := []int64{960, 1920, 2880, 960, 960, 2880, 1920, 1920, 960, 2880}
	packets := make([][]byte, 0, 300)
	durations := make([]int64, 0, 300)
	for i := range 300 {
		d := pattern[i%len(pattern)]
		pool := byDuration[d]
		packets = append(packets, pool[i%len(pool)])
		durations = append(durations, d)
	}
	starts := make([]int64, len(packets))
	for i := 1; i < len(starts); i++ {
		starts[i] = starts[i-1] + durations[i-1]
	}
	rng := rand.New(rand.NewPCG(7, 9))
	ticks := make([]int64, len(packets))
	for i, s := range starts {
		ticks[i] = max(int64(math.Round(float64(s)/48+rng.Float64()*6-3)), 0)
	}
	exact, _ := decodeAll(t, bytes.NewReader(timestampedWebM(t, packets, 1000000, multiplesOf(starts))), DecodeOptions{})
	if got, _ := decodeAll(t, bytes.NewReader(timestampedWebM(t, packets, 1000000, ticks)), DecodeOptions{}); !slices.Equal(got, exact) {
		t.Fatalf("jittered timestamps: %d samples, want the %d of the packets", len(got), len(exact))
	}
	jitter := int64(6 * 48)
	for i := 10; i+1 < len(packets); i++ {
		lost, next := durations[i], durations[i+1]
		data := timestampedWebM(t, slices.Concat(packets[:i], packets[i+1:]), 1000000, slices.Concat(ticks[:i], ticks[i+1:]))
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		hole := src.Packets[i].Start - src.Packets[i-1].End()
		switch {
		case lost < next/2-jitter:
			if hole != 0 {
				t.Fatalf("a %d-sample block lost before a %d-sample one: a hole of %d, want none found", lost, next, hole)
			}
		case lost%next == 0:
			if hole != lost {
				t.Fatalf("a %d-sample block lost before a %d-sample one: a hole of %d", lost, next, hole)
			}
		case lost >= next/2+jitter:
			if abs(hole-lost) > jitter+60 {
				t.Fatalf("a %d-sample block lost before a %d-sample one: a hole of %d", lost, next, hole)
			}
		}
	}
}

// multiplesOf returns sample positions as millisecond ticks.
func multiplesOf(starts []int64) []int64 {
	out := make([]int64, len(starts))
	for i, s := range starts {
		out[i] = s / 48
	}
	return out
}
