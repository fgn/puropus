package puropus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
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
			var packets [][]byte
			for range 4 {
				for _, p := range decodeLogStream(t, c).Packets {
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
// §4.1, is filled up to the jump, so positions follow the granule positions,
// and no gap is filled beyond 10 minutes, against a corrupt or hostile
// granule position.
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
