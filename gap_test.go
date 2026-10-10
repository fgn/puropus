package puropus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
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
// more than a timestamp tick, and then whole packets are missing; an Ogg
// page's missing audio counts exactly, once it reaches half a packet.
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
		{1440, 2880, 48, 2880}, // half: one packet
		{2880 - 288, 2880, 48, 2880},
		{2880 + 288, 2880, 48, 2880},
		{2*2880 + 96, 2880, 48, 2 * 2880},
		{479, 960, 48, 0},
		{480, 960, 48, 960},
		{480, 960, 480, 0}, // a tick of 10 ms is rounding, not loss
		{481, 960, 480, 960},
		{1000, 0, 48, 0}, // a packet that does not parse has no duration to count in
		{1 << 40, 2880, 48, maxGap / 2880 * 2880},
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
