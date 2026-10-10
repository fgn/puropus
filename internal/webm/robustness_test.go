package webm

import (
	"bytes"
	"errors"
	"io"
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
