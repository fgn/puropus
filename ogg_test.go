package puropus

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
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
// it: the fragments on either side are never joined. The audio before it is
// untouched and decoding goes on after it. ffmpeg produces as many samples
// when the page completes the packet. For the page inside it, ffmpeg joins
// the fragments it has, fails to decode the result and puts a packet's
// duration of silence in its place.
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
		{"inside the packet", middle, len(want) - 960, 50},
		// Packets 10 to 49 are lost. The last packet keeps its encoder
		// padding, as with ffmpeg: end trimming is anchored to the granule
		// position of the page before it, now the first audio page's.
		{"completing the packet", completing, intact + 960, 11},
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

// Cut sees the file with a lost EOS page on one timeline too. The first
// link's last packet has nothing left to trim, so only the second link's
// pre-skip becomes a synthetic stretch between them, and the first link's
// audio is copied exactly.
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
	skip := samplesToDuration(312)
	assertSpans(t, out, []Span{
		{Out: 0, In: 0, Length: samplesToDuration(linkA)},
		{Out: samplesToDuration(linkA), Length: skip, Synthetic: true},
		{Out: samplesToDuration(linkA) + skip, In: samplesToDuration(linkA), Length: 3000 * ms},
	})
	pcm := decodeStream(t, out, FormatOgg)
	if int64(len(pcm)) != durationToSamples(out.Duration()) || maxAbsDiff(pcm[:linkA], direct) != 0 {
		t.Fatalf("cut decodes to %d samples for %v, first link differs by %g", len(pcm), out.Duration(), maxAbsDiff(pcm[:linkA], direct))
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
		if !bytes.Equal(g.Data, w.Data) || g.Start != w.Start || g.Duration != w.Duration || g.Skip != w.Skip || g.Trim != w.Trim {
			t.Fatalf("%s: packet %d is %+v, want %+v", name, i, g, w)
		}
	}
}
