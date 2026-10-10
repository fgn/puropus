package puropus

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/fgn/puropus/opus"
)

// Where RFC 3533 and RFC 7845 leave room, these tests specify what the
// ffmpeg command does with the same files.

// oggCRC is the Ogg page checksum (polynomial 0x04c11db7, no reflection).
func oggCRC(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// oggPage locates one page of an Ogg file.
type oggPage struct {
	start, size int
	lacing      []byte
}

// oggPages splits an Ogg file into its pages.
func oggPages(t *testing.T, data []byte) []oggPage {
	t.Helper()
	var pages []oggPage
	for off := 0; off < len(data); {
		if off+27 > len(data) || string(data[off:off+4]) != "OggS" {
			t.Fatalf("no page header at offset %d", off)
		}
		nseg := int(data[off+26])
		lacing := data[off+27 : off+27+nseg]
		size := 27 + nseg
		for _, l := range lacing {
			size += int(l)
		}
		pages = append(pages, oggPage{start: off, size: size, lacing: lacing})
		off += size
	}
	return pages
}

// fixOggCRC recomputes the checksum of page p after an edit.
func fixOggCRC(data []byte, p oggPage) {
	page := data[p.start : p.start+p.size]
	binary.LittleEndian.PutUint32(page[22:], 0)
	binary.LittleEndian.PutUint32(page[22:], oggCRC(page))
}

// pageSamples returns the 48 kHz duration of the packets completing on page p
// (the fixtures never split a packet across pages).
func pageSamples(t *testing.T, data []byte, p oggPage) int {
	t.Helper()
	body := data[p.start+27+len(p.lacing) : p.start+p.size]
	total := 0
	var cur []byte
	for _, l := range p.lacing {
		cur = append(cur, body[:l]...)
		body = body[l:]
		if l < 255 {
			parsed, err := opus.Parse(cur, false)
			if err != nil {
				t.Fatal(err)
			}
			total += parsed.Duration48()
			cur = nil
		}
	}
	return total
}

// A corrupt page in the middle of a recording is dropped and decoding
// continues. The output is the clean decode minus that page's samples; the
// audio before the page is untouched; end trimming still applies because it
// is anchored to granule positions, not to a running sum of what was decoded.
func TestDecodeSkipsCorruptOggPage(t *testing.T) {
	clean := readFixture(t, "ogg-celt-fb.opus")
	want, _ := decodeAll(t, bytes.NewReader(clean), DecodeOptions{})
	pages := oggPages(t, clean)
	if len(pages) < 5 {
		t.Fatalf("fixture has %d pages", len(pages))
	}
	// Pages 0 and 1 hold OpusHead and OpusTags; corrupt the second audio page.
	target := pages[3]
	before := pageSamples(t, clean, pages[2]) - 312 // first audio page minus pre-skip
	dropped := pageSamples(t, clean, target)

	for name, corrupt := range map[string]func([]byte){
		"payload byte":  func(d []byte) { d[target.start+27+len(target.lacing)+40] ^= 0x01 },
		"segment count": func(d []byte) { d[target.start+26] ^= 0x40 },
	} {
		data := append([]byte(nil), clean...)
		corrupt(data)
		got, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
		if len(got) != len(want)-dropped {
			t.Fatalf("%s: %d samples, want %d (clean %d minus the page's %d)", name, len(got), len(want)-dropped, len(want), dropped)
		}
		for i := 0; i < before; i++ {
			if got[i] != want[i] {
				t.Fatalf("%s: sample %d differs before the corrupt page", name, i)
			}
		}
		if dec.Position() != samplesToDuration(int64(len(got))) {
			t.Fatalf("%s: position %v for %d samples", name, dec.Position(), len(got))
		}
	}
}

// RFC 7845 §4 lets a stream's granule positions start at an offset. That
// must not change the decoded output or defeat end trimming (FFmpeg anchors
// its timeline to the page granules).
func TestOggGranuleOffsetDoesNotChangeOutput(t *testing.T) {
	clean := readFixture(t, "ogg-celt-fb.opus")
	want, _ := decodeAll(t, bytes.NewReader(clean), DecodeOptions{})
	data := append([]byte(nil), clean...)
	for _, p := range oggPages(t, data)[2:] {
		g := int64(binary.LittleEndian.Uint64(data[p.start+6:]))
		if g >= 0 {
			binary.LittleEndian.PutUint64(data[p.start+6:], uint64(g+1_000_000))
			fixOggCRC(data, p)
		}
	}
	got, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	if len(got) != len(want) {
		t.Fatalf("%d samples with offset granules, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d differs", i)
		}
	}
}

// TestSinglePageOggEndTrim pins end trimming for a stream whose audio fits in
// one page: there is no previous page granule to anchor to, so the expected
// end is the page's own duration counted from 0, and the final granule
// trims the encoder padding of the last packet. FFmpeg decodes this fixture
// to 2002 samples at 48 kHz and 667 at 16 kHz.
func TestSinglePageOggEndTrim(t *testing.T) {
	data := readFixture(t, "ogg-celt-short.opus")
	for _, tc := range []struct{ rate, want int }{{48000, 2002}, {16000, 667}} {
		got, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: tc.rate})
		if len(got) != tc.want {
			t.Fatalf("%d Hz: %d samples, want %d", tc.rate, len(got), tc.want)
		}
		if dec.Position() != samplesAtRateToDuration(int64(tc.want), int64(tc.rate)) {
			t.Fatalf("%d Hz: position %v for %d samples", tc.rate, dec.Position(), tc.want)
		}
	}
	src, err := ReadPackets(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Packets) != 3 || src.Header.PreSkip != 312 {
		t.Fatalf("%d packets with pre-skip %d, want 3 with 312", len(src.Packets), src.Header.PreSkip)
	}
}

// A chained file (two complete Ogg Opus streams back to back) decodes to the
// concatenation of the two decodes: the second link's header is applied (its
// own pre-skip and gain), and the codec state is reset at the boundary.
func TestDecodeChainedOggStreams(t *testing.T) {
	a := readFixture(t, "ogg-celt-fb.opus")
	b := readFixture(t, "ogg-silk-wb.opus")
	wantA, _ := decodeAll(t, bytes.NewReader(a), DecodeOptions{})
	wantB, _ := decodeAll(t, bytes.NewReader(b), DecodeOptions{})
	chained := append(append([]byte(nil), a...), b...)

	got, dec := decodeAll(t, bytes.NewReader(chained), DecodeOptions{})
	if len(got) != len(wantA)+len(wantB) {
		t.Fatalf("%d samples, want %d + %d", len(got), len(wantA), len(wantB))
	}
	for i := range wantA {
		if got[i] != wantA[i] {
			t.Fatalf("first link differs at sample %d", i)
		}
	}
	for i := range wantB {
		if got[len(wantA)+i] != wantB[i] {
			t.Fatalf("second link differs at sample %d", i)
		}
	}
	if dec.Header().Format != FormatOgg || dec.Header().Channels != 1 {
		t.Fatalf("header %+v", dec.Header())
	}

	// The packet view sees both links on one continuous timeline.
	srcA, err := ReadPackets(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	srcB, err := ReadPackets(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	src, err := ReadPackets(bytes.NewReader(chained))
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Packets) != len(srcA.Packets)+len(srcB.Packets) {
		t.Fatalf("%d packets, want %d + %d", len(src.Packets), len(srcA.Packets), len(srcB.Packets))
	}
	for i := 1; i < len(src.Packets); i++ {
		if src.Packets[i].Start < src.Packets[i-1].Start {
			t.Fatalf("packet %d starts at %d before packet %d at %d", i, src.Packets[i].Start, i-1, src.Packets[i-1].Start)
		}
	}
	firstB := src.Packets[len(srcA.Packets)]
	if firstB.Start != int64(len(wantA)) || firstB.Skip != srcB.Header.PreSkip {
		t.Fatalf("second link starts at %d skipping %d, want %d skipping its pre-skip %d", firstB.Start, firstB.Skip, len(wantA), srcB.Header.PreSkip)
	}
	if lastA := src.Packets[len(srcA.Packets)-1]; lastA.End() != int64(len(wantA)) || lastA.Trim == 0 {
		t.Fatalf("first link ends at %d with trim %d, want %d with the encoder padding trimmed", lastA.End(), lastA.Trim, len(wantA))
	}
}

// In a grouped Ogg file the Opus stream's BOS page need not come first
// (RFC 3533 §4, RFC 7845 §1). The decoder selects the Opus stream.
func TestDecodeGroupedOggSelectsTheOpusStream(t *testing.T) {
	grouped := readFixture(t, "ogg-grouped-vorbis-opus.ogg")
	only := readFixture(t, "ogg-grouped-opus-only.opus")
	want, _ := decodeAll(t, bytes.NewReader(only), DecodeOptions{})
	got, dec := decodeAll(t, bytes.NewReader(grouped), DecodeOptions{})
	if len(got) != len(want) || len(got) == 0 {
		t.Fatalf("%d samples from the grouped file, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d differs", i)
		}
	}
	if dec.Header().Channels != 1 || dec.Header().PreSkip != 312 {
		t.Fatalf("header %+v", dec.Header())
	}
	src, err := ReadPackets(bytes.NewReader(grouped))
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Packets) != 51 {
		t.Fatalf("%d packets, want 51", len(src.Packets))
	}
}
