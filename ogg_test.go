package puropus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
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

// makeOggPage assembles a page. Its pieces are laced in order, each as a
// complete packet or the end of one, except that with open set the last
// piece is the start of a packet continued on the next page, a multiple of
// 255 bytes long.
func makeOggPage(t *testing.T, serial, sequence uint32, flags byte, granule int64, open bool, pieces ...[]byte) []byte {
	t.Helper()
	var lacing, payload []byte
	for i, piece := range pieces {
		lacing = append(lacing, bytes.Repeat([]byte{255}, len(piece)/255)...)
		if !open || i < len(pieces)-1 {
			lacing = append(lacing, byte(len(piece)%255))
		} else if len(piece)%255 != 0 {
			t.Fatalf("open piece of %d bytes", len(piece))
		}
		payload = append(payload, piece...)
	}
	if len(lacing) > 255 {
		t.Fatalf("%d segments", len(lacing))
	}
	page := make([]byte, 27, 27+len(lacing)+len(payload))
	copy(page, "OggS")
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:], uint64(granule))
	binary.LittleEndian.PutUint32(page[14:], serial)
	binary.LittleEndian.PutUint32(page[18:], sequence)
	page[26] = byte(len(lacing))
	page = append(append(page, lacing...), payload...)
	binary.LittleEndian.PutUint32(page[22:], oggCRC(page))
	return page
}

// padOpusPacket reframes a single-frame (code 0) Opus packet as a code 3
// packet of size bytes. Decoders ignore the padding (RFC 6716 §3.2.5), so it
// decodes as before.
func padOpusPacket(t *testing.T, packet []byte, size int) []byte {
	t.Helper()
	if packet[0]&3 != 0 {
		t.Fatal("not a code 0 packet")
	}
	frame := packet[1:]
	for lengthBytes := 1; ; lengthBytes++ {
		padding := size - 2 - lengthBytes - len(frame)
		last := padding - 254*(lengthBytes-1) // each 255 byte adds 254
		if padding < 0 || last < 0 {
			t.Fatalf("cannot pad %d bytes to %d", len(packet), size)
		}
		if last > 254 {
			continue
		}
		out := []byte{packet[0] | 3, 0x40 | 1} // padding flag, one frame
		out = append(out, bytes.Repeat([]byte{255}, lengthBytes-1)...)
		out = append(out, byte(last))
		out = append(out, frame...)
		return append(out, make([]byte, padding)...)
	}
}

// groupedSpanningOgg rebuilds the grouped Vorbis and Opus fixture so that
// Opus packet 10, padded to 1000 bytes, spans three pages: the end of the
// first, all of the second, and the start of the third, with the Vorbis
// stream's last page between the second and the third. It returns the pages
// and the indexes of those three.
func groupedSpanningOgg(t *testing.T) (pages [][]byte, middle, vorbis, completing int) {
	t.Helper()
	data := readFixture(t, "ogg-grouped-vorbis-opus.ogg")
	src, err := ReadPackets(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	fixture := oggPages(t, data) // Vorbis, Opus, Vorbis, Opus, Vorbis (last), Opus audio, Opus EOS
	page := func(i int) []byte { return data[fixture[i].start : fixture[i].start+fixture[i].size] }
	opusSerial := binary.LittleEndian.Uint32(page(1)[14:])
	audio := make([][]byte, len(src.Packets))
	for i, p := range src.Packets {
		audio[i] = p.Data
	}
	if len(audio) != 51 || !bytes.Equal(page(5)[27+len(fixture[5].lacing):][:len(audio[0])], audio[0]) {
		t.Fatal("unexpected fixture layout")
	}
	granule := func(i int) int64 { return int64(binary.LittleEndian.Uint64(page(i)[6:])) }
	big := padOpusPacket(t, audio[10], 1000)
	const continued, eos = 0x01, 0x04
	pages = [][]byte{
		page(0), page(1), page(2), page(3),
		makeOggPage(t, opusSerial, 2, 0, 10*960, true, append(audio[:10:10], big[:510])...),
		makeOggPage(t, opusSerial, 3, continued, -1, true, big[510:765]),
		page(4),
		makeOggPage(t, opusSerial, 4, continued, granule(5), false, append([][]byte{big[765:]}, audio[11:50]...)...),
		makeOggPage(t, opusSerial, 5, eos, granule(6), false, audio[50]),
	}
	return pages, 5, 6, 7
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
// continues. The page's audio is concealed, since the granule position of
// the page after it shows how much is missing (RFC 7845 §4.1), so the output
// keeps the clean decode's length and every later sample its position; the
// audio before the page is untouched, and end trimming still applies.
// ffmpeg drops the page's samples instead: 96 000 of 144 000.
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
		if len(got) != len(want) {
			t.Fatalf("%s: %d samples, want the clean decode's %d", name, len(got), len(want))
		}
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		lost := dropped / 960
		if hole := src.Packets[50].Start - src.Packets[49].End(); len(src.Packets) != 151-lost || hole != int64(dropped) {
			t.Fatalf("%s: %d packets, a hole of %d samples after the first page", name, len(src.Packets), hole)
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

// In a grouped file an Opus packet can span pages with a page of another
// stream between them. When that page is corrupt it is dropped and the Opus
// packet is untouched, so the Opus output is exactly that of the intact file,
// whatever the damage: even a serial number that reads as the Opus stream's
// cannot be trusted, and only the Opus stream's own page sequence numbers
// show whether it lost a page. ffmpeg's output does not change either, except
// when the corrupt page's size runs past the end of the input, as it does
// here for the segment count: ffmpeg then loses every page that size
// swallowed.
func TestGroupedOggCorruptPageOfAnotherStreamKeepsOpusPacket(t *testing.T) {
	pages, _, vorbis, _ := groupedSpanningOgg(t)
	want, _ := decodeAll(t, bytes.NewReader(readFixture(t, "ogg-grouped-vorbis-opus.ogg")), DecodeOptions{})
	// The padded packet spanning pages decodes as the fixture's did.
	assertSamePCM(t, "intact", bytes.Join(pages, nil), want)

	opusSerial := binary.LittleEndian.Uint32(pages[1][14:])
	for name, corrupt := range map[string]func([]byte){
		"payload byte":         func(p []byte) { p[27+int(p[26])+7] ^= 0x01 },
		"serial reads as ours": func(p []byte) { binary.LittleEndian.PutUint32(p[14:], opusSerial) },
		"segment count":        func(p []byte) { p[26] ^= 0x40 }, // the page runs past the end of the input
	} {
		page := append([]byte(nil), pages[vorbis]...)
		corrupt(page)
		data := bytes.Join(slices.Concat(pages[:vorbis], [][]byte{page}, pages[vorbis+1:]), nil)
		assertSamePCM(t, name, data, want)
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil || len(src.Packets) != 51 {
			t.Fatalf("%s: %d packets, %v", name, len(src.Packets), err)
		}
	}
}

// A corrupt page of the Opus stream itself costs the packets completed on it
// and the packet spanning it, whether it lies inside that packet or completes
// it: the fragments on either side are never joined, and the audio is
// concealed, so the output keeps its length. The audio before it is
// untouched and decoding goes on after it. For the page inside the packet,
// ffmpeg joins the fragments it has, fails to decode the result and puts a
// packet's duration of silence in its place, so it keeps the length too; for
// the page that completes it, ffmpeg drops the 40 packets lost and returns
// 10 248 samples.
func TestGroupedOggCorruptOpusPageDropsSpanningPacket(t *testing.T) {
	pages, middle, _, completing := groupedSpanningOgg(t)
	want, _ := decodeAll(t, bytes.NewReader(bytes.Join(pages, nil)), DecodeOptions{})
	const intact = 10*960 - 312 // the first ten packets, after the pre-skip
	for _, tc := range []struct {
		name    string
		page    int
		samples int
		packets int
	}{
		{"inside the packet", middle, len(want), 50},
		// Packets 10 to 49 are lost, and the page after them is the final
		// one, where the lost audio and the end trimming cannot be told
		// apart: the gap reaches the final granule position, and the last
		// packet keeps its encoder padding.
		{"completing the packet", completing, len(want), 11},
	} {
		data := bytes.Clone(bytes.Join(pages, nil))
		at := 0
		for _, p := range pages[:tc.page] {
			at += len(p)
		}
		data[at+len(pages[tc.page])-1] ^= 0x01
		got, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
		if len(got) != tc.samples || dec.Position() != samplesToDuration(int64(tc.samples)) {
			t.Fatalf("%s: %d samples, position %v, want %d", tc.name, len(got), dec.Position(), tc.samples)
		}
		if d := maxAbsDiff(got[:intact], want); d != 0 {
			t.Fatalf("%s: the audio before the lost page differs by %g", tc.name, d)
		}
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil || len(src.Packets) != tc.packets || durationToSamples(src.Duration()) != int64(tc.samples) {
			t.Fatalf("%s: %d packets covering %v, %v", tc.name, len(src.Packets), src.Duration(), err)
		}
	}
}

// assertSamePCM decodes data at 48 kHz and requires exactly want.
func assertSamePCM(t *testing.T, name string, data []byte, want []float32) {
	t.Helper()
	got, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	if len(got) != len(want) {
		t.Fatalf("%s: %d samples, want %d", name, len(got), len(want))
	}
	for i := range want {
		if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
			t.Fatalf("%s: sample %d differs", name, i)
		}
	}
}

// When the EOS page of a chained link is lost (corrupt, missing, or cut
// short where another file was appended), the next link still starts at its
// BOS page, since RFC 3533 places every BOS page of a link before its other
// pages. The file then decodes exactly as if the first link had ended on the
// page before the lost one: the second link's header applies, the codec is
// reset, and the timeline runs on. Only the lost page's packets are missing,
// and with them the first link's end trimming, which no longer has anything
// to trim. ffmpeg decodes the same samples from the fixtures chained this
// way (143 688 and 144 000), and fails on a grouped second link even when
// nothing is lost.
func TestDecodeChainedOggWithLostEOSPage(t *testing.T) {
	a := readFixture(t, "ogg-celt-fb.opus")
	pagesA := oggPages(t, a)
	eos := pagesA[len(pagesA)-1]
	// The first link as it would be had it ended on the page before.
	ended := bytes.Clone(a[:eos.start])
	before := pagesA[len(pagesA)-2]
	ended[before.start+5] |= 0x04
	fixOggCRC(ended, before)

	// A second link with its own pre-skip and output gain.
	b := bytes.Clone(readFixture(t, "ogg-silk-wb.opus"))
	binary.LittleEndian.PutUint16(b[27+1+10:], 1000) // OpusHead pre-skip
	binary.LittleEndian.PutUint16(b[27+1+16:], 768)  // output gain, +3 dB
	fixOggCRC(b, oggPages(t, b)[0])
	wantA, _ := decodeAll(t, bytes.NewReader(ended), DecodeOptions{})
	wantB, _ := decodeAll(t, bytes.NewReader(b), DecodeOptions{})

	for _, next := range []struct {
		name string
		data []byte
	}{
		{"new serial", b},
		{"same serial", a},
		{"grouped", readFixture(t, "ogg-grouped-vorbis-opus.ogg")},
	} {
		reference := append(bytes.Clone(ended), next.data...)
		want48, _ := decodeAll(t, bytes.NewReader(reference), DecodeOptions{})
		want16, _ := decodeAll(t, bytes.NewReader(reference), DecodeOptions{SampleRate: 16000})
		wantSrc, err := ReadPackets(bytes.NewReader(reference))
		if err != nil {
			t.Fatal(err)
		}
		_, refDec := decodeAll(t, bytes.NewReader(reference), DecodeOptions{KeepNative: true})
		wantNative := refDec.Source().Native
		for _, lost := range []struct {
			name string
			page []byte
		}{
			{"corrupt", append(bytes.Clone(a[eos.start:eos.start+eos.size-1]), a[eos.start+eos.size-1]^0x01)},
			{"missing", nil},
			{"cut short", a[eos.start : eos.start+40]},
		} {
			name := next.name + ", EOS page " + lost.name
			data := slices.Concat(a[:eos.start], lost.page, next.data)
			assertSamePCM(t, name, data, want48)
			got16, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: 16000})
			if len(got16) != len(want16) || maxAbsDiff(got16, want16) != 0 {
				t.Fatalf("%s: 16 kHz decode differs", name)
			}
			src, err := ReadPackets(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			assertSamePackets(t, name, src.Packets, wantSrc.Packets)
			_, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
			kept := dec.Source()
			assertSamePackets(t, name+", Source", kept.Packets, wantSrc.Packets)
			if len(kept.Native) != len(wantNative) || maxAbsDiff(kept.Native, wantNative) != 0 {
				t.Fatalf("%s: %d native samples for %v", name, len(kept.Native), kept.Duration())
			}
		}
		if next.name == "new serial" {
			if len(want48) != len(wantA)+len(wantB) || maxAbsDiff(want48, slices.Concat(wantA, wantB)) != 0 {
				t.Fatalf("%d samples, want the two links' %d + %d", len(want48), len(wantA), len(wantB))
			}
		}
	}
}

// Cut sees the file with a lost EOS page on one timeline too, with its two
// links, so a whole cut decodes exactly as the file does: the first link's
// last packet has nothing left to trim, and the second link keeps its
// pre-skip.
func TestCutChainedOggWithLostEOSPage(t *testing.T) {
	a := readFixture(t, "ogg-celt-fb.opus")
	b := readFixture(t, "ogg-silk-wb.opus")
	pagesA := oggPages(t, a)
	eos := pagesA[len(pagesA)-1]
	data := slices.Concat(a[:eos.start+eos.size-1], []byte{a[eos.start+eos.size-1] ^ 0x01}, b)
	direct, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepPackets: true})
	src := dec.Source()
	firstB := len(sourceFor(t, "ogg-celt-fb.opus").Packets) - 1
	linkA := src.Packets[firstB-1].End()
	if src.Packets[firstB-1].Trim != 0 || src.Packets[firstB].Skip != 312 || src.Packets[firstB].Start != linkA {
		t.Fatalf("boundary packets %+v %+v", src.Packets[firstB-1], src.Packets[firstB])
	}
	if durationToSamples(src.Duration()) != int64(len(direct)) || len(direct) != 143688+144000 {
		t.Fatalf("source covers %v, decoder produced %d samples", src.Duration(), len(direct))
	}
	out, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
	if err != nil {
		t.Fatal(err)
	}
	assertSpans(t, out, []Span{{Out: 0, In: 0, Length: src.Duration()}})
	if len(out.Links) != 2 || out.Links[1].PreSkip != 312 {
		t.Fatalf("output links %+v", out.Links)
	}
	if pcm := decodeStream(t, out, FormatOgg); !slices.Equal(pcm, direct) {
		t.Fatalf("cut decodes to %d samples, differing by %g", len(pcm), maxAbsDiff(pcm, direct))
	}
}

// assertSamePackets requires two packet lists to match field by field.
func assertSamePackets(t *testing.T, name string, got, want []Packet) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d packets, want %d", name, len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if !bytes.Equal(g.Data, w.Data) || g.Start != w.Start || g.Duration != w.Duration || g.Skip != w.Skip || g.Trim != w.Trim || g.Link != w.Link {
			t.Fatalf("%s: packet %d is %+v, want %+v", name, i, g, w)
		}
	}
}

// A chained link whose OpusTags page is lost goes on to its audio: its
// header still applies, and the file decodes exactly as if the page were
// there, as with ffmpeg n9.0.2 (ffmpeg 6.1.1 stops at the link). A chained
// link that ends after its OpusHead is truncated, and without the first
// link's OpusTags the input is not taken for Ogg Opus, as with ffmpeg.
func TestDecodeChainedOggWithLostTagsPage(t *testing.T) {
	a := readFixture(t, "ogg-celt-fb.opus")
	b := readFixture(t, "ogg-silk-wb.opus")
	pagesB := oggPages(t, b)
	want, _ := decodeAll(t, bytes.NewReader(slices.Concat(a, b)), DecodeOptions{})
	wantSrc, err := ReadPackets(bytes.NewReader(slices.Concat(a, b)))
	if err != nil {
		t.Fatal(err)
	}
	tags := pagesB[1]
	corrupt := bytes.Clone(b)
	corrupt[tags.start+tags.size-1] ^= 0x01
	missing := slices.Concat(b[:tags.start], b[tags.start+tags.size:])
	for name, link := range map[string][]byte{"corrupt": corrupt, "missing": missing} {
		data := slices.Concat(a, link)
		assertSamePCM(t, name, data, want)
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertSamePackets(t, name, src.Packets, wantSrc.Packets)
	}

	headOnly := slices.Concat(a, b[:tags.start])
	dec, err := NewDecoder(bytes.NewReader(headOnly), DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pcm, err := readAll(dec, make([]float32, 4096)); !errors.Is(err, ErrTruncated) || errors.Is(err, io.EOF) || len(pcm) != 144000 {
		t.Fatalf("chained link ending after its OpusHead: %d samples, %v", len(pcm), err)
	}
	if _, err := ReadPackets(bytes.NewReader(headOnly)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("ReadPackets of a link ending after its OpusHead: %v", err)
	}

	first := bytes.Clone(a)
	first[oggPages(t, a)[1].start+40] ^= 0x01
	if _, err := NewDecoder(bytes.NewReader(first), DecodeOptions{}); err == nil {
		t.Fatal("first link without OpusTags accepted")
	}
}

// An Ogg file in which no BOS page carries OpusHead is not Opus: a file with
// only a Vorbis stream is ErrUnsupported, not truncated, and so is one whose
// only OpusHead page is corrupt, the error then counting the dropped pages.
// A file that ends after its OpusHead is truncated.
func TestOggWithoutOpusStreamIsUnsupported(t *testing.T) {
	grouped := readFixture(t, "ogg-grouped-vorbis-opus.ogg")
	pages := oggPages(t, grouped) // Vorbis, Opus, Vorbis, Opus, Vorbis, Opus, Opus
	page := func(i int) []byte { return grouped[pages[i].start : pages[i].start+pages[i].size] }
	vorbis := slices.Concat(page(0), page(2), page(4))
	corruptHead := bytes.Clone(readFixture(t, "ogg-celt-fb.opus"))
	corruptHead[40] ^= 0x01
	for name, data := range map[string][]byte{"Vorbis only": vorbis, "corrupt OpusHead": corruptHead} {
		_, err := NewDecoder(bytes.NewReader(data), DecodeOptions{})
		if !errors.Is(err, ErrUnsupported) || errors.Is(err, ErrTruncated) {
			t.Fatalf("%s: NewDecoder: %v, want ErrUnsupported", name, err)
		}
		if _, err := ReadPackets(bytes.NewReader(data)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s: ReadPackets: %v, want ErrUnsupported", name, err)
		}
		if name == "corrupt OpusHead" && !strings.Contains(err.Error(), "1 corrupt pages dropped") {
			t.Fatalf("%s: %v does not count the dropped page", name, err)
		}
	}
	a := readFixture(t, "ogg-celt-fb.opus")
	if _, err := NewDecoder(bytes.NewReader(a[:oggPages(t, a)[1].start]), DecodeOptions{}); !errors.Is(err, ErrTruncated) || errors.Is(err, io.EOF) {
		t.Fatalf("OpusHead alone: %v, want ErrTruncated", err)
	}
}

// RFC 7845 §4.4 sets the final page's sample count by its granule position,
// so the end trimming spreads over the page's packets, from the last back,
// when it is longer than the last packet: a packet trimmed whole contributes
// nothing and keeps a Trim of its coded duration. ffmpeg decodes these files
// to the same counts (480 samples for three 20 ms packets with a final
// granule of 480); puropus used to trim only the last packet and returned
// 1920.
func TestOggEndTrimSpansTheFinalPage(t *testing.T) {
	short := readFixture(t, "ogg-celt-short.opus") // three 960-sample packets on one audio page, pre-skip 312
	untrimmed := bytes.Clone(short)
	audio := oggPages(t, short)[2]
	binary.LittleEndian.PutUint64(untrimmed[audio.start+6:], 2880)
	fixOggCRC(untrimmed, audio)
	full, _ := decodeAll(t, bytes.NewReader(untrimmed), DecodeOptions{})
	if len(full) != 2880-312 {
		t.Fatalf("untrimmed: %d samples", len(full))
	}
	silence, err := opus.Silence(1, opus.BandwidthFullband, 20*ms)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		data     []byte
		granule  int64
		samples  int
		duration []int // of the three packets
		trim     []int
	}{
		{"silence, no pre-skip", silencePage(t, silence[0], 0, 480), 480, 480, []int{480, 0, 0}, []int{480, 960, 960}},
		{"into the pre-skip", silencePage(t, silence[0], 312, 480), 480, 168, []int{168, 0, 0}, []int{480, 960, 960}},
		{"fixture", nil, 1000, 1000 - 312, []int{648, 40, 0}, []int{0, 920, 960}},
		// Granule 100 ends the stream inside the pre-skip: no audio at all.
		{"inside the pre-skip", nil, 100, 0, []int{0, 0, 0}, []int{860, 960, 960}},
	} {
		data := tc.data
		if data == nil {
			data = bytes.Clone(short)
			binary.LittleEndian.PutUint64(data[audio.start+6:], uint64(tc.granule))
			fixOggCRC(data, audio)
		}
		got, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepPackets: true})
		if len(got) != tc.samples || dec.Position() != samplesToDuration(int64(tc.samples)) {
			t.Fatalf("%s: %d samples, position %v, want %d", tc.name, len(got), dec.Position(), tc.samples)
		}
		if tc.data == nil && maxAbsDiff(got, full) != 0 {
			t.Fatalf("%s: the trimmed decode is not a prefix of the untrimmed one", tc.name)
		}
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		assertSamePackets(t, tc.name, dec.Source().Packets, src.Packets)
		for i, p := range src.Packets {
			if p.Duration != tc.duration[i] || p.Trim != tc.trim[i] {
				t.Fatalf("%s: packet %d contributes %d and trims %d, want %d and %d", tc.name, i, p.Duration, p.Trim, tc.duration[i], tc.trim[i])
			}
		}
		if tc.samples == 0 {
			continue
		}
		out, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
		if err != nil {
			t.Fatal(err)
		}
		if pcm := decodeStream(t, out, FormatOgg); !slices.Equal(pcm, got) {
			t.Fatalf("%s: a whole cut decodes to %d samples, want the %d of the source", tc.name, len(pcm), len(got))
		}
	}
}

// silencePage returns an Ogg Opus file with the given pre-skip whose only
// audio page holds three copies of packet and ends the stream at granule.
func silencePage(t *testing.T, packet []byte, preSkip int, granule int64) []byte {
	t.Helper()
	head := opus.Head{Version: 1, Channels: 1, PreSkip: preSkip, InputSampleRate: SampleRate}.Marshal()
	return slices.Concat(
		makeOggPage(t, 5, 0, 0x02, 0, false, head),
		makeOggPage(t, 5, 1, 0, 0, false, opus.MarshalTags("test")),
		makeOggPage(t, 5, 2, 0x04, granule, false, packet, packet, packet),
	)
}

// offsetLink returns the pages of an Ogg Opus link with the given pre-skip
// and output gain whose granule positions start at offset, RFC 7845 §4.5's
// initial offset: perPage packets to a page, the last page trimming trim
// samples.
func offsetLink(t *testing.T, serial uint32, preSkip int, gain int16, offset int64, packets [][]byte, perPage, trim int) [][]byte {
	t.Helper()
	head := opus.Head{Version: 1, Channels: 1, PreSkip: preSkip, InputSampleRate: SampleRate, OutputGain: gain}.Marshal()
	pages := [][]byte{
		makeOggPage(t, serial, 0, 0x02, 0, false, head),
		makeOggPage(t, serial, 1, 0, 0, false, opus.MarshalTags("test")),
	}
	granule := offset
	for i := 0; i < len(packets); i += perPage {
		page := packets[i:min(i+perPage, len(packets))]
		for _, p := range page {
			parsed, err := opus.Parse(p, false)
			if err != nil {
				t.Fatal(err)
			}
			granule += int64(parsed.Duration48())
		}
		flags := byte(0)
		if i+perPage >= len(packets) {
			flags, granule = 0x04, granule-int64(trim)
		}
		pages = append(pages, makeOggPage(t, serial, uint32(len(pages)), flags, granule, false, page...))
	}
	return pages
}

// A chained link may start its granule positions at an offset, as the first
// may (RFC 7845 §4.5). Its header pages, at granule 0, used to set the
// baseline its first audio page was measured against, so the offset read as
// lost audio and was concealed: two 40 ms links, the second offset by 9 600
// samples, decoded to 13 440 samples. Now a link's audio pages alone place
// it, and the chain decodes as its links do alone, also with a pre-skip and
// gain on the second link, its OpusTags page lost, the first link's EOS page
// lost, or both. ffmpeg decodes the 40 ms links to 3 840 samples too.
func TestChainedLinkGranuleOffsetIsNotAGap(t *testing.T) {
	silence, err := opus.Silence(1, opus.BandwidthFullband, 20*ms)
	if err != nil {
		t.Fatal(err)
	}
	short := slices.Concat(offsetLink(t, 101, 0, 0, 0, [][]byte{silence[0], silence[0]}, 1, 0)...)
	shortOffset := slices.Concat(offsetLink(t, 202, 0, 0, 9600, [][]byte{silence[0], silence[0]}, 1, 0)...)
	if got, _ := decodeAll(t, bytes.NewReader(slices.Concat(short, shortOffset)), DecodeOptions{}); len(got) != 3840 {
		t.Fatalf("two 40 ms links: %d samples, want 3840", len(got))
	}

	audio := fixturePackets(t, "ogg-celt-fb.opus")
	a := offsetLink(t, 1, 312, 0, 0, audio[:30], 10, 500)
	b := offsetLink(t, 2, 312, 3*256, 9600, audio[30:60], 10, 300)
	ended := slices.Clone(a[:len(a)-1]) // a, had it ended on the page before its last
	before := bytes.Clone(ended[len(ended)-1])
	before[5] |= 0x04
	fixOggCRC(before, oggPage{start: 0, size: len(before)})
	ended[len(ended)-1] = before
	alone := func(pages [][]byte) []float32 {
		pcm, _ := decodeAll(t, bytes.NewReader(slices.Concat(pages...)), DecodeOptions{})
		return pcm
	}
	corruptLast := func(pages [][]byte) [][]byte {
		out := slices.Clone(pages)
		last := bytes.Clone(out[len(out)-1])
		last[len(last)-1] ^= 0x01
		out[len(out)-1] = last
		return out
	}
	withoutTags := slices.Concat(b[:1], b[2:])
	for _, tc := range []struct {
		name  string
		first [][]byte
		next  [][]byte
		want  []float32
	}{
		{"offset", a, b, slices.Concat(alone(a), alone(b))},
		{"offset, OpusTags lost", a, withoutTags, slices.Concat(alone(a), alone(b))},
		{"offset, EOS lost", corruptLast(a), b, slices.Concat(alone(ended), alone(b))},
		{"offset, OpusTags and EOS lost", corruptLast(a), withoutTags, slices.Concat(alone(ended), alone(b))},
	} {
		data := slices.Concat(slices.Concat(tc.first...), slices.Concat(tc.next...))
		assertSamePCM(t, tc.name, data, tc.want)
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(src.Packets); i++ {
			if src.Packets[i].Start != src.Packets[i-1].End() {
				t.Fatalf("%s: a hole of %d samples before packet %d", tc.name, src.Packets[i].Start-src.Packets[i-1].End(), i)
			}
		}
		if durationToSamples(src.Duration()) != int64(len(tc.want)) {
			t.Fatalf("%s: the source covers %v for %d samples", tc.name, src.Duration(), len(tc.want))
		}
	}
}
