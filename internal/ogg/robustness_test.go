package ogg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

// The tests in this file cover damaged and unusual input. The reader
// recovers from them the way the ffmpeg command does.

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
