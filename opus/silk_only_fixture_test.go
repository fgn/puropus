package opus

import (
	"os"
	"strconv"
	"testing"
)

// TestSILKOnlyExternalFixtureFullRun decodes an external Ogg Opus file of
// 20 ms WB SILK packets, named by OPUS_SILK_ONLY_FIXTURE, and checks the
// packet count against OPUS_SILK_ONLY_FIXTURE_PACKETS when set. It is skipped
// when OPUS_SILK_ONLY_FIXTURE is unset.
func TestSILKOnlyExternalFixtureFullRun(t *testing.T) {
	fixture := os.Getenv("OPUS_SILK_ONLY_FIXTURE")
	if fixture == "" {
		t.Skip("set OPUS_SILK_ONLY_FIXTURE to run this external fixture test")
	}
	packets := readOggOpusAudioPacketsForTest(t, fixture)
	decoder, err := NewDecoder(1)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, MaxPacketSamples)
	for packetIndex, data := range packets {
		parsed, err := Parse(data, false)
		if err != nil {
			t.Fatalf("parse packet %d: %v", packetIndex, err)
		}
		if parsed.Config != 9 || parsed.Mode != ModeSILK || parsed.Bandwidth != BandwidthWideband || parsed.FrameDuration != 960 {
			t.Fatalf("packet %d config=%d mode=%d bandwidth=%d duration=%d, want 9/SILK/WB/960",
				packetIndex, parsed.Config, parsed.Mode, parsed.Bandwidth, parsed.FrameDuration)
		}
		samples, err := decoder.Decode(data, out)
		if err != nil {
			t.Fatalf("decode packet %d: %v", packetIndex, err)
		}
		if samples != 960 {
			t.Fatalf("decode packet %d samples=%d, want 960", packetIndex, samples)
		}
	}
	if want := os.Getenv("OPUS_SILK_ONLY_FIXTURE_PACKETS"); want != "" && want != strconv.Itoa(len(packets)) {
		t.Fatalf("decoded packets=%d, want %s", len(packets), want)
	}
}
