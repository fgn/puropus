package puropus

import (
	"bytes"
	"errors"
	"testing"
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
