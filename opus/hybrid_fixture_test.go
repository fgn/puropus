package opus

import (
	"os"
	"strconv"
	"testing"
)

// TestHybridExternalFixtureFullRun decodes an external Ogg Opus file that
// contains configuration 13 hybrid packets, named by OPUS_HYBRID_FIXTURE, and
// checks the packet count against OPUS_HYBRID_FIXTURE_PACKETS when set. It is
// skipped when OPUS_HYBRID_FIXTURE is unset.
func TestHybridExternalFixtureFullRun(t *testing.T) {
	fixture := os.Getenv("OPUS_HYBRID_FIXTURE")
	if fixture == "" {
		t.Skip("set OPUS_HYBRID_FIXTURE to run this external fixture test")
	}
	packets := readOggOpusAudioPacketsForTest(t, fixture)
	decoder, err := NewDecoder(1)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, MaxPacketSamples)
	config13Seen := false
	for packetIndex, data := range packets {
		parsed, err := Parse(data, false)
		if err != nil {
			t.Fatalf("parse packet %d: %v", packetIndex, err)
		}
		if parsed.Config == 13 {
			config13Seen = true
		}
		if _, err := decoder.Decode(data, out); err != nil {
			t.Fatalf("decode packet %d config=%d size=%d: %v", packetIndex, parsed.Config, parsed.FrameSize[0], err)
		}
	}
	if want := os.Getenv("OPUS_HYBRID_FIXTURE_PACKETS"); want != "" && want != strconv.Itoa(len(packets)) {
		t.Fatalf("decoded packets=%d, want %s", len(packets), want)
	}
	if !config13Seen {
		t.Fatal("config 13 hybrid packet not seen in external fixture")
	}
}
