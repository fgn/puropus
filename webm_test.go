package puropus

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/fgn/puropus/internal/webm"
	"github.com/fgn/puropus/opus"
)

// withKnownSegmentSize rewrites the unknown size of the Segment that the
// WebM writer starts as a known size: the bytes after the Segment header,
// plus missing more that the file does not have.
func withKnownSegmentSize(t *testing.T, data []byte, missing int) []byte {
	t.Helper()
	header := []byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	at := bytes.Index(data, header)
	if at < 0 {
		t.Fatal("no unknown-size Segment")
	}
	out := bytes.Clone(data)
	size := uint64(len(data) - at - len(header) + missing)
	out[at+4] = 0x01 // an 8-byte size
	for i := range 7 {
		out[at+11-i] = byte(size >> (8 * i))
	}
	return out
}

// A WebM file whose known-size Segment declares bytes the input does not
// have is truncated, though it ends between two elements: the decoder
// delivers what it decoded and then reports ErrTruncated, and ReadPackets
// returns the packets with ErrTruncated. The same file with the size it has
// ends cleanly.
func TestDecodeReportsTruncatedKnownSizeWebMSegment(t *testing.T) {
	src := sourceFor(t, "webm-celt-60ms.webm")
	stream := &Stream{Header: Header{Channels: 1}, Packets: []Packet{{Data: src.Packets[1].Data, Duration: src.Packets[1].Coded()}}}
	var buf bytes.Buffer
	if err := stream.WriteWebM(&buf); err != nil {
		t.Fatal(err)
	}
	complete := withKnownSegmentSize(t, buf.Bytes(), 0)
	if pcm, _ := decodeAll(t, bytes.NewReader(complete), DecodeOptions{}); len(pcm) != 2880 {
		t.Fatalf("complete Segment: %d samples, want 2880", len(pcm))
	}
	truncated := withKnownSegmentSize(t, buf.Bytes(), 10)
	dec, err := NewDecoder(bytes.NewReader(truncated), DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := readAll(dec, make([]float32, 4096))
	if !errors.Is(err, ErrTruncated) || len(pcm) != 2880 {
		t.Fatalf("Segment 10 bytes short: %d samples, error %v, want 2880 and ErrTruncated", len(pcm), err)
	}
	if got, err := ReadPackets(bytes.NewReader(truncated)); !errors.Is(err, ErrTruncated) || len(got.Packets) != 1 {
		t.Fatalf("ReadPackets: %d packets, error %v", len(got.Packets), err)
	}
}

// webmFile writes packets as a single-track WebM file whose OpusHead has the
// given pre-skip, also written as CodecDelay, giving packet i the
// DiscardPadding discard[i] in 48 kHz samples: positive from its end,
// negative from its start.
func webmFile(t *testing.T, channels, preSkip int, packets [][]byte, discard map[int]int) []byte {
	t.Helper()
	var buf bytes.Buffer
	head := opus.Head{Version: 1, Channels: channels, PreSkip: preSkip, InputSampleRate: SampleRate}
	w, err := webm.NewWriter(&buf, webm.WriterOptions{Channels: channels, CodecPrivate: head.Marshal(), CodecDelay: samplesToDuration(int64(preSkip))})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range packets {
		parsed, err := opus.Parse(p, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.WriteBlock(p, parsed.Duration48(), discard[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A negative WebM DiscardPadding drops audio from the start of its block
// (Matroska), on the first block or any later one: the decoder, Position,
// the retained packets (as Skip) and their native PCM all leave it out. The
// rest of the audio is what the block decodes to, so the output is the
// decode without the padding with those samples cut. ffmpeg returns the same
// samples: 2400 for a 60 ms block with -10 ms, and the pre-skip comes on top
// of a first block's padding. A cut keeps such a block's Skip at the head of
// its output, as pre-skip, and elsewhere emits the samples as a synthetic
// span, as for any skip it cannot carry.
func TestNegativeDiscardPaddingTrimsTheBlockStart(t *testing.T) {
	packets := fixturePackets(t, "webm-celt-60ms.webm")[:20] // 2880 samples each
	plain, _ := decodeAll(t, bytes.NewReader(webmFile(t, 1, 0, packets, nil)), DecodeOptions{})
	for _, tc := range []struct {
		name    string
		preSkip int
		block   int // the block with -480 samples of padding
	}{
		{"first block", 0, 0},
		{"first block after the pre-skip", 312, 0},
		{"later block", 0, 10},
	} {
		data := webmFile(t, 1, tc.preSkip, packets, map[int]int{tc.block: -480})
		ref, _ := decodeAll(t, bytes.NewReader(webmFile(t, 1, tc.preSkip, packets, nil)), DecodeOptions{})
		at := tc.block * 2880
		if tc.block == 0 {
			at = 0
		}
		want := slices.Concat(ref[:at], ref[at+480:])
		if tc.block > 0 && maxAbsDiff(ref, plain) != 0 {
			t.Fatal("the reference decode differs")
		}
		got, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
		if !slices.Equal(got, want) || dec.Position() != samplesToDuration(int64(len(want))) {
			t.Fatalf("%s: %d samples, position %v, want %d", tc.name, len(got), dec.Position(), len(want))
		}
		got16, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: 16000})
		if !slices.Equal(got16, resampleTo16k(want, 1)) {
			t.Fatalf("%s: the 16 kHz decode is not the resampled 48 kHz one", tc.name)
		}
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		kept := dec.Source()
		assertSamePackets(t, tc.name, kept.Packets, src.Packets)
		if p := src.Packets[tc.block]; p.Skip != 480+tc.preSkip || p.Duration != 2880-480-tc.preSkip || p.Start != int64(at) {
			t.Fatalf("%s: block %d is %+v", tc.name, tc.block, p)
		}
		if !slices.Equal(kept.Native, want) {
			t.Fatalf("%s: retained native PCM differs", tc.name)
		}

		out, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
		if err != nil {
			t.Fatal(err)
		}
		var spans []Span
		if tc.block == 0 {
			spans = []Span{{Out: 0, In: 0, Length: src.Duration()}}
			if out.Header.PreSkip != 480+tc.preSkip {
				t.Fatalf("%s: output pre-skip %d", tc.name, out.Header.PreSkip)
			}
		} else {
			split := samplesToDuration(int64(at))
			skip := samplesToDuration(480)
			spans = []Span{
				{Out: 0, In: 0, Length: split},
				{Out: split, Length: skip, Synthetic: true},
				{Out: split + skip, In: split, Length: src.Duration() - split},
			}
		}
		assertSpans(t, out, spans)
		for _, format := range []Format{FormatOgg, FormatWebM} {
			pcm := decodeStream(t, out, format)
			if int64(len(pcm)) != durationToSamples(out.Duration()) {
				t.Fatalf("%s, %v: the cut decodes to %d samples for %v", tc.name, format, len(pcm), out.Duration())
			}
			if tc.block == 0 && !slices.Equal(pcm, want) {
				t.Fatalf("%s, %v: a whole cut does not round-trip", tc.name, format)
			}
		}
	}
}
