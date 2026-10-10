package opus

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// silenceFloor bounds the output of silence frames on a fresh decoder.
// libopus's float de-emphasis adds VERY_SMALL (1e-30) to every input sample
// to keep the filter out of denormal numbers, so silence decodes to a
// constant offset that rises to 1e-30/(1-0.85)/32768 = 2.03e-34, not to
// zero; Decoder reproduces those samples bit for bit.
const silenceFloor = 1e-33

// TestSilenceFramesDecodeToZeros decodes Silence packets for each CELT
// bandwidth and a range of durations on a fresh decoder: every sample is
// zero but for libopus's anti-denormal offset (silenceFloor) and the sample
// count matches the duration.
func TestSilenceFramesDecodeToZeros(t *testing.T) {
	for _, bw := range []Bandwidth{BandwidthNarrowband, BandwidthWideband, BandwidthSuperWideband, BandwidthFullband} {
		for _, d := range []time.Duration{2500 * time.Microsecond, 5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 60 * time.Millisecond, 120 * time.Millisecond, 137500 * time.Microsecond} {
			packets, err := Silence(1, bw, d)
			if err != nil {
				t.Fatal(err)
			}
			dec, err := NewDecoder(1)
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			out := make([]float32, MaxPacketSamples)
			for i, p := range packets {
				parsed, err := Parse(p, false)
				if err != nil {
					t.Fatalf("bw=%d d=%v packet %d: %v", bw, d, i, err)
				}
				if parsed.Mode != ModeCELT {
					t.Fatalf("bw=%d d=%v packet %d: mode %d", bw, d, i, parsed.Mode)
				}
				n, err := dec.Decode(p, out)
				if err != nil {
					t.Fatalf("bw=%d d=%v packet %d: %v", bw, d, i, err)
				}
				for j := 0; j < n; j++ {
					if math.Abs(float64(out[j])) > silenceFloor {
						t.Fatalf("bw=%d d=%v packet %d sample %d = %g", bw, d, i, j, out[j])
					}
				}
				total += n
			}
			if want := int(d * 48000 / time.Second); total != want {
				t.Fatalf("bw=%d d=%v: %d samples, want %d", bw, d, total, want)
			}
		}
	}
	if _, err := Silence(1, BandwidthFullband, 3*time.Millisecond); err == nil {
		t.Fatal("off-grid duration accepted")
	}
}

// TestSilenceAfterSignalDecays checks that silence following a real CELT
// frame leaves only the overlap/de-emphasis tail, which drops below -100 dBFS
// within the first 10 ms.
func TestSilenceAfterSignalDecays(t *testing.T) {
	packet := testCELTPacket(t)
	dec, err := NewDecoder(1)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, MaxPacketSamples)
	if _, err := dec.Decode(packet, out); err != nil {
		t.Fatal(err)
	}
	silence, err := Silence(1, BandwidthFullband, 60*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	n, err := dec.Decode(silence[0], out)
	if err != nil {
		t.Fatal(err)
	}
	peakTail := 0.0
	for i := 480; i < n; i++ {
		peakTail = math.Max(peakTail, math.Abs(float64(out[i])))
	}
	if peakTail > 1e-5 {
		t.Fatalf("silence tail after 10 ms peaks at %g", peakTail)
	}
}

// testCELTPacket returns a 20 ms FB CELT packet with signal: the last packet
// before the first loss in the loss-celt transition vector.
func testCELTPacket(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/transition-vectors/loss-celt.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector transitionVectorCase
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	packet, err := hex.DecodeString(vector.Records[11].PacketHex)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}
