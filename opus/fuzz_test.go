package opus

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
	"time"
)

// FuzzDecode feeds arbitrary packets to the decoder, as packets and as the
// next packet of DecodeFEC. Any byte sequence must either decode or return an
// error; it must never panic or write outside the output buffer. A packet
// that parses fails only in hybrid mode and never in DecodeFEC, and a
// failure leaves the decoder state unchanged.
func FuzzDecode(f *testing.F) {
	silence, _ := Silence(1, BandwidthFullband, 20*time.Millisecond)
	f.Add(silence[0])
	f.Add([]byte{0xFB, 0x03, 0xFF, 0xFE, 0xFF, 0xFE, 0xFF, 0xFE})
	f.Add([]byte{0x00})
	f.Add([]byte{0x78, 0x00, 0x00})
	// Hybrid frames whose CELT redundancy is longer than the frame.
	bad, err := hex.DecodeString("3fc0d5d41550fd4f9ed4d013e1caaeb47059f3219f26ccd99dd76bc80aa2e84cf66451135f30abc8a958ebbf964b271ac0d785")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(append([]byte{14 << 3}, bad...))
	f.Add(append(append([]byte{14<<3 | 1}, bad...), bad...))
	// Packets of each mode; those of the FEC sequences carry LBRR data.
	for _, name := range []string{"loss-silk", "loss-hybrid", "loss-celt", "channels-silk-fec", "channels-hybrid-fec"} {
		raw, err := os.ReadFile("testdata/stereo-vectors/" + name + ".json")
		if err != nil {
			f.Fatal(err)
		}
		var vector stereoVector
		if err = json.Unmarshal(raw, &vector); err != nil {
			f.Fatal(err)
		}
		packet, err := hex.DecodeString(vector.Records[1].Packet)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(packet)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, channels := range []int{1, 2} {
			dec, err := NewDecoder(channels)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]float32, MaxPacketSamples*channels+8)
			for i := range out[MaxPacketSamples*channels:] {
				out[MaxPacketSamples*channels+i] = 12345
			}
			parsed, parseErr := Parse(data, false)
			n, err := dec.Decode(data, out[:MaxPacketSamples*channels])
			if err == nil && (n < 0 || n > MaxPacketSamples) {
				t.Fatalf("decoded %d samples", n)
			}
			if parseErr == nil && err != nil && parsed.Mode != ModeHybrid {
				t.Fatalf("%v packet failed: %v", parsed.Mode, err)
			}
			for i := range out[MaxPacketSamples*channels:] {
				if out[MaxPacketSamples*channels+i] != 12345 {
					t.Fatal("decoder wrote outside its output buffer")
				}
			}
			// Loss after arbitrary priming also exercises pitch/resampler histories.
			for _, samples := range []int{120, 360, 960, MaxPacketSamples} {
				if _, err := dec.Conceal(samples, out[:MaxPacketSamples*channels]); err != nil {
					t.Fatal(err)
				}
				for _, v := range out[:samples*channels] {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Fatal("non-finite concealment")
					}
				}
			}
			// The packet as the first one after a loss: DecodeFEC fails
			// exactly when it does not parse, then leaving the state as it
			// was, and otherwise fills the lost duration with finite audio.
			for _, samples := range []int{120, 960, 2880, MaxPacketSamples} {
				before := stateOf(dec)
				n, err := dec.DecodeFEC(data, samples, out[:MaxPacketSamples*channels])
				if (err != nil) != (parseErr != nil) {
					t.Fatalf("DecodeFEC error %v for a packet whose parse error is %v", err, parseErr)
				}
				if err != nil {
					if !reflect.DeepEqual(stateOf(dec), before) {
						t.Fatal("a rejected DecodeFEC changed the decoder state")
					}
					continue
				}
				if n != samples {
					t.Fatalf("DecodeFEC returned %d of %d samples", n, samples)
				}
				for _, v := range out[:samples*channels] {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Fatal("non-finite FEC decode")
					}
				}
				for _, v := range out[MaxPacketSamples*channels:] {
					if v != 12345 {
						t.Fatal("DecodeFEC overran output")
					}
				}
			}
			// A second packet exercises recovery from that loss.
			before := stateOf(dec)
			n, err = dec.Decode(data, out[:MaxPacketSamples*channels])
			if err != nil && !reflect.DeepEqual(stateOf(dec), before) {
				t.Fatal("a rejected packet changed the decoder state")
			}
			if err == nil {
				for _, v := range out[:n*channels] {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Fatal("nonfinite recovery")
					}
				}
			}
			for _, v := range out[MaxPacketSamples*channels:] {
				if v != 12345 {
					t.Fatal("stateful decode overran output")
				}
			}
		}
	})
}
