package puropus

import (
	"bytes"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/fgn/puropus/opus"
)

// decodeStream writes s in the given container and decodes the file at
// 48 kHz.
func decodeStream(t *testing.T, s *Stream, format Format) []float32 {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Write(&buf, format); err != nil {
		t.Fatal(err)
	}
	pcm, _ := decodeAll(t, bytes.NewReader(buf.Bytes()), DecodeOptions{})
	return pcm
}

func TestCutOutwardKeepsOverlappingPacketsOnly(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm") // 60 ms packets on a 60 ms grid
	out, err := Cut(src, Edit{Keep: []Interval{{100 * ms, 250 * ms}, {1000 * ms, 1020 * ms}}})
	if err != nil {
		t.Fatal(err)
	}
	// [100,250) overlaps packets 1..4 (60..300 ms); [1000,1020) overlaps packet 16 (960..1020).
	if len(out.Packets) != 5 {
		t.Fatalf("kept %d packets: %+v", len(out.Packets), out.Packets)
	}
	for i, want := range []int{1, 2, 3, 4, 16} {
		if !bytes.Equal(out.Packets[i].Data, src.Packets[want].Data) {
			t.Fatalf("output packet %d is not source packet %d", i, want)
		}
	}
	if out.Duration() != 300*ms {
		t.Fatalf("duration %v", out.Duration())
	}
	if len(out.Spans) != 2 || out.Spans[0].In != 60*ms || out.Spans[0].Length != 240*ms || out.Spans[1].Out != 240*ms || out.Spans[1].In != 960*ms {
		t.Fatalf("spans %+v", out.Spans)
	}
	if in, ok := out.InputTime(250 * ms); !ok || in != 970*ms {
		t.Fatalf("InputTime(250ms) = %v, %v", in, ok)
	}
	if _, ok := out.InputTime(300 * ms); ok {
		t.Fatal("InputTime past the end should fail")
	}
}

func TestCutInwardDropsPartialPackets(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	out, err := Cut(src, Edit{Keep: []Interval{{100 * ms, 250 * ms}, {1000 * ms, 1020 * ms}}, Snap: SnapInward})
	if err != nil {
		t.Fatal(err)
	}
	// Only packets 2 and 3 (120..240 ms) are fully inside; the 20 ms interval selects nothing.
	if len(out.Packets) != 2 || !bytes.Equal(out.Packets[0].Data, src.Packets[2].Data) {
		t.Fatalf("kept %d packets", len(out.Packets))
	}
}

// The first packet of a source with pre-skip contributes only the samples
// after the pre-skip, so it starts at 0 and inward selection can keep it.
func TestCutInwardKeepsFirstPacketOfPreSkipSource(t *testing.T) {
	src := sourceFor(t, "ogg-celt-fb.opus")
	if src.Header.PreSkip == 0 || src.Packets[0].Skip != src.Header.PreSkip {
		t.Fatalf("fixture should have pre-skip; header %+v first packet %+v", src.Header, src.Packets[0])
	}
	out, err := Cut(src, Edit{Keep: []Interval{{0, 1000 * ms}}, Snap: SnapInward})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) == 0 || !bytes.Equal(out.Packets[0].Data, src.Packets[0].Data) {
		t.Fatalf("inward [0,1s) did not keep the first packet; first kept = %+v", out.Packets[:min(1, len(out.Packets))])
	}
	// The first packet ends at 960-312 = 648 samples = 13.5 ms, entirely inside [0, 20 ms).
	out, err = Cut(src, Edit{Keep: []Interval{{0, 20 * ms}}, Snap: SnapInward})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) != 1 || !bytes.Equal(out.Packets[0].Data, src.Packets[0].Data) {
		t.Fatalf("inward [0,20ms) kept %d packets, want exactly the first", len(out.Packets))
	}
}

// A pre-skip longer than a packet leaves whole packets that contribute
// nothing. They are kept with the packet that follows, so the output decodes
// exactly like the source.
func TestCutKeepsPacketsInsideALongPreSkip(t *testing.T) {
	base := sourceFor(t, "ogg-celt-fb-10ms.opus") // 480-sample packets, pre-skip 120
	packets := append([]Packet(nil), base.Packets...)
	packets[0] = Packet{Data: packets[0].Data, Start: 0, Duration: 0, Skip: 480}
	packets[1] = Packet{Data: packets[1].Data, Start: 0, Duration: 360, Skip: 120}
	for i := 2; i < len(packets); i++ {
		packets[i].Start = packets[i-1].End()
	}
	var buf bytes.Buffer
	if err := (&Stream{Header: base.Header, Packets: packets}).WriteOgg(&buf); err != nil {
		t.Fatal(err)
	}
	direct, _ := decodeAll(t, bytes.NewReader(buf.Bytes()), DecodeOptions{})
	src, err := ReadPackets(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if src.Header.PreSkip != 600 || src.Packets[0].Duration != 0 || src.Packets[0].Skip != 480 || src.Packets[1].Skip != 120 || src.Packets[1].Duration != 360 {
		t.Fatalf("header %+v, packets %+v %+v", src.Header, src.Packets[0], src.Packets[1])
	}
	if int64(len(direct)) != durationToSamples(src.Duration()) {
		t.Fatalf("decoded %d samples, source duration covers %d", len(direct), durationToSamples(src.Duration()))
	}

	out, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}, Snap: SnapInward})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) != len(src.Packets) || !bytes.Equal(out.Packets[0].Data, src.Packets[0].Data) || out.Header.PreSkip != 600 {
		t.Fatalf("kept %d of %d packets with pre-skip %d", len(out.Packets), len(src.Packets), out.Header.PreSkip)
	}
	for _, format := range []Format{FormatOgg, FormatWebM} {
		pcm := decodeStream(t, out, format)
		if len(pcm) != len(direct) || maxAbsDiff(pcm, direct) != 0 {
			t.Fatalf("%v: round trip decodes %d samples, differs by %g", format, len(pcm), maxAbsDiff(pcm, direct))
		}
	}
	// A region starting later drops the leading run with the first packet.
	out, err = Cut(src, Edit{Keep: []Interval{{100 * ms, 200 * ms}}, Snap: SnapInward})
	if err != nil {
		t.Fatal(err)
	}
	if out.Packets[0].Duration == 0 || out.Header.PreSkip != 0 {
		t.Fatalf("interior cut starts with %+v, pre-skip %d", out.Packets[0], out.Header.PreSkip)
	}
}

func TestCutExtendMergesAndClamps(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	out, err := Cut(src, Edit{
		Keep:   []Interval{{500 * ms, 520 * ms}, {130 * ms, 140 * ms}, {10 * ms, 20 * ms}}, // unsorted
		Extend: Margin{Before: 100 * ms, After: 100 * ms},
	})
	if err != nil {
		t.Fatal(err)
	}
	// After extension: [0,120) [30,240) merge to [0,240); [400,620).
	if len(out.Spans) != 2 || out.Spans[0].In != 0 || out.Spans[0].Length != 240*ms || out.Spans[1].In != 360*ms || out.Spans[1].Length != 300*ms {
		t.Fatalf("spans %+v", out.Spans)
	}
	if out.Duration() != 540*ms {
		t.Fatalf("duration %v", out.Duration())
	}
}

func TestCutInsertsSilenceThatDecodesToZeros(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	out, err := Cut(src, Edit{
		Keep:    []Interval{{0, 120 * ms}, {600 * ms, 720 * ms}},
		Silence: Silence{Leading: 20 * ms, Between: 130 * ms, Trailing: 2500 * time.Microsecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDuration := 20*ms + 120*ms + 130*ms + 120*ms + 2500*time.Microsecond
	if out.Duration() != wantDuration {
		t.Fatalf("duration %v want %v", out.Duration(), wantDuration)
	}
	if len(out.Spans) != 5 || !out.Spans[0].Synthetic || out.Spans[1].Synthetic || !out.Spans[2].Synthetic || !out.Spans[4].Synthetic {
		t.Fatalf("spans %+v", out.Spans)
	}
	if _, ok := out.InputTime(10 * ms); ok {
		t.Fatal("silence must not map to input time")
	}
	if in, ok := out.InputTime(20*ms + 30*ms); !ok || in != 30*ms {
		t.Fatalf("InputTime inside first region = %v, %v", in, ok)
	}
	for _, format := range []Format{FormatWebM, FormatOgg} {
		pcm := decodeStream(t, out, format)
		if len(pcm) != int(wantDuration*SampleRate/time.Second) {
			t.Fatalf("%v: decoded %d samples, want %d", format, len(pcm), int(wantDuration*SampleRate/time.Second))
		}
		// Leading silence on a fresh decoder is zero but for the constant
		// offset, below 1e-33, that libopus's float de-emphasis keeps to
		// avoid denormal numbers.
		for i := 0; i < 20*48; i++ {
			if pcm[i] > 1e-33 || pcm[i] < -1e-33 {
				t.Fatalf("%v: leading silence sample %d = %g", format, i, pcm[i])
			}
		}
		// The gap after the first region: the silence frames decode to zero
		// coefficients, so only the de-emphasis filter's geometric tail from
		// the preceding audio remains; after 10 ms it is far below -160 dBFS.
		gapStart := (20 + 120) * 48
		for i := gapStart + 480; i < gapStart+130*48; i++ {
			if pcm[i] > 1e-8 || pcm[i] < -1e-8 {
				t.Fatalf("%v: inserted silence sample %d = %g", format, i, pcm[i])
			}
		}
	}
}

// Silence separates regions that contribute packets: a region that selects
// nothing adds no silence around it.
func TestCutInsertsSilenceOnlyBetweenRegionsThatKeepPackets(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm") // 60 ms packets on a 60 ms grid, pre-skip 0

	// [100,110) contains no whole packet, so inward selection keeps nothing
	// there. The output must be exactly the one packet of [600,660) with no
	// silence before it.
	out, err := Cut(src, Edit{
		Keep:    []Interval{{100 * ms, 110 * ms}, {600 * ms, 660 * ms}},
		Snap:    SnapInward,
		Silence: Silence{Between: 100 * ms},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) != 1 || !bytes.Equal(out.Packets[0].Data, src.Packets[10].Data) {
		t.Fatalf("kept %d packets, want only source packet 10: %+v", len(out.Packets), out.Packets)
	}
	if out.Duration() != 60*ms {
		t.Fatalf("duration %v, want 60ms (no silence)", out.Duration())
	}
	for _, s := range out.Spans {
		if s.Synthetic {
			t.Fatalf("synthetic span emitted next to an empty region: %+v", out.Spans)
		}
	}

	// Three intervals with an empty middle one: exactly one silence between
	// the two non-empty regions, never two adjacent silences.
	out, err = Cut(src, Edit{
		Keep:    []Interval{{0, 120 * ms}, {200 * ms, 210 * ms}, {600 * ms, 660 * ms}},
		Snap:    SnapInward,
		Silence: Silence{Between: 100 * ms},
	})
	if err != nil {
		t.Fatal(err)
	}
	synthetic := 0
	for i, s := range out.Spans {
		if s.Synthetic {
			synthetic++
			if i == 0 || i == len(out.Spans)-1 {
				t.Fatalf("silence at the edge of the output: %+v", out.Spans)
			}
			if out.Spans[i-1].Synthetic || out.Spans[i+1].Synthetic {
				t.Fatalf("adjacent silence spans: %+v", out.Spans)
			}
		}
	}
	if synthetic != 1 {
		t.Fatalf("%d silence spans, want 1: %+v", synthetic, out.Spans)
	}
	if want := 120*ms + 100*ms + 60*ms; out.Duration() != want {
		t.Fatalf("duration %v, want %v", out.Duration(), want)
	}

	// A lone interval that keeps nothing yields an empty stream even with
	// leading and trailing silence requested: there is nothing to pad.
	out, err = Cut(src, Edit{
		Keep:    []Interval{{100 * ms, 110 * ms}},
		Snap:    SnapInward,
		Silence: Silence{Leading: 100 * ms, Between: 100 * ms, Trailing: 100 * ms},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) != 0 || len(out.Spans) != 0 || out.Duration() != 0 {
		t.Fatalf("empty selection produced %d packets, %d spans, %v", len(out.Packets), len(out.Spans), out.Duration())
	}
}

// Keeping the source's last packet as the output's last keeps its end
// trimming, so the encoder padding does not reappear.
func TestCutKeepsEndTrimmingOnTheLastPacket(t *testing.T) {
	data := readFixture(t, "ogg-celt-fb.opus")
	direct, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	src := sourceFor(t, "ogg-celt-fb.opus")
	last := src.Packets[len(src.Packets)-1]
	if last.Trim == 0 {
		t.Fatalf("fixture should trim its last packet: %+v", last)
	}
	out, err := Cut(src, Edit{Keep: []Interval{{2500 * ms, 3000 * ms}}})
	if err != nil {
		t.Fatal(err)
	}
	from := durationToSamples(out.Spans[0].In)
	if want := int64(len(direct)) - from; durationToSamples(out.Duration()) != want {
		t.Fatalf("duration %v covers %d samples, want %d", out.Duration(), durationToSamples(out.Duration()), want)
	}
	for _, format := range []Format{FormatOgg, FormatWebM} {
		pcm := decodeStream(t, out, format)
		if int64(len(pcm)) != int64(len(direct))-from {
			t.Fatalf("%v: decoded %d samples, want %d", format, len(pcm), int64(len(direct))-from)
		}
		// The first kept packet decodes from a fresh state; the rest matches.
		tail := len(pcm) - 200*48
		if d := maxAbsDiff(pcm[tail:], direct[len(direct)-200*48:]); d > 1e-4 {
			t.Fatalf("%v: last 200 ms differ from the source decode by up to %g", format, d)
		}
	}
}

// A pre-skip or end trimming that does not fall at the corresponding end of
// the output cannot be written to either container. Cut emits those samples
// as audio and maps them as synthetic, so the timeline stays exact.
func TestCutFoldsInteriorSkipAndTrimIntoSyntheticSpans(t *testing.T) {
	src := sourceFor(t, "ogg-celt-fb.opus")
	first, last := src.Packets[0], src.Packets[len(src.Packets)-1]
	whole := Interval{0, src.Duration()}

	out, err := Cut(src, Edit{Keep: []Interval{whole}, Silence: Silence{Trailing: 100 * ms}})
	if err != nil {
		t.Fatal(err)
	}
	wantSpans := []Span{
		{Out: 0, In: 0, Length: 3000 * ms},
		{Out: 3000 * ms, Length: samplesToDuration(int64(last.Trim)) + 100*ms, Synthetic: true},
	}
	assertSpans(t, out, wantSpans)
	if out.Header.PreSkip != first.Skip {
		t.Fatalf("pre-skip %d, want the source's %d", out.Header.PreSkip, first.Skip)
	}

	out, err = Cut(src, Edit{Keep: []Interval{whole}, Silence: Silence{Leading: 100 * ms}})
	if err != nil {
		t.Fatal(err)
	}
	warmUp := samplesToDuration(int64(first.Skip))
	wantSpans = []Span{
		{Out: 0, Length: 100*ms + warmUp, Synthetic: true},
		{Out: 100*ms + warmUp, In: 0, Length: 3000 * ms},
	}
	assertSpans(t, out, wantSpans)
	if out.Header.PreSkip != 0 {
		t.Fatalf("pre-skip %d after leading silence, want 0", out.Header.PreSkip)
	}
}

// chainedOgg chains links: each is a fixture's packets with its pre-skip,
// end trimming and the given output gain, written as an Ogg link.
func chainedOgg(t *testing.T, gains []int16, names ...string) []byte {
	t.Helper()
	var chain bytes.Buffer
	for i, name := range names {
		src := sourceFor(t, name)
		s := &Stream{Header: src.Header, Packets: src.Packets}
		s.Header.OutputGain = gains[i]
		if err := s.WriteOgg(&chain); err != nil {
			t.Fatal(err)
		}
	}
	return chain.Bytes()
}

// A cut of a chained Ogg stream keeps its links: the packets of each source
// link go to a link of their own, with that link's output gain, its pre-skip
// on its leading packets and its end trimming on its last, so WriteOgg
// writes a chain whose decoder resets where the source's did. A whole cut
// therefore decodes exactly as the source, gains of 0 and +6 dB included
// (it used to lose the second link's gain and reset: 48% RMS error), and
// the span map has no synthetic stretch at the boundary. A cut across the
// boundary starts the second link at its own first packet. WebM cannot
// carry two gains, so writing that cut as WebM fails; with equal gains it
// is written, the second link's pre-skip and the first one's end trimming
// as DiscardPadding, with no decoder reset between them.
func TestCutKeepsChainedLinks(t *testing.T) {
	for _, gains := range [][]int16{{0, 0}, {0, 6 * 256}} {
		chained := chainedOgg(t, gains, "ogg-celt-fb.opus", "ogg-silk-wb.opus")
		direct, dec := decodeAll(t, bytes.NewReader(chained), DecodeOptions{KeepPackets: true})
		src, err := ReadPackets(bytes.NewReader(chained))
		if err != nil {
			t.Fatal(err)
		}
		assertSamePackets(t, "Source", dec.Source().Packets, src.Packets)
		if !slices.Equal(dec.Source().Links, src.Links) {
			t.Fatalf("Decoder links %+v, ReadPackets links %+v", dec.Source().Links, src.Links)
		}
		if len(src.Links) != 2 || src.Links[1].OutputGain != gains[1] || src.Links[1].PreSkip != 312 || src.Links[0] != src.Header {
			t.Fatalf("source links %+v", src.Links)
		}
		boundary := len(sourceFor(t, "ogg-celt-fb.opus").Packets)
		if src.Packets[boundary-1].Link != 0 || src.Packets[boundary].Link != 1 {
			t.Fatalf("link numbers %d, %d at the boundary", src.Packets[boundary-1].Link, src.Packets[boundary].Link)
		}

		whole, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
		if err != nil {
			t.Fatal(err)
		}
		assertSpans(t, whole, []Span{{Out: 0, In: 0, Length: 6000 * ms}})
		if len(whole.Links) != 2 || whole.Links[0].PreSkip != 312 || whole.Links[1].PreSkip != 312 || whole.Links[1].OutputGain != gains[1] {
			t.Fatalf("output links %+v", whole.Links)
		}
		if pcm := decodeStream(t, whole, FormatOgg); !slices.Equal(pcm, direct) {
			t.Fatalf("gains %v: a whole cut written as Ogg decodes to %d samples, differing by %g", gains, len(pcm), maxAbsDiff(pcm, direct))
		}

		across, err := Cut(src, Edit{Keep: []Interval{{2500 * ms, 3500 * ms}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(across.Links) != 2 || across.Links[0].PreSkip != 0 || across.Links[1].PreSkip != 312 {
			t.Fatalf("links of a cut across the boundary %+v", across.Links)
		}
		if in, ok := across.InputTime(across.Duration() - ms); !ok || in != across.Spans[len(across.Spans)-1].In+across.Spans[len(across.Spans)-1].Length-ms {
			t.Fatalf("InputTime at the end: %v, %v", in, ok)
		}
		pcm := decodeStream(t, across, FormatOgg)
		if int64(len(pcm)) != durationToSamples(across.Duration()) {
			t.Fatalf("the cut across decodes to %d samples for %v", len(pcm), across.Duration())
		}
		// The second link decodes as in the source: reset, its gain applied.
		second := durationToSamples(3000*ms) - durationToSamples(across.Spans[0].In)
		if d := maxAbsDiff(pcm[second:], direct[3000*48:]); d != 0 {
			t.Fatalf("gains %v: the second link differs by %g", gains, d)
		}

		var buf bytes.Buffer
		err = whole.WriteWebM(&buf)
		if gains[1] != 0 {
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("WebM output of two gains: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		webmPCM, _ := decodeAll(t, bytes.NewReader(buf.Bytes()), DecodeOptions{})
		if len(webmPCM) != len(direct) || maxAbsDiff(webmPCM[:3000*48], direct) != 0 {
			t.Fatalf("WebM output: %d samples, first link differs by %g", len(webmPCM), maxAbsDiff(webmPCM[:3000*48], direct))
		}
		webmSrc, err := ReadPackets(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		first, next := webmSrc.Packets[boundary-1], webmSrc.Packets[boundary]
		if first.Trim != src.Packets[boundary-1].Trim || next.Skip != 312 || first.Trim == 0 {
			t.Fatalf("WebM boundary packets %+v %+v", first, next)
		}
	}
}

func TestCutRejectsBadInput(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	if _, err := Cut(src, Edit{Keep: []Interval{{100 * ms, 100 * ms}}}); err == nil {
		t.Fatal("empty interval accepted")
	}
	if _, err := Cut(src, Edit{Keep: []Interval{{0, 60 * ms}}, Silence: Silence{Between: 3 * ms}}); err == nil {
		t.Fatal("off-grid silence accepted")
	}
	if _, err := Cut(src, Edit{Keep: []Interval{{0, 60 * ms}}, Snap: SnapExact}); !errors.Is(err, ErrEncoderRequired) {
		t.Fatalf("want ErrEncoderRequired, got %v", err)
	}
	if _, err := Cut(src, Edit{Keep: []Interval{{0, 60 * ms}}, Snap: SnapExact, Encoder: silenceEncoder{}}); !errors.Is(err, ErrNativeRequired) {
		t.Fatalf("want ErrNativeRequired, got %v", err)
	}
	if _, err := Cut(Source{}, Edit{}); err == nil {
		t.Fatal("empty source accepted")
	}
	out, err := Cut(src, Edit{Keep: []Interval{{5 * time.Second, 6 * time.Second}}})
	if err != nil || len(out.Packets) != 0 {
		t.Fatalf("interval past the end: %v, %d packets", err, len(out.Packets))
	}
}

func TestCutAcceptsMaxDurationAsIntervalEnd(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	out, err := Cut(src, Edit{Keep: []Interval{{End: time.Duration(1<<63 - 1)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) != len(src.Packets) {
		t.Fatalf("kept %d packets, want all %d", len(out.Packets), len(src.Packets))
	}
}

func TestCutExtendSaturatesAtTheDurationLimits(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	out, err := Cut(src, Edit{
		Keep:   []Interval{{Start: math.MinInt64 + 1, End: math.MaxInt64}},
		Extend: Margin{Before: 40 * ms, After: 40 * ms},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) != len(src.Packets) {
		t.Fatalf("kept %d packets, want all %d", len(out.Packets), len(src.Packets))
	}
}

func TestCutRejectsUnknownSnapModeWhenNothingIsSelected(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	beyond := src.Duration() + time.Second
	if _, err := Cut(src, Edit{Keep: []Interval{{beyond, beyond + time.Second}}, Snap: SnapExact + 1}); err == nil {
		t.Fatal("Cut accepted an unknown snap mode")
	}
}

// silenceEncoder stands in for a real Opus encoder: it emits silence frames
// of the requested length, which is enough to exercise the exact-cut
// geometry.
type silenceEncoder struct{}

func (silenceEncoder) Encode(pcm []float32, channels int) ([][]byte, error) {
	return opus.Silence(channels, opus.BandwidthFullband, time.Duration(len(pcm)/channels)*time.Second/SampleRate)
}

// recordingEncoder keeps the PCM it was asked to encode and returns silence
// of the same length, so tests can check which audio reached the encoder.
type recordingEncoder struct {
	pcm [][]float32
}

func (e *recordingEncoder) Encode(pcm []float32, channels int) ([][]byte, error) {
	e.pcm = append(e.pcm, append([]float32(nil), pcm...))
	return silenceEncoder{}.Encode(pcm, channels)
}

func TestCutExactReencodesGuardsAndCopiesInterior(t *testing.T) {
	data := readFixture(t, "webm-celt-60ms.webm")
	_, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
	src := dec.Source()
	out, err := Cut(src, Edit{Keep: []Interval{{100 * ms, 700 * ms}}, Snap: SnapExact, Encoder: silenceEncoder{}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Duration() != 600*ms {
		t.Fatalf("duration %v", out.Duration())
	}
	// Interior packets fully inside [180, 620): packets 4..9 (240..600).
	copied := 0
	for _, p := range out.Packets {
		for _, s := range src.Packets[4:10] {
			if bytes.Equal(p.Data, s.Data) {
				copied++
			}
		}
	}
	if copied != 6 {
		t.Fatalf("copied %d interior packets, want 6", copied)
	}
	if in, ok := out.InputTime(0); !ok || in != 100*ms {
		t.Fatalf("InputTime(0) = %v, %v", in, ok)
	}
	if in, ok := out.InputTime(599 * ms); !ok || in != 699*ms {
		t.Fatalf("InputTime(599ms) = %v, %v", in, ok)
	}
	// A region too short for any interior packet is re-encoded whole.
	short, err := Cut(src, Edit{Keep: []Interval{{100 * ms, 200 * ms}}, Snap: SnapExact, Encoder: silenceEncoder{}})
	if err != nil {
		t.Fatal(err)
	}
	if short.Duration() != 100*ms {
		t.Fatalf("short duration %v", short.Duration())
	}
}

// The guards of an exact cut are re-encoded from the audio at the interval
// boundary, on sources with pre-skip and after ReleasePackets, and the output
// maps back to within 1.25 ms of the requested interval.
func TestCutExactReencodesTheAudioAtTheBoundary(t *testing.T) {
	// A 60 ms WebM (pre-skip 0), with the first second released: packets
	// 20..22 (1.2 s to 1.38 s) are interior, the guards are [1.1, 1.2) and
	// [1.38, 1.5).
	data := readFixture(t, "webm-celt-60ms.webm")
	direct, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	dec, err := NewDecoder(bytes.NewReader(data), DecodeOptions{KeepNative: true})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float32, SampleRate)
	for total := 0; total < 2*SampleRate; {
		n, err := dec.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	dec.ReleasePackets(1 * time.Second)
	src := dec.Source()
	if src.Packets[0].Start == 0 {
		t.Fatal("ReleasePackets kept the first packet")
	}
	enc := &recordingEncoder{}
	out, err := Cut(src, Edit{Keep: []Interval{{1100 * ms, 1500 * ms}}, Snap: SnapExact, Encoder: enc})
	if err != nil {
		t.Fatal(err)
	}
	assertSpans(t, out, []Span{{Out: 0, In: 1100 * ms, Length: 400 * ms}})
	assertEncoded(t, enc, direct, [][2]int{{52800, 57600}, {66240, 72000}})
	copied := 0
	for _, p := range out.Packets {
		for _, s := range src.Packets {
			if s.Start >= 57600 && s.End() <= 66240 && bytes.Equal(p.Data, s.Data) {
				copied++
			}
		}
	}
	if copied != 3 {
		t.Fatalf("copied %d interior packets, want 3", copied)
	}

	// A pre-skip source: packet boundaries sit at 648+960k, off the 2.5 ms
	// grid of the timeline, so the guards round to the packet they meet:
	// [101, 193.5) ms before packet 9, [613.5, 701) ms after packet 29.
	data = readFixture(t, "ogg-celt-fb.opus")
	direct, dec = decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
	enc = &recordingEncoder{}
	out, err = Cut(dec.Source(), Edit{Keep: []Interval{{100 * ms, 700 * ms}}, Snap: SnapExact, Encoder: enc})
	if err != nil {
		t.Fatal(err)
	}
	assertSpans(t, out, []Span{{Out: 0, In: 101 * ms, Length: 600 * ms}})
	assertEncoded(t, enc, direct, [][2]int{{4848, 9288}, {29448, 33648}})

	// An interval past the end is clamped to the audio there is.
	data = readFixture(t, "ogg-celt-fb-10ms.opus")
	_, dec = decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
	enc = &recordingEncoder{}
	out, err = Cut(dec.Source(), Edit{Keep: []Interval{{2000 * ms, 5000 * ms}}, Snap: SnapExact, Encoder: enc})
	if err != nil {
		t.Fatal(err)
	}
	if in, ok := out.InputTime(0); !ok || in < 2000*ms-1250*time.Microsecond || in > 2000*ms+1250*time.Microsecond {
		t.Fatalf("InputTime(0) = %v, %v", in, ok)
	}
	if end := out.Spans[len(out.Spans)-1]; end.In+end.Length != 3000*ms {
		t.Fatalf("exact cut ends at %v, want the source end", end.In+end.Length)
	}
	for _, pcm := range enc.pcm {
		if len(pcm) == 0 || len(pcm)%120 != 0 {
			t.Fatalf("encoder received %d samples", len(pcm))
		}
	}
}

// assertSpans checks the span map of out, that the spans end where the
// stream does, and that the stream is writable.
func assertSpans(t *testing.T, out *Stream, want []Span) {
	t.Helper()
	if len(out.Spans) != len(want) {
		t.Fatalf("spans %+v, want %+v", out.Spans, want)
	}
	for i := range want {
		if out.Spans[i] != want[i] {
			t.Fatalf("span %d is %+v, want %+v", i, out.Spans[i], want[i])
		}
	}
	if end := want[len(want)-1]; out.Duration() != end.Out+end.Length {
		t.Fatalf("duration %v, spans end at %v", out.Duration(), end.Out+end.Length)
	}
	if _, err := out.links(true); err != nil {
		t.Fatal(err)
	}
}

// assertEncoded checks that the encoder received exactly the source audio of
// the given sample ranges, in order.
func assertEncoded(t *testing.T, enc *recordingEncoder, direct []float32, ranges [][2]int) {
	t.Helper()
	if len(enc.pcm) != len(ranges) {
		t.Fatalf("encoder called %d times, want %d", len(enc.pcm), len(ranges))
	}
	for i, r := range ranges {
		if len(enc.pcm[i]) != r[1]-r[0] {
			t.Fatalf("call %d encoded %d samples, want [%d, %d)", i, len(enc.pcm[i]), r[0], r[1])
		}
		if d := maxAbsDiff(enc.pcm[i], direct[r[0]:r[1]]); d != 0 {
			t.Fatalf("call %d did not receive the source audio of [%d, %d): differs by %g", i, r[0], r[1], d)
		}
	}
}

// Cut rejects the settings of an Edit that have no meaning instead of giving
// them an accidental one: a negative Guard used to copy packets far outside
// an exact interval (1.2 s for [100, 200) ms with a guard of -1 s), and a
// negative margin shrank the intervals. Unsorted and overlapping intervals
// are not errors: they are sorted and merged.
func TestCutRejectsMeaninglessEdits(t *testing.T) {
	data := readFixture(t, "webm-celt-60ms.webm")
	_, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
	src := dec.Source()
	interval := []Interval{{100 * ms, 200 * ms}}
	for name, edit := range map[string]Edit{
		"negative guard":         {Keep: interval, Snap: SnapExact, Guard: -time.Second, Encoder: silenceEncoder{}},
		"negative guard, inward": {Keep: interval, Snap: SnapInward, Guard: -ms},
		"negative margin before": {Keep: interval, Extend: Margin{Before: -10 * ms}},
		"negative margin after":  {Keep: interval, Extend: Margin{After: -10 * ms}},
		"negative silence":       {Keep: interval, Silence: Silence{Leading: -2500 * time.Microsecond}},
		"off-grid silence":       {Keep: interval, Silence: Silence{Trailing: time.Millisecond}},
		"empty interval":         {Keep: []Interval{{100 * ms, 100 * ms}}},
		"reversed interval":      {Keep: []Interval{{200 * ms, 100 * ms}}},
		"unknown snap mode":      {Keep: interval, Snap: -1},
	} {
		if out, err := Cut(src, edit); err == nil {
			t.Fatalf("%s: accepted, %v of output", name, out.Duration())
		}
	}
	exact, err := Cut(src, Edit{Keep: interval, Snap: SnapExact, Encoder: silenceEncoder{}})
	if err != nil || exact.Duration() != 100*ms {
		t.Fatalf("exact [100, 200) ms: %v, %v", exact.Duration(), err)
	}
}

// SnapExact rounds each re-encoded stretch to the nearest 2.5 ms, so an edge
// of its output lands within 1.25 ms of the interval's, even at an end of
// the source, where too little audio is left for the rounded length: the
// encoder gets silence past the end, which the output then discards as
// pre-skip or end trimming. Before, the length was rounded down there, and a
// whole exact cut of a 959-sample source returned 840 samples, 2.48 ms short.
func TestCutExactKeepsItsToleranceAtTheSourceEnds(t *testing.T) {
	silence, err := opus.Silence(1, opus.BandwidthFullband, 20*ms)
	if err != nil {
		t.Fatal(err)
	}
	tiny := Source{Header: Header{Channels: 1}, Packets: []Packet{{Data: silence[0], Duration: 959, Trim: 1}}, Native: make([]float32, 959)}
	out, err := Cut(tiny, Edit{Keep: []Interval{{0, tiny.Duration()}}, Snap: SnapExact, Encoder: silenceEncoder{}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Duration() != tiny.Duration() {
		t.Fatalf("a whole exact cut of %v returned %v", tiny.Duration(), out.Duration())
	}

	data := readFixture(t, "ogg-celt-fb.opus") // pre-skip 312, end trimming 648
	_, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
	src := dec.Source()
	end := durationToSamples(src.Duration())
	const tolerance = 60 // 1.25 ms
	for k := int64(0); k < 400; k += 23 {
		for _, reg := range []region{{k, 48000}, {50000, end - k}, {k, end - k}, {k, k + 2000}, {end - 2000 - k, end - k}} {
			out, err := Cut(src, Edit{Keep: []Interval{{samplesToDuration(reg.start), samplesToDuration(reg.end)}}, Snap: SnapExact, Encoder: silenceEncoder{}})
			if err != nil {
				t.Fatal(err)
			}
			first, last := out.Spans[0], out.Spans[len(out.Spans)-1]
			from, to := durationToSamples(first.In), durationToSamples(last.In+last.Length)
			if first.Synthetic || last.Synthetic || abs(from-reg.start) > tolerance || abs(to-reg.end) > tolerance {
				t.Fatalf("[%d, %d): the output covers [%d, %d), spans %+v", reg.start, reg.end, from, to, out.Spans)
			}
			if d := durationToSamples(out.Duration()) - (reg.end - reg.start); abs(d) > 2*tolerance {
				t.Fatalf("[%d, %d): %v of output", reg.start, reg.end, out.Duration())
			}
			for _, format := range []Format{FormatOgg, FormatWebM} {
				if pcm := decodeStream(t, out, format); int64(len(pcm)) != durationToSamples(out.Duration()) {
					t.Fatalf("[%d, %d), %v: %d samples for %v", reg.start, reg.end, format, len(pcm), out.Duration())
				}
			}
		}
	}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
