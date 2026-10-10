package ogg

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

// The tests in this file cover damaged and unusual input. The reader
// recovers from them the way the ffmpeg command does, except where a test
// says otherwise.

// A page whose checksum or version byte is wrong is dropped and reading
// continues with the next page, as with ffmpeg, which goes on to decode the
// pages after it. A corrupted segment count must not let the
// reader swallow the following pages: after a mismatch the reader rescans the
// bytes it already consumed for the next capture pattern.
func TestCorruptPageIsDroppedAndReadingContinues(t *testing.T) {
	packets := testPackets(10)
	clean := multiPageStream(t, 7, packets, 3) // pages: [0 1 2] [3 4 5] [6 7 8] [9]
	starts := pageStarts(t, clean)
	if len(starts) != 4 {
		t.Fatalf("%d pages, want 4", len(starts))
	}
	want := append(append([][]byte{}, packets[:3]...), packets[6:]...) // page 1 lost
	corruptions := map[string]func([]byte){
		"payload byte":  func(d []byte) { d[starts[1]+headerSize+3+5] ^= 0x55 },
		"version byte":  func(d []byte) { d[starts[1]+4] = 1 },
		"granule field": func(d []byte) { d[starts[1]+9] ^= 0xFF },
		"segment count": func(d []byte) { d[starts[1]+26] ^= 0x40 },
		"lacing value":  func(d []byte) { d[starts[1]+headerSize] ^= 0x80 },
	}
	for name, corrupt := range corruptions {
		data := append([]byte(nil), clean...)
		corrupt(data)
		r := NewReader(bytes.NewReader(data))
		got, err := readAllPackets(r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		expectPackets(t, name, got, want)
		if r.DroppedPages() != 1 {
			t.Fatalf("%s: DroppedPages() = %d, want 1", name, r.DroppedPages())
		}
	}

	// A corrupt page that runs into the end of the input still yields the
	// pages before it and ends with io.EOF (there is no more data to wait for).
	data := append([]byte(nil), clean...)
	data[starts[3]+26] = 200 // last page claims 200 segments
	r := NewReader(bytes.NewReader(data))
	got, err := readAllPackets(r)
	if err != nil {
		t.Fatalf("corrupt last page: %v", err)
	}
	expectPackets(t, "corrupt last page", got, packets[:9])
}

// Dropping a page costs time proportional to its size, so a recording whose
// pages all carry a bad checksum (some muxers write CRC 0) is still read in
// linear time: 20 000 corrupt pages followed by a good stream must yield that
// stream's packets in well under a second.
func TestManyCorruptPagesReadInLinearTime(t *testing.T) {
	const corrupt = 20000
	packets := testPackets(3)
	page := multiPageStream(t, 7, packets[:1], 1) // one page, one packet
	starts := pageStarts(t, page)
	if len(starts) != 1 {
		t.Fatalf("%d pages, want 1", len(starts))
	}
	bad := append([]byte(nil), page...)
	bad[headerSize+1+5] ^= 0x55 // payload byte: CRC no longer matches
	var input bytes.Buffer
	for i := 0; i < corrupt; i++ {
		input.Write(bad)
	}
	input.Write(multiPageStream(t, 9, packets[1:], 2)) // good stream after the junk

	start := time.Now()
	r := NewReader(&input)
	got, err := readAllPackets(r)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	expectPackets(t, "after corrupt pages", got, packets[1:])
	if r.DroppedPages() != corrupt {
		t.Fatalf("DroppedPages() = %d, want %d", r.DroppedPages(), corrupt)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("reading %d corrupt pages took %v, want time linear in the input size", corrupt, elapsed)
	}
}

// A chained bitstream (a new logical stream after an EOS page) is read in
// sequence; the second link's first packet carries BOS and its serial.
func TestChainedStreamsAreReadInSequence(t *testing.T) {
	a := testPackets(4)
	b := testPackets(3)
	data := append(multiPageStream(t, 1, a, 2), multiPageStream(t, 2, b, 2)...)
	r := NewReader(bytes.NewReader(data))
	got, err := readAllPackets(r)
	if err != nil {
		t.Fatal(err)
	}
	expectPackets(t, "chain", got, append(append([][]byte{}, a...), b...))
	if !got[0].BOS || got[0].Serial != 1 || !got[3].EOS || got[3].Serial != 1 {
		t.Fatalf("first link flags wrong: %+v %+v", got[0], got[3])
	}
	if !got[4].BOS || got[4].Serial != 2 || got[4].EOS || !got[6].EOS || got[6].Serial != 2 {
		t.Fatalf("second link flags wrong: %+v %+v", got[4], got[6])
	}
	if got[1].BOS || got[5].BOS {
		t.Fatal("BOS set on a non-first packet")
	}
}

// RFC 7845 §3: when a page arrives with the continued flag but no packet is
// being assembled, the first segment run on that page belongs to a packet
// whose start was never seen and must be discarded, not delivered.
func TestContinuedFlagWithoutPartialDiscardsFirstRun(t *testing.T) {
	packets := testPackets(6)
	data := multiPageStream(t, 7, packets, 2) // [0 1] [2 3] [4 5]
	starts := pageStarts(t, data)
	data[starts[1]+5] |= FlagContinued
	fixCRC(data, starts[1])
	got, err := readAllPackets(NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	expectPackets(t, "continued without partial", got, [][]byte{packets[0], packets[1], packets[3], packets[4], packets[5]})
}

// RFC 7845 §3: a packet spanning pages is invalid when a page in between is
// missing (page sequence gap); its fragments must not be concatenated into a
// packet. Subsequent packets are unaffected.
func TestPageSequenceGapDropsSpanningPacket(t *testing.T) {
	big := bytes.Repeat([]byte{0xAB}, 140000) // spans three pages (65025 + 65025 + rest)
	packets := [][]byte{[]byte("before"), big, []byte("after")}
	data := multiPageStream(t, 7, packets, 1)
	starts := pageStarts(t, data)
	if len(starts) != 5 {
		t.Fatalf("%d pages, want 5", len(starts))
	}
	// Drop the middle page of the big packet (page 2 of pages 1..3).
	cut := append(append([]byte(nil), data[:starts[2]]...), data[starts[3]:]...)
	r := NewReader(bytes.NewReader(cut))
	got, err := readAllPackets(r)
	if err != nil {
		t.Fatal(err)
	}
	expectPackets(t, "sequence gap", got, [][]byte{[]byte("before"), []byte("after")})
}

// A few stray bytes after the last page of a stream that has no EOS flag are
// not a truncated page. Everything was delivered, so this is io.EOF.
func TestTrailingJunkAfterAStreamWithoutEOSIsEndOfInput(t *testing.T) {
	packets := testPackets(4)
	data := multiPageStream(t, 7, packets, 2)
	starts := pageStarts(t, data)
	last := starts[len(starts)-1]
	data[last+5] &^= FlagEOS
	fixCRC(data, last)
	for _, tail := range [][]byte{[]byte("abc"), {0x00}, []byte("Og")} {
		input := append(append([]byte(nil), data...), tail...)
		got, err := readAllPackets(NewReader(bytes.NewReader(input)))
		if err != nil {
			t.Fatalf("tail %q: %v", tail, err)
		}
		expectPackets(t, fmt.Sprintf("tail %q", tail), got, packets)
	}
	// A real truncation inside a page is still reported.
	if _, err := readAllPackets(NewReader(bytes.NewReader(data[:len(data)-5]))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated page: want ErrUnexpectedEOF, got %v", err)
	}
}

// pageSpec describes one page for the tests to assemble byte by byte.
type pageSpec struct {
	serial, sequence uint32
	flags            byte
	granule          int64
	// pieces are laced in order, each as a complete packet or the end of
	// one, except that with open set the last piece is the start of a packet
	// continued on the next page, a multiple of 255 bytes long.
	pieces [][]byte
	open   bool
}

// bytes returns the page with a valid checksum.
func (s pageSpec) bytes(tb testing.TB) []byte {
	tb.Helper()
	var lacing, payload []byte
	for i, piece := range s.pieces {
		for n := len(piece); n >= 255; n -= 255 {
			lacing = append(lacing, 255)
		}
		if s.open && i == len(s.pieces)-1 {
			if len(piece)%255 != 0 {
				tb.Fatalf("open piece of %d bytes", len(piece))
			}
		} else {
			lacing = append(lacing, byte(len(piece)%255))
		}
		payload = append(payload, piece...)
	}
	if len(lacing) > maxSegments {
		tb.Fatalf("%d segments", len(lacing))
	}
	page := make([]byte, headerSize, headerSize+len(lacing)+len(payload))
	copy(page, "OggS")
	page[5] = s.flags
	binary.LittleEndian.PutUint64(page[6:], uint64(s.granule))
	binary.LittleEndian.PutUint32(page[14:], s.serial)
	binary.LittleEndian.PutUint32(page[18:], s.sequence)
	page[26] = byte(len(lacing))
	page = append(append(page, lacing...), payload...)
	binary.LittleEndian.PutUint32(page[22:], crcUpdate(0, page))
	return page
}

// filler returns n bytes of value b.
func filler(n int, b byte) []byte { return bytes.Repeat([]byte{b}, n) }

// spanningPacket returns a packet of n bytes that is not a run of one value,
// so a fragment out of place would show.
func spanningPacket(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i * 7)
	}
	return p
}

// In a grouped bitstream a packet of the selected stream can span pages with
// a page of another stream between them. When that page is corrupt it is
// dropped, but the packet is intact and is delivered whole, whatever the
// damage: nothing in a dropped page can be trusted, not even a serial number
// that reads as the selected stream's, so only the selected stream's own
// sequence numbers tell whether it lost a page. FFmpeg delivers the packet
// too, except when a corrupt size runs past the end of the input: FFmpeg then
// loses the pages it swallowed.
func TestCorruptPageOfAnotherStreamKeepsSpanningPacket(t *testing.T) {
	head, a0, a1 := []byte("head-a"), []byte("packet-a0"), []byte("packet-a1")
	big := spanningPacket(700) // 510 bytes on one page, 190 on the next
	pages := [][]byte{
		pageSpec{serial: 1, flags: FlagBOS, pieces: [][]byte{head}}.bytes(t),
		pageSpec{serial: 2, flags: FlagBOS, pieces: [][]byte{[]byte("head-b")}}.bytes(t),
		pageSpec{serial: 1, sequence: 1, granule: 960, pieces: [][]byte{a0, big[:510]}, open: true}.bytes(t),
		pageSpec{serial: 2, sequence: 1, granule: 960, pieces: [][]byte{filler(40, 0xff)}}.bytes(t),
		pageSpec{serial: 1, sequence: 2, flags: FlagContinued | FlagEOS, granule: 2880, pieces: [][]byte{big[510:], a1}}.bytes(t),
		pageSpec{serial: 2, sequence: 2, flags: FlagEOS, granule: 2880, pieces: [][]byte{filler(300, 0x02)}}.bytes(t),
	}
	const other = 3
	want := [][]byte{head, a0, big, a1}
	for name, corrupt := range map[string]func([]byte){
		"payload byte":        func(p []byte) { p[headerSize+1+5] ^= 0x55 },
		"version byte":        func(p []byte) { p[4] = 1 },
		"serial of the other": func(p []byte) { binary.LittleEndian.PutUint32(p[14:], 1) },
		"sequence number":     func(p []byte) { p[18] ^= 0x01 },
		"flags":               func(p []byte) { p[5] = FlagBOS | FlagContinued },
		// The first payload byte becomes a lacing value: the page claims
		// 255 bytes more and ends inside the last page.
		"segment count": func(p []byte) { p[26] = 2 },
		// The page claims more bytes than the input holds.
		"segment count past the end": func(p []byte) { p[26] = 255 },
		"lacing value":               func(p []byte) { p[headerSize] = 200 },
	} {
		page := append([]byte(nil), pages[other]...)
		corrupt(page)
		input := bytes.Join([][]byte{bytes.Join(pages[:other], nil), page, bytes.Join(pages[other+1:], nil)}, nil)
		r := NewReader(bytes.NewReader(input))
		got, err := readAllPackets(r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		expectPackets(t, name, got, want)
		if r.DroppedPages() < 1 {
			t.Fatalf("%s: no page dropped", name)
		}
	}

	// When the input ends inside the other stream's page, the packet's
	// second half never arrives: it is not delivered, and the truncation is
	// reported.
	input := bytes.Join(pages[:other], nil)
	input = append(input, pages[other][:headerSize+10]...)
	got, err := readAllPackets(NewReader(bytes.NewReader(input)))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated: want io.ErrUnexpectedEOF, got %v", err)
	}
	expectPackets(t, "truncated", got, [][]byte{head, a0})
}

// A corrupt page of the selected stream still costs the packets it carries
// and the packet spanning it: its sequence number is missing when the
// stream's next page arrives. Whether the page was in the middle of the
// packet or completed it, the fragments around it are never joined, and the
// packets after it are delivered.
func TestCorruptPageOfTheSelectedStreamDropsSpanningPacket(t *testing.T) {
	head, a0, a1, a2 := []byte("head-a"), []byte("packet-a0"), []byte("packet-a1"), []byte("packet-a2")
	big := spanningPacket(900)  // pages 2, 3 and 5: 510, 255 and 135 bytes
	next := spanningPacket(400) // pages 5 and 6: 255 and 145 bytes
	pages := [][]byte{
		pageSpec{serial: 1, flags: FlagBOS, pieces: [][]byte{head}}.bytes(t),
		pageSpec{serial: 2, flags: FlagBOS, pieces: [][]byte{[]byte("head-b")}}.bytes(t),
		pageSpec{serial: 1, sequence: 1, granule: 960, pieces: [][]byte{a0, big[:510]}, open: true}.bytes(t),
		pageSpec{serial: 1, sequence: 2, flags: FlagContinued, granule: -1, pieces: [][]byte{big[510:765]}, open: true}.bytes(t),
		pageSpec{serial: 2, sequence: 1, granule: 960, pieces: [][]byte{filler(40, 0xff)}}.bytes(t),
		pageSpec{serial: 1, sequence: 3, flags: FlagContinued, granule: 2880, pieces: [][]byte{big[765:], a1, next[:255]}, open: true}.bytes(t),
		pageSpec{serial: 1, sequence: 4, flags: FlagContinued | FlagEOS, granule: 4800, pieces: [][]byte{next[255:], a2}}.bytes(t),
	}
	for _, tc := range []struct {
		name    string
		page    int
		corrupt func([]byte)
		want    [][]byte
	}{
		{"inside the packet", 3, func(p []byte) { p[headerSize+1+9] ^= 0x01 }, [][]byte{head, a0, a1, next, a2}},
		{"completing the packet", 5, func(p []byte) { p[len(p)-1] ^= 0x01 }, [][]byte{head, a0, a2}},
		{"completing it, serial unreadable", 5, func(p []byte) { p[15] ^= 0x10 }, [][]byte{head, a0, a2}},
		{"completing the next packet", 6, func(p []byte) { p[len(p)-1] ^= 0x01 }, [][]byte{head, a0, big, a1}},
	} {
		input := make([][]byte, len(pages))
		copy(input, pages)
		input[tc.page] = append([]byte(nil), pages[tc.page]...)
		tc.corrupt(input[tc.page])
		r := NewReader(bytes.NewReader(bytes.Join(input, nil)))
		got, err := readAllPackets(r)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		expectPackets(t, tc.name, got, tc.want)
		if r.DroppedPages() != 1 {
			t.Fatalf("%s: DroppedPages() = %d, want 1", tc.name, r.DroppedPages())
		}
	}
	got, err := readAllPackets(NewReader(bytes.NewReader(bytes.Join(pages, nil))))
	if err != nil {
		t.Fatal(err)
	}
	expectPackets(t, "intact", got, [][]byte{head, a0, big, a1, next, a2})
}

// RFC 3533 places all BOS pages of a link before its other pages. So when the
// EOS page of a chained link is lost (corrupt, missing, or cut short where
// another file was appended), a BOS page after the stream's data starts the
// next link: the reader ends the link there and selects from the new one, as
// it would after the EOS page, with or without a new serial number. FFmpeg
// decodes the next link too.
func TestLostEOSPageEndsLinkAtNextBOSPage(t *testing.T) {
	head1, p1, p2, p3 := []byte("head-1"), []byte("packet-1"), []byte("packet-2"), []byte("packet-3")
	head2, q1, q2 := []byte("head-2"), []byte("packet-q1"), []byte("packet-q2")
	big := spanningPacket(600) // left open by the lost EOS page
	link1 := func(serial uint32) [][]byte {
		return [][]byte{
			pageSpec{serial: serial, flags: FlagBOS, pieces: [][]byte{head1}}.bytes(t),
			pageSpec{serial: serial, sequence: 1, granule: 960, pieces: [][]byte{p1, p2, big[:510]}, open: true}.bytes(t),
			pageSpec{serial: serial, sequence: 2, flags: FlagContinued | FlagEOS, granule: 2880, pieces: [][]byte{big[510:], p3}}.bytes(t),
		}
	}
	link2 := func(serial uint32) []byte {
		return bytes.Join([][]byte{
			pageSpec{serial: serial, flags: FlagBOS, pieces: [][]byte{head2}}.bytes(t),
			pageSpec{serial: serial, sequence: 1, granule: 960, pieces: [][]byte{q1}}.bytes(t),
			pageSpec{serial: serial, sequence: 2, flags: FlagEOS, granule: 1920, pieces: [][]byte{q2}}.bytes(t),
		}, nil)
	}
	// The second link groups a stream the selector rejects, BOS page first.
	grouped := bytes.Join([][]byte{
		pageSpec{serial: 3, flags: FlagBOS, pieces: [][]byte{[]byte("other")}}.bytes(t),
		pageSpec{serial: 2, flags: FlagBOS, pieces: [][]byte{head2}}.bytes(t),
		pageSpec{serial: 3, sequence: 1, granule: 960, pieces: [][]byte{filler(30, 3)}}.bytes(t),
		pageSpec{serial: 2, sequence: 1, granule: 960, pieces: [][]byte{q1}}.bytes(t),
		pageSpec{serial: 3, sequence: 2, flags: FlagEOS, granule: 1920, pieces: [][]byte{filler(30, 4)}}.bytes(t),
		pageSpec{serial: 2, sequence: 2, flags: FlagEOS, granule: 1920, pieces: [][]byte{q2}}.bytes(t),
	}, nil)
	acceptHead := func(packet []byte) bool { return bytes.HasPrefix(packet, []byte("head")) }

	want := [][]byte{head1, p1, p2, head2, q1, q2}
	for _, tc := range []struct {
		name     string
		eos      func([]byte) []byte
		serial   uint32 // of the second link
		next     []byte // the second link, when not link2(serial)
		selector func([]byte) bool
	}{
		{name: "corrupt", eos: func(p []byte) []byte { p[len(p)-1] ^= 1; return p }, serial: 2},
		{name: "missing", eos: func([]byte) []byte { return nil }, serial: 2},
		{name: "cut short", eos: func(p []byte) []byte { return p[:headerSize+5] }, serial: 2},
		{name: "corrupt, same serial", eos: func(p []byte) []byte { p[len(p)-1] ^= 1; return p }, serial: 1},
		{name: "corrupt, grouped next link", eos: func(p []byte) []byte { p[len(p)-1] ^= 1; return p }, next: grouped, selector: acceptHead},
		{name: "corrupt, selector", eos: func(p []byte) []byte { p[len(p)-1] ^= 1; return p }, serial: 2, selector: acceptHead},
	} {
		first := link1(1)
		first[2] = tc.eos(first[2])
		next := tc.next
		if next == nil {
			next = link2(tc.serial)
		}
		r := NewReader(bytes.NewReader(append(bytes.Join(first, nil), next...)))
		r.SetBOSSelector(tc.selector)
		got, err := readAllPackets(r)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		expectPackets(t, tc.name, got, want)
		if !got[3].BOS || got[2].EOS {
			t.Fatalf("%s: second link's head BOS=%v, last packet of the first EOS=%v", tc.name, got[3].BOS, got[2].EOS)
		}
		if serial := cmp.Or(tc.serial, 2); got[3].Serial != serial || got[5].Serial != serial || !got[5].EOS {
			t.Fatalf("%s: second link read from serial %d to %d, want %d", tc.name, got[3].Serial, got[5].Serial, serial)
		}
	}
}

// A BOS page only ends a link once the selected stream has had other pages.
// Before that it opens another stream grouped into the same link, which is
// skipped. After that, against RFC 3533, it still ends the link, as it does
// with FFmpeg, which stops decoding the stream there too, even though the
// stream's pages go on.
func TestBOSPageEndsLinkOnlyAfterTheSelectedStreamsHeaders(t *testing.T) {
	head, p1, p2 := []byte("head"), []byte("packet-1"), []byte("packet-2")
	grouped := bytes.Join([][]byte{
		pageSpec{serial: 1, flags: FlagBOS, pieces: [][]byte{head}}.bytes(t),
		pageSpec{serial: 2, flags: FlagBOS, pieces: [][]byte{[]byte("other")}}.bytes(t),
		pageSpec{serial: 1, sequence: 1, granule: 960, pieces: [][]byte{p1}}.bytes(t),
		pageSpec{serial: 2, sequence: 1, flags: FlagEOS, granule: 960, pieces: [][]byte{filler(20, 2)}}.bytes(t),
		pageSpec{serial: 1, sequence: 2, flags: FlagEOS, granule: 1920, pieces: [][]byte{p2}}.bytes(t),
	}, nil)
	got, err := readAllPackets(NewReader(bytes.NewReader(grouped)))
	if err != nil {
		t.Fatal(err)
	}
	expectPackets(t, "grouped", got, [][]byte{head, p1, p2})

	late := bytes.Join([][]byte{
		pageSpec{serial: 1, flags: FlagBOS, pieces: [][]byte{head}}.bytes(t),
		pageSpec{serial: 1, sequence: 1, granule: 960, pieces: [][]byte{p1}}.bytes(t),
		pageSpec{serial: 2, flags: FlagBOS, pieces: [][]byte{[]byte("other")}}.bytes(t),
		pageSpec{serial: 1, sequence: 2, flags: FlagEOS, granule: 1920, pieces: [][]byte{p2}}.bytes(t),
	}, nil)
	r := NewReader(bytes.NewReader(late))
	r.SetBOSSelector(func(packet []byte) bool { return bytes.Equal(packet, head) })
	got, err = readAllPackets(r)
	if err != nil {
		t.Fatal(err)
	}
	expectPackets(t, "late BOS page", got, [][]byte{head, p1})
}
