package ogg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"
)

func testPackets(n int) [][]byte {
	packets := make([][]byte, n)
	for i := range packets {
		packets[i] = []byte(fmt.Sprintf("packet-%02d-%s", i, bytes.Repeat([]byte{byte('a' + i)}, 30)))
	}
	return packets
}

// singlePacketStream returns one page holding data as the only packet of a
// stream, flagged BOS and EOS.
func singlePacketStream(tb testing.TB, serial uint32, data []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, serial)
	if err := w.WritePacket(data, 960, true); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

// multiPageStream writes packets with perPage packets per page (the last page
// may hold fewer) and the EOS flag on the final page.
func multiPageStream(t *testing.T, serial uint32, packets [][]byte, perPage int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, serial)
	for i, p := range packets {
		if err := w.WritePacket(p, int64(i+1)*960, i == 0); err != nil {
			t.Fatal(err)
		}
		if (i+1)%perPage == 0 && i+1 < len(packets) {
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pageStarts returns the offset of every page in data.
func pageStarts(t *testing.T, data []byte) []int {
	t.Helper()
	var starts []int
	for off := 0; off < len(data); {
		if off+headerSize > len(data) || string(data[off:off+4]) != "OggS" {
			t.Fatalf("no page header at offset %d", off)
		}
		nseg := int(data[off+26])
		size := headerSize + nseg
		for _, l := range data[off+headerSize : off+headerSize+nseg] {
			size += int(l)
		}
		starts = append(starts, off)
		off += size
	}
	return starts
}

// fixCRC recomputes the checksum of the page starting at off.
func fixCRC(data []byte, off int) {
	nseg := int(data[off+26])
	size := headerSize + nseg
	for _, l := range data[off+headerSize : off+headerSize+nseg] {
		size += int(l)
	}
	page := data[off : off+size]
	binary.LittleEndian.PutUint32(page[22:], 0)
	binary.LittleEndian.PutUint32(page[22:], crcUpdate(0, page))
}

func readAllPackets(r *Reader) ([]Packet, error) {
	var got []Packet
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			return got, nil
		}
		if err != nil {
			return got, err
		}
		got = append(got, p)
	}
}

func expectPackets(t *testing.T, name string, got []Packet, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d packets, want %d", name, len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i].Data, want[i]) {
			t.Fatalf("%s: packet %d is %q, want %q", name, i, got[i].Data, want[i])
		}
	}
}

// roundTrip writes packets, flushing a page after every flushEvery packets
// (never when zero), and reads them back.
func roundTrip(t *testing.T, packets [][]byte, flushEvery int) []Packet {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, 0x1234)
	for i, p := range packets {
		if err := w.WritePacket(p, int64(i+1)*960, i == 0); err != nil {
			t.Fatal(err)
		}
		if flushEvery > 0 && (i+1)%flushEvery == 0 {
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := readAllPackets(NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRoundTripPreservesPacketsAndFlags(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var packets [][]byte
	for i := 0; i < 40; i++ {
		size := rng.Intn(700)
		if i == 7 {
			size = 255 // exact multiple of the lacing size needs a terminating zero segment
		}
		if i == 9 {
			size = 70000 // spans more than one page
		}
		p := make([]byte, size)
		rng.Read(p)
		packets = append(packets, p)
	}
	for _, flushEvery := range []int{0, 1, 3} {
		got := roundTrip(t, packets, flushEvery)
		if len(got) != len(packets) {
			t.Fatalf("flushEvery=%d: got %d packets, want %d", flushEvery, len(got), len(packets))
		}
		for i := range packets {
			if !bytes.Equal(got[i].Data, packets[i]) {
				t.Fatalf("flushEvery=%d: packet %d differs", flushEvery, i)
			}
			if got[i].BOS != (i == 0) {
				t.Fatalf("flushEvery=%d: packet %d BOS=%v", flushEvery, i, got[i].BOS)
			}
			// Flushing after every packet leaves Close with an empty EOS page,
			// so no packet carries the EOS flag in that configuration.
			if flushEvery != 1 && got[i].EOS != (i == len(packets)-1) {
				t.Fatalf("flushEvery=%d: packet %d EOS=%v", flushEvery, i, got[i].EOS)
			}
		}
		last := got[len(got)-1]
		if !last.GranuleKnown || last.Granule != int64(len(packets))*960 {
			t.Fatalf("flushEvery=%d: final granule %d known=%v", flushEvery, last.Granule, last.GranuleKnown)
		}
		if flushEvery == 1 {
			for i := range got {
				if !got[i].GranuleKnown || got[i].Granule != int64(i+1)*960 {
					t.Fatalf("packet %d granule %d known=%v", i, got[i].Granule, got[i].GranuleKnown)
				}
			}
		}
	}
}

func TestBadCRCIsDroppedAndCounted(t *testing.T) {
	page := singlePacketStream(t, 7, []byte("hello"))
	page[len(page)-1] ^= 0xFF
	r := NewReader(bytes.NewReader(page))
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
	if r.DroppedPages() != 1 {
		t.Fatalf("DroppedPages() = %d, want 1", r.DroppedPages())
	}
}

func TestTruncatedPageIsUnexpectedEOF(t *testing.T) {
	page := singlePacketStream(t, 7, bytes.Repeat([]byte{1}, 300))
	for _, cut := range []int{10, 27, 30, len(page) - 1} {
		_, err := NewReader(bytes.NewReader(page[:cut])).Next()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("cut=%d: want ErrUnexpectedEOF, got %v", cut, err)
		}
	}
	if _, err := NewReader(bytes.NewReader(nil)).Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("empty input: got %v", err)
	}
}

func TestSkipsOtherSerialsAndGarbage(t *testing.T) {
	var first bytes.Buffer
	firstWriter := NewWriter(&first, 7)
	if err := firstWriter.WritePacket([]byte("first"), 960, true); err != nil {
		t.Fatal(err)
	}
	if err := firstWriter.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := firstWriter.Close(); err != nil {
		t.Fatal(err)
	}
	other := singlePacketStream(t, 99, []byte("other"))
	firstPages := first.Bytes()
	firstPageEnd := headerSize + 1 + len("first")
	input := append([]byte("junk"), firstPages[:firstPageEnd]...)
	// Interleave the other stream's page before the first stream's EOS page.
	input = append(input, other...)
	input = append(input, firstPages[firstPageEnd:]...)
	input = append(input, []byte("trailing")...)
	r := NewReader(bytes.NewReader(input))
	p, err := r.Next()
	if err != nil || string(p.Data) != "first" {
		t.Fatalf("got %q, %v", p.Data, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF after EOS, got %v", err)
	}
}

// TestReusePacketData reads a stream of small, page-spanning and corrupt
// pages with ReusePacketData and requires the packets of a default Reader,
// each valid until the next call to Next, and no allocations per packet.
func TestReusePacketData(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var packets [][]byte
	for i := 0; i < 400; i++ {
		size := rng.Intn(600)
		if i%97 == 5 {
			size = 70000 // spans two pages
		}
		p := make([]byte, size)
		rng.Read(p)
		packets = append(packets, p)
	}
	data := multiPageStream(t, 7, packets, 9)
	// Corrupt one page in the middle: both readers drop it.
	starts := pageStarts(t, data)
	data[starts[len(starts)/2]+headerSize+3] ^= 0xff

	want, err := readAllPackets(NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	r := NewReader(bytes.NewReader(data))
	r.ReusePacketData()
	for i := range want {
		p, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(p.Data, want[i].Data) || p.Granule != want[i].Granule || p.GranuleKnown != want[i].GranuleKnown ||
			p.BOS != want[i].BOS || p.EOS != want[i].EOS {
			t.Fatalf("packet %d differs", i)
		}
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last packet: %v", err)
	}

	// Steady state: pages of seven short packets.
	r = NewReader(bytes.NewReader(multiPageStream(t, 9, testPackets(50), 7)))
	r.ReusePacketData()
	if _, err := r.Next(); err != nil { // the first page grows the buffers
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(40, func() {
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per packet", allocs)
	}
}
