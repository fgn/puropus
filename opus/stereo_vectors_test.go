package opus

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// stereoVector is a libopus reference sequence for the stereo tests: packets
// and losses with the final range and PCM position libopus produced.
type stereoVector struct {
	Channels int `json:"channels"`
	Samples  int `json:"samples"`
	Records  []struct {
		Index      int    `json:"index"`
		Missing    bool   `json:"missing"`
		Packet     string `json:"packet_hex"`
		FrameSize  int    `json:"frame_size"`
		Samples    int    `json:"samples"`
		Offset     int    `json:"pcm_offset"`
		FinalRange uint32 `json:"final_range"`
	} `json:"records"`
}

// readFileHashes reads a manifest that maps reference file names to their
// SHA-256 hashes.
func readFileHashes(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.Files
}

// TestStereoReferenceVectors replays fifteen stereo libopus sequences, which
// switch modes, bandwidths and coded channel counts and include losses and
// DTX, on mono and stereo decoders. Sample counts and final ranges match
// exactly; PCM has the bits of libopus's generic build where
// genericBitExact holds, and everywhere stays within the frame limits of
// TestTransitionVectors against the decodes of the generic and default
// builds.
func TestStereoReferenceVectors(t *testing.T) {
	root := filepath.Join("testdata", "stereo-vectors")
	hashes := readFileHashes(t, filepath.Join(root, "manifest.json"))
	names := []string{"loss-silk", "loss-hybrid", "loss-celt", "loss-celt-2p5ms", "loss-celt-5ms", "loss-silk-nb10", "loss-silk-mb60", "quiet-dtx", "channels-silk-fec", "channels-hybrid-fec", "channels-celt", "modes", "transients-celt", "bandwidth-silk", "bandwidth-hybrid"}
	// Per sequence: the packets (.json), the default build's stereo and mono
	// PCM, and the generic build's (.generic.f32le, .generic.mono.f32le).
	if len(hashes) != 5*len(names) {
		t.Fatal("incomplete stereo manifest")
	}
	read := func(t *testing.T, name string) []byte {
		t.Helper()
		return readPinnedFile(t, filepath.Join(root, name), hashes[name])
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			var tc stereoVector
			if err := json.Unmarshal(read(t, name+".json"), &tc); err != nil {
				t.Fatal(err)
			}
			records := 72
			if name == "quiet-dtx" {
				records = 120
			}
			if tc.Channels != 2 || len(tc.Records) != records {
				t.Fatal("unexpected vector shape")
			}
			for _, channels := range []int{1, 2} {
				t.Run(fmt.Sprint(channels), func(t *testing.T) {
					suffix := ".f32le"
					if channels == 1 {
						suffix = ".mono.f32le"
					}
					gold := float32sFromLE(read(t, name+suffix))
					generic := float32sFromLE(read(t, name+".generic"+suffix))
					if len(gold) != tc.Samples*channels || len(generic) != len(gold) {
						t.Fatal("PCM count mismatch")
					}
					d, e := NewDecoder(channels)
					if e != nil {
						t.Fatal(e)
					}
					out := make([]float32, MaxPacketSamples*channels)
					total := 0
					// This decoder against the default and the generic build,
					// and the two libopus builds against each other.
					var stock, gen, builds transitionVectorError
					for i, r := range tc.Records {
						if r.Index != i || r.Offset != total || r.Samples <= 0 {
							t.Fatal("bad record geometry")
						}
						packet, e := hex.DecodeString(r.Packet)
						if e != nil {
							t.Fatal(e)
						}
						var n int
						if r.Missing {
							if len(packet) != 0 {
								t.Fatal("missing packet contains data")
							}
							n, e = d.Conceal(r.FrameSize, out)
						} else {
							if len(packet) == 0 {
								t.Fatal("empty regular packet")
							}
							n, e = d.Decode(packet, out)
						}
						if e != nil {
							t.Fatalf("record %d: %v", i, e)
						}
						if n != r.Samples {
							t.Fatalf("record %d: count %d, want %d", i, n, r.Samples)
						}
						if d.FinalRange() != r.FinalRange {
							t.Fatalf("record %d: range %x want %x", i, d.FinalRange(), r.FinalRange)
						}
						got := out[:n*channels]
						for _, v := range got {
							if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
								t.Fatalf("record %d: nonfinite PCM", i)
							}
						}
						at := total * channels
						var frameStock, frameGeneric transitionVectorError
						frameStock.add(i, got, gold[at:at+len(got)])
						frameGeneric.add(i, got, generic[at:at+len(got)])
						stock.add(i, got, gold[at:at+len(got)])
						gen.add(i, got, generic[at:at+len(got)])
						builds.add(i, generic[at:at+len(got)], gold[at:at+len(got)])
						if frameGeneric.WorstRMS > strictRMS || frameGeneric.Max > strictPeak {
							t.Errorf("record %d: peak %g / RMS %g against the generic build exceeds %g / %g",
								i, frameGeneric.Max, frameGeneric.WorstRMS, strictPeak, strictRMS)
						}
						// Against the default build, only CELT pitch
						// concealment and its recovery use the wider limits.
						peakLimit, rmsLimit := strictPeak, strictRMS
						if strings.HasPrefix(name, "loss-celt") && i >= 12 {
							peakLimit, rmsLimit = buildsPeak, buildsRMS
						}
						if frameStock.WorstRMS > rmsLimit || frameStock.Max > peakLimit {
							t.Errorf("record %d: peak %g / RMS %g against the default build exceeds %g / %g",
								i, frameStock.Max, frameStock.WorstRMS, peakLimit, rmsLimit)
						}
						total += n
					}
					if total != tc.Samples {
						t.Fatal("incomplete replay")
					}
					if genericBitExact() && gen.Exact != gen.Samples {
						t.Errorf("%d of %d samples have the generic build's bits", gen.Exact, gen.Samples)
					}
					t.Logf("against the generic build %v; against the default build %v; libopus builds apart %v",
						&gen, &stock, &builds)
				})
			}
		})
	}
}

// TestStereoDecodeBufferAndReset checks that a short stereo output buffer is
// rejected without touching the state or the buffer, and that Reset leaves a
// stereo decoder equal to a fresh one.
func TestStereoDecodeBufferAndReset(t *testing.T) {
	for _, name := range []string{"loss-silk", "loss-hybrid", "loss-celt"} {
		t.Run(name, func(t *testing.T) {
			raw, e := os.ReadFile("testdata/stereo-vectors/" + name + ".json")
			if e != nil {
				t.Fatal(e)
			}
			var tc stereoVector
			if e = json.Unmarshal(raw, &tc); e != nil {
				t.Fatal(e)
			}
			d, e := NewDecoder(2)
			if e != nil {
				t.Fatal(e)
			}
			buf := make([]float32, 2*MaxPacketSamples+8)
			for _, r := range tc.Records[:12] {
				p, e := hex.DecodeString(r.Packet)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = d.Decode(p, buf); e != nil {
					t.Fatal(e)
				}
			}
			before := *d
			p, e := hex.DecodeString(tc.Records[13].Packet)
			if e != nil {
				t.Fatal(e)
			}
			for i := range buf {
				buf[i] = 12345
			}
			if _, e = d.Decode(p, buf[:2*tc.Records[13].Samples-1]); e == nil {
				t.Fatal("accepted short stereo output")
			}
			if _, e = d.Conceal(960, buf[:1919]); e == nil {
				t.Fatal("concealment accepted short stereo output")
			}
			if !reflect.DeepEqual(*d, before) {
				t.Fatal("buffer failure changed state")
			}
			for _, v := range buf {
				if v != 12345 {
					t.Fatal("buffer failure wrote PCM")
				}
			}
			d.Reset()
			if n, e := d.Conceal(360, buf[:720]); e != nil || n != 360 {
				t.Fatalf("cold stereo PLC: %d %v", n, e)
			}
			for _, v := range buf[:720] {
				if v != 0 {
					t.Fatal("reset retained stereo audio")
				}
			}
			fresh, _ := NewDecoder(2)
			want := make([]float32, 2*MaxPacketSamples)
			n, e := d.Decode(p, buf[:2*MaxPacketSamples])
			if e != nil {
				t.Fatal(e)
			}
			m, e := fresh.Decode(p, want)
			if e != nil || n != m || !slices.Equal(buf[:n*2], want[:m*2]) {
				t.Fatal("reset retained stereo state")
			}
			for _, v := range buf[2*MaxPacketSamples:] {
				if v != 12345 {
					t.Fatal("stereo decode overran output")
				}
			}
		})
	}
}
