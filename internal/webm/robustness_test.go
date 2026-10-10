package webm

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"
)

// The tests in this file cover malformed and unusual input. Several guard
// size arithmetic that differs on 32-bit platforms, so they also run with
// GOARCH=386.

// Blocks of other tracks may be arbitrarily large (a 4K video keyframe easily
// exceeds 1 MiB). They must be skipped, not read into memory and rejected.
// The size cap stays for the Opus track.
func TestForeignTrackBlocksAboveTheSizeLimitAreSkipped(t *testing.T) {
	big := make([]byte, 3*maxPayloadSize/2) // 1.5 MiB "video keyframe"
	for i := range big {
		big[i] = byte(i)
	}
	file := withVideoTrack(header(WriterOptions{Channels: 1, App: "test"}, testOpusHead))
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = append(cluster, simpleBlock(2, big)...)
	group := appendElement(nil, idBlock, append([]byte{0x82, 0x00, 0x10, 0x00}, big...))
	cluster = appendElement(cluster, idBlockGroup, group)
	cluster = append(cluster, simpleBlock(1, []byte{0xAA, 0xBB})...)
	file = appendElement(file, idCluster, cluster)

	r, blocks, err := readAll(t, file)
	if err != nil {
		t.Fatalf("large video blocks broke the audio decode: %v", err)
	}
	if r.Track().Number != 1 || len(blocks) != 1 || !bytes.Equal(blocks[0].Data, []byte{0xAA, 0xBB}) {
		t.Fatalf("track %d, blocks %+v; want the one audio block", r.Track().Number, blocks)
	}
}

func TestOversizedOpusBlockIsAnErrorNotAPanic(t *testing.T) {
	big := make([]byte, 3*maxPayloadSize/2)
	file := header(WriterOptions{Channels: 1, App: "test"}, testOpusHead)
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = append(cluster, simpleBlock(1, big)...)
	file = appendElement(file, idCluster, cluster)
	if _, _, err := readAll(t, file); err == nil {
		t.Fatal("a 1.5 MiB Opus block was accepted")
	}
}

// A child whose declared size cannot fit in its parent is rejected on every
// architecture. With a 32-bit int, int(size) of these sizes is negative or
// wraps, so the bounds checks compare sizes as int64.
func TestHugeChildSizesAreRejectedWithoutPanic(t *testing.T) {
	huge := []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE} // 2^56-2, not the unknown-size marker

	// Info with a child claiming 2^56-2 bytes but holding 2.
	info := appendID(nil, idMuxingApp)
	info = append(info, huge...)
	info = append(info, 'h', 'i')
	file := appendUnknownSizeHeader(minimalEBMLHeader(), idSegment)
	file = appendElement(file, idInfo, info)
	if _, err := NewReader(bytes.NewReader(file)); err == nil {
		t.Fatal("Info child with size 2^56-2 accepted")
	}

	// The same inside a BlockGroup.
	group := appendID(nil, idBlock)
	group = append(group, huge...)
	group = append(group, 0x81, 0x00, 0x00, 0x80, 0xAA)
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = appendElement(cluster, idBlockGroup, group)
	file = appendElement(header(WriterOptions{Channels: 1, App: "test"}, testOpusHead), idCluster, cluster)
	if _, _, err := readAll(t, file); err == nil {
		t.Fatal("BlockGroup child with size 2^56-2 accepted")
	}

	// A 32-bit-wrapping size (2^32+8) in the Info.
	wrap := []byte{0x01, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x08}
	info = appendID(nil, idMuxingApp)
	info = append(info, wrap...)
	info = append(info, 'h', 'i')
	file = appendUnknownSizeHeader(minimalEBMLHeader(), idSegment)
	file = appendElement(file, idInfo, info)
	if _, err := NewReader(bytes.NewReader(file)); err == nil {
		t.Fatal("Info child with size 2^32+8 accepted")
	}
}

// Bytes after the end of a known-size Segment, and zero padding after the
// last Cluster of an unknown-size Segment, end the stream cleanly. Everything
// before them has been delivered, so this is io.EOF, not an error.
func TestTrailingBytesAfterTheLastClusterAreEndOfStream(t *testing.T) {
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = append(cluster, simpleBlock(1, []byte{0xAA})...)

	// Known-size Segment followed by junk.
	body := append(audioTrackHeaders("A_OPUS"), appendElement(nil, idCluster, cluster)...)
	file := appendElement(minimalEBMLHeader(), idSegment, body)
	file = append(file, []byte("garbage after the segment")...)
	_, blocks, err := readAll(t, file)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("known-size Segment + trailing junk: blocks %d, err %v (want 1 block, io.EOF)", len(blocks), err)
	}

	// Unknown-size Segment, unknown-size last Cluster, then zero padding.
	file = appendUnknownSizeHeader(minimalStream("A_OPUS"), idCluster)
	file = append(file, cluster...)
	file = append(file, make([]byte, 16)...)
	_, blocks, err = readAll(t, file)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("zero padding after the last cluster: blocks %d, err %v (want 1 block, io.EOF)", len(blocks), err)
	}

	// Truncation inside a block is still reported.
	file = minimalStream("A_OPUS", cluster)
	if _, _, err := readAll(t, file[:len(file)-1]); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated block: want ErrUnexpectedEOF, got %v", err)
	}
}

// A known-size Segment declares how many bytes it holds, so input that ends
// before them is truncated even when it ends between elements: after a
// complete known-size Cluster, or inside an unknown-size one. A live
// recording, whose Segment and Clusters have unknown sizes, may end after any
// element, and that is the end of the stream.
func TestEndInsideAKnownSizeSegmentIsTruncation(t *testing.T) {
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = append(cluster, simpleBlock(1, []byte{0xAA})...)
	headers := audioTrackHeaders("A_OPUS")
	segment := func(body []byte, missing int) []byte {
		file := minimalEBMLHeader()
		file = appendID(file, idSegment)
		file = appendSize(file, uint64(len(body)+missing))
		return append(file, body...)
	}
	complete := slices.Concat(headers, appendElement(nil, idCluster, cluster))
	unknownCluster := slices.Concat(headers, appendUnknownSizeHeader(nil, idCluster), cluster)
	for _, tc := range []struct {
		name    string
		file    []byte
		wantErr error
	}{
		{"complete", segment(complete, 0), nil},
		{"after a known-size Cluster", segment(complete, 10), io.ErrUnexpectedEOF},
		{"inside an unknown-size Cluster", segment(unknownCluster, 10), io.ErrUnexpectedEOF},
		{"live recording", slices.Concat(minimalStream("A_OPUS"), appendUnknownSizeHeader(nil, idCluster), cluster), nil},
	} {
		_, blocks, err := readAll(t, tc.file)
		if len(blocks) != 1 || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
			t.Fatalf("%s: %d blocks, error %v, want 1 block and %v", tc.name, len(blocks), err, tc.wantErr)
		}
	}
}

// Bytes that are not an element end a live recording only outside any
// known-size Cluster that still declares bytes. Inside one, a zero byte
// followed by the end of the input is a Cluster cut off, io.ErrUnexpectedEOF
// (it used to be taken for trailing padding and a clean end), and garbage
// filling the declared bytes is an invalid element; zero padding after a
// finished Cluster still ends the stream.
func TestInvalidBytesInsideAKnownSizeCluster(t *testing.T) {
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = append(cluster, simpleBlock(1, []byte{0xAA})...)
	declaring := func(extra int, tail []byte) []byte {
		file := minimalStream("A_OPUS")
		file = appendID(file, idCluster)
		file = appendSize(file, uint64(len(cluster)+extra))
		return slices.Concat(file, cluster, tail)
	}
	for _, tc := range []struct {
		name    string
		file    []byte
		wantErr error
	}{
		{"a zero byte, then the end", declaring(10, []byte{0}), io.ErrUnexpectedEOF},
		{"ten zero bytes", declaring(10, make([]byte, 10)), errInvalidElementHeader},
		{"padding after the Cluster", declaring(0, make([]byte, 10)), nil},
	} {
		_, blocks, err := readAll(t, tc.file)
		if len(blocks) != 1 || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
			t.Fatalf("%s: %d blocks, error %v, want 1 block and %v", tc.name, len(blocks), err, tc.wantErr)
		}
	}
}
