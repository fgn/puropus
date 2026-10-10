package opus

import (
	"bytes"
	"os"
	"testing"
)

// readOggOpusAudioPacketsForTest returns the audio packets of an Ogg Opus
// file, skipping the OpusHead and OpusTags headers.
func readOggOpusAudioPacketsForTest(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var packets [][]byte
	var partial []byte
	for pos := 0; pos < len(data); {
		if pos+27 > len(data) || !bytes.Equal(data[pos:pos+4], []byte("OggS")) {
			t.Fatalf("invalid Ogg page at byte %d", pos)
		}
		segments := int(data[pos+26])
		lacingStart := pos + 27
		payloadStart := lacingStart + segments
		if payloadStart > len(data) {
			t.Fatalf("truncated Ogg lacing table at byte %d", pos)
		}
		payloadSize := 0
		for i := 0; i < segments; i++ {
			payloadSize += int(data[lacingStart+i])
		}
		payloadEnd := payloadStart + payloadSize
		if payloadEnd > len(data) {
			t.Fatalf("truncated Ogg payload at byte %d", pos)
		}

		payloadOffset := payloadStart
		for i := 0; i < segments; i++ {
			size := int(data[lacingStart+i])
			partial = append(partial, data[payloadOffset:payloadOffset+size]...)
			payloadOffset += size
			if size < 255 {
				packet := append([]byte(nil), partial...)
				partial = partial[:0]
				if !bytes.HasPrefix(packet, []byte("OpusHead")) &&
					!bytes.HasPrefix(packet, []byte("OpusTags")) {
					packets = append(packets, packet)
				}
			}
		}
		pos = payloadEnd
	}
	return packets
}
