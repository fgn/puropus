package webm

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// testOpusHead is a mono OpusHead with 312 samples of pre-skip.
var testOpusHead = []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 1, 0x38, 0x01, 0x80, 0xBB, 0, 0, 0, 0, 0}

func readAll(t *testing.T, data []byte) (*Reader, []Block, error) {
	t.Helper()
	r, err := NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	var blocks []Block
	for {
		b, err := r.Next()
		if errors.Is(err, io.EOF) {
			return r, blocks, nil
		}
		if err != nil {
			return r, blocks, err
		}
		blocks = append(blocks, b)
	}
}

// minimalEBMLHeader returns an EBML header holding only the DocType.
func minimalEBMLHeader() []byte {
	return appendElement(nil, idEBML, appendString(nil, idDocType, "webm"))
}

// audioTrackHeaders returns an Info element and a Tracks element holding
// audio track 1 with the given CodecID.
func audioTrackHeaders(codecID string) []byte {
	file := appendElement(nil, idInfo, appendUint(nil, idTimecodeScale, 1000000))
	var entry []byte
	entry = appendUint(entry, idTrackNumber, 1)
	entry = appendUint(entry, idTrackType, trackTypeAudio)
	entry = appendString(entry, idCodecID, codecID)
	entry = appendElement(entry, idCodecPrivate, testOpusHead)
	entry = appendElement(entry, idAudio, appendUint(nil, idChannels, 1))
	return appendElement(file, idTracks, appendElement(nil, idTrackEntry, entry))
}

// minimalStream builds an EBML header, an unknown-size Segment, and the
// headers of audio track 1 with the given CodecID, followed by the given
// clusters.
func minimalStream(codecID string, clusters ...[]byte) []byte {
	file := appendUnknownSizeHeader(minimalEBMLHeader(), idSegment)
	file = append(file, audioTrackHeaders(codecID)...)
	for _, c := range clusters {
		file = appendElement(file, idCluster, c)
	}
	return file
}

// withVideoTrack appends a second Tracks element holding a video track 2, as
// a browser recording with video and audio has.
func withVideoTrack(file []byte) []byte {
	var video []byte
	video = appendUint(video, idTrackNumber, 2)
	video = appendUint(video, idTrackUID, 2)
	video = appendUint(video, idTrackType, 1)
	video = appendString(video, idCodecID, "V_VP9")
	return appendElement(file, idTracks, appendElement(nil, idTrackEntry, video))
}

// simpleBlock builds a SimpleBlock for track (1..126) at relative time 0.
func simpleBlock(track byte, payload []byte) []byte {
	return appendElement(nil, idSimpleBlock, append([]byte{0x80 | track, 0x00, 0x00, 0x80}, payload...))
}

// buildHandmade assembles a stream with an unknown-size cluster, a BlockGroup
// with DiscardPadding, a block on another track, and a second cluster.
func buildHandmade() []byte {
	file := withVideoTrack(header(WriterOptions{Channels: 1, App: "test"}, testOpusHead))
	file = appendElement(file, idVoid, make([]byte, 5))

	file = appendUnknownSizeHeader(file, idCluster)
	file = appendUint(file, idTimestamp, 1000)
	file = appendElement(file, idSimpleBlock, append([]byte{0x81, 0x00, 0x14, 0x80}, 0xAA, 0xBB))
	file = appendElement(file, idSimpleBlock, append([]byte{0x82, 0x00, 0x00, 0x80}, 0xCC)) // track 2
	group := appendElement(nil, idBlock, append([]byte{0x81, 0x00, 0x50, 0x00}, 0xDD))
	group = appendElement(group, idDiscardPadding, []byte{0x00, 0xF4, 0x24, 0x00}) // 16 000 000 ns
	file = appendElement(file, idBlockGroup, group)

	// A segment-level element ends the unknown-size cluster.
	file = appendElement(file, idCues, []byte{})

	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 2000)
	cluster = appendElement(cluster, idSimpleBlock, append([]byte{0x81, 0xFF, 0xF6, 0x80}, 0xEE)) // relative -10
	return appendElement(file, idCluster, cluster)
}

func TestWriterReaderRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, WriterOptions{Channels: 1, Duration: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// 1500 packets of 60 ms = 90 s: forces a cluster split at 30 s.
	for i := 0; i < 1500; i++ {
		if err := w.WriteBlock([]byte{byte(i), byte(i >> 8), 0xFE}, 2880, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, blocks, err := readAll(t, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1500 {
		t.Fatalf("got %d blocks", len(blocks))
	}
	if r.Track().Channels != 1 || r.Track().CodecID != "A_OPUS" || len(r.Track().CodecPrivate) != 19 {
		t.Fatalf("track %+v", r.Track())
	}
	if r.Info().Duration != 90000 {
		t.Fatalf("duration %v", r.Info().Duration)
	}
	for i, b := range blocks {
		if b.Data[0] != byte(i) || b.Data[1] != byte(i>>8) {
			t.Fatalf("block %d payload %x", i, b.Data)
		}
		want := time.Duration((int64(i)*2880*1000+24000)/48000) * time.Millisecond
		if b.Timestamp != want {
			t.Fatalf("block %d timestamp %v want %v", i, b.Timestamp, want)
		}
	}
	if bytes.Count(buf.Bytes(), appendID(nil, idCluster)) < 3 {
		t.Fatalf("expected at least three clusters for 90 s")
	}
}

func TestReaderHandlesUnknownSizeClustersAndBlockGroups(t *testing.T) {
	r, blocks, err := readAll(t, buildHandmade())
	if err != nil {
		t.Fatal(err)
	}
	if r.Track().Number != 1 {
		t.Fatalf("selected track %d", r.Track().Number)
	}
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks: %+v", len(blocks), blocks)
	}
	if !bytes.Equal(blocks[0].Data, []byte{0xAA, 0xBB}) || blocks[0].Timestamp != 1020*time.Millisecond {
		t.Fatalf("block 0: %+v", blocks[0])
	}
	if !bytes.Equal(blocks[1].Data, []byte{0xDD}) || blocks[1].Timestamp != 1080*time.Millisecond || blocks[1].DiscardPadding != 16*time.Millisecond {
		t.Fatalf("block 1: %+v", blocks[1])
	}
	if !bytes.Equal(blocks[2].Data, []byte{0xEE}) || blocks[2].Timestamp != 1990*time.Millisecond {
		t.Fatalf("block 2: %+v", blocks[2])
	}
}

func TestReaderRejectsLacingAndReportsTruncation(t *testing.T) {
	file := header(WriterOptions{Channels: 1, App: "test"}, testOpusHead)
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = appendElement(cluster, idSimpleBlock, append([]byte{0x81, 0x00, 0x00, 0x86}, 0x01, 0x00, 0x00))
	laced := appendElement(file, idCluster, cluster)
	if _, _, err := readAll(t, laced); !errors.Is(err, ErrLacing) {
		t.Fatalf("want ErrLacing, got %v", err)
	}

	good := buildHandmade()
	for _, cut := range []int{len(good) - 1, len(good) - 3, 60} {
		_, _, err := readAll(t, good[:cut])
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("cut=%d: want ErrUnexpectedEOF, got %v", cut, err)
		}
	}
	if _, err := NewReader(bytes.NewReader([]byte("RIFF"))); err == nil {
		t.Fatal("non-EBML input accepted")
	}
}

func TestFileWithoutOpusTrackIsRejected(t *testing.T) {
	var video []byte
	video = appendUint(video, idTrackNumber, 1)
	video = appendUint(video, idTrackType, 1)
	video = appendString(video, idCodecID, "V_VP8")
	stream := appendUnknownSizeHeader(minimalEBMLHeader(), idSegment)
	stream = appendElement(stream, idTracks, appendElement(nil, idTrackEntry, video))
	stream = appendElement(stream, idCluster, appendUint(nil, idTimestamp, 0))
	if _, err := NewReader(bytes.NewReader(stream)); !errors.Is(err, ErrNoOpusTrack) {
		t.Fatalf("want ErrNoOpusTrack, got %v", err)
	}
}

// ffmpeg decodes tracks with codec ID A_OPUS/EXPERIMENTAL, which early
// muxers wrote, as Opus; so does the reader.
func TestExperimentalOpusCodecIDIsAccepted(t *testing.T) {
	var cluster []byte
	cluster = appendUint(cluster, idTimestamp, 0)
	cluster = append(cluster, simpleBlock(1, []byte{0xAA})...)
	r, blocks, err := readAll(t, minimalStream("A_OPUS/EXPERIMENTAL", cluster))
	if err != nil {
		t.Fatalf("A_OPUS/EXPERIMENTAL rejected: %v", err)
	}
	if r.Track().Number != 1 || len(blocks) != 1 || !bytes.Equal(blocks[0].Data, []byte{0xAA}) {
		t.Fatalf("track %+v blocks %+v", r.Track(), blocks)
	}
}

// TestReuseBlockData reads the handmade stream and a written one with
// ReuseBlockData and requires the blocks of a default Reader, each valid
// until the next call to Next, and no allocations per block.
func TestReuseBlockData(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, WriterOptions{Channels: 1, Duration: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1500; i++ {
		block := bytes.Repeat([]byte{byte(i), byte(i >> 8)}, 1+i%200)
		if err := w.WriteBlock(block, 2880, 48*(i%3)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{buildHandmade(), buf.Bytes()} {
		_, want, err := readAll(t, data)
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r.ReuseBlockData()
		for i := range want {
			b, err := r.Next()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b.Data, want[i].Data) || b.Timestamp != want[i].Timestamp || b.DiscardPadding != want[i].DiscardPadding {
				t.Fatalf("block %d: %+v, want %+v", i, b, want[i])
			}
		}
		if _, err := r.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("after the last block: %v", err)
		}
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	r.ReuseBlockData()
	for i := 0; i < 200; i++ { // the buffer grows to the largest block
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
	}
	allocs := testing.AllocsPerRun(1000, func() {
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per block", allocs)
	}
}
