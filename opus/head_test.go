package opus

import (
	"bytes"
	"errors"
	"testing"
)

func TestHeadRoundTrip(t *testing.T) {
	head := Head{Version: 1, Channels: 1, PreSkip: 312, InputSampleRate: 48000, OutputGain: -256}
	data := head.Marshal()
	if len(data) != 19 {
		t.Fatalf("len %d", len(data))
	}
	parsed, err := ParseHead(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != head {
		t.Fatalf("got %+v want %+v", parsed, head)
	}
	if !bytes.Equal(data, []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 1, 0x38, 0x01, 0x80, 0xBB, 0, 0, 0x00, 0xFF, 0}) {
		t.Fatalf("unexpected bytes % x", data)
	}
}

func TestHeadRejectsBadInput(t *testing.T) {
	if _, err := ParseHead([]byte("OpusTags")); !errors.Is(err, ErrNotOpusHead) {
		t.Fatalf("want ErrNotOpusHead, got %v", err)
	}
	if _, err := ParseHead(Head{Channels: 1}.Marshal()[:18]); err == nil {
		t.Fatal("short header accepted")
	}
	bad := Head{Version: 1, Channels: 1, MappingFamily: 1}.Marshal()
	if _, err := ParseHead(bad); err == nil {
		t.Fatal("mapping family 1 accepted")
	}
	if !IsTags(MarshalTags("x")) {
		t.Fatal("MarshalTags not recognized")
	}
}
