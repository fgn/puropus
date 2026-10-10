package opus

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// transitionVectorRecord is one packet or loss of a libopus reference
// sequence, with the result, final range and PCM position libopus produced.
type transitionVectorRecord struct {
	WaveformClass  string `json:"waveform_class"`
	Index          int    `json:"index"`
	Event          string `json:"event"`
	Config         int    `json:"config"`
	PreviousConfig int    `json:"previous_config"`
	Missing        bool   `json:"missing"`
	PacketHex      string `json:"packet_hex"`
	Result         int    `json:"result"`
	FinalRange     uint32 `json:"final_range"`
	PCMOffset      int    `json:"pcm_offset"`
	Samples        int    `json:"samples"`
}

type transitionVectorCase struct {
	Records []transitionVectorRecord `json:"records"`
	Samples int                      `json:"samples"`
}

type transitionVectorManifest struct {
	Cases []struct {
		Name       string `json:"name"`
		Records    int    `json:"records"`
		Samples    int    `json:"samples"`
		JSONSHA256 string `json:"json_sha256"`
		PCMSHA256  string `json:"pcm_sha256"`
		// GenericPCMSHA256 pins <name>.generic.f32le, the decode of libopus's
		// generic C build (tools/libopus-ref/generic-vectors.sh).
		GenericPCMSHA256 string `json:"generic_pcm_sha256"`
	} `json:"cases"`
}

// transitionVectorError accumulates the PCM error of one group of frames.
type transitionVectorError struct {
	Samples                         int
	Max, WorstRMS                   float64
	MaxRecord, MaxSample, RMSRecord int
	// Exact counts the samples with the reference's bits.
	Exact int
}

// add accumulates the error of got against want for samples [start, end) of
// record.
func (g *transitionVectorError) add(record int, got, want []float32) {
	var squared float64
	for i := range got {
		if math.Float32bits(got[i]) == math.Float32bits(want[i]) {
			g.Exact++
		}
		delta := math.Abs(float64(got[i]) - float64(want[i]))
		squared += delta * delta
		if delta > g.Max {
			g.Max = delta
			g.MaxRecord = record
			g.MaxSample = i
		}
	}
	rms := math.Sqrt(squared / float64(len(got)))
	if rms > g.WorstRMS {
		g.WorstRMS = rms
		g.RMSRecord = record
	}
	g.Samples += len(got)
}

func (g *transitionVectorError) String() string {
	return fmt.Sprintf("samples=%d exact=%d worst_frame_RMS=%.3g record=%d max=%.3g record=%d sample=%d",
		g.Samples, g.Exact, g.WorstRMS, g.RMSRecord, g.Max, g.MaxRecord, g.MaxSample)
}

// Waveform gates. On amd64 and 386 every sample must have the bits of the
// decode of libopus's generic C build, the code the port follows,
// concealment included, in the scalar, SIMD, GOAMD64=v3 and 386 builds
// alike (genericBitExact). Elsewhere, where fused multiply-adds and the
// math library need not round as on x86, every frame must stay within RMS
// 1e-6 and peak 1e-4 of it. The committed reference
// PCM comes from libopus's default build, whose SSE xcorr_kernel, which
// celt_fir and celt_iir use in CELT pitch concealment, sums in another order
// than the generic C. Measured with the two libopus builds on these
// sequences, CELT pitch concealment and the frames recovering from it are up
// to RMS 2.60e-4 and peak 4.79e-4 apart (transition-vectors loss-celt-long,
// record 14; in stereo-vectors at most RMS 5.88e-5, loss-celt mono record 12,
// and peak 1.88e-4, loss-celt stereo record 12), and every other frame
// within RMS 1.8e-8 and peak 1.2e-7. Against the default build those frames
// use RMS 3e-4 and peak 5e-4, every other frame the strict limits.
const (
	strictRMS, strictPeak = 1e-6, 1e-4
	buildsRMS, buildsPeak = 3e-4, 5e-4
)

// genericBitExact reports whether this architecture's float arithmetic
// rounds as the generic build's does on x86-64, so its PCM must match
// bit for bit, not only within the waveform gates.
func genericBitExact() bool {
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "386"
}

// transitionVectorMode returns the mode of a configuration (0 SILK, 1 hybrid,
// 2 CELT), or -1 for none.
func transitionVectorMode(config int) int {
	if config < 0 {
		return -1
	}
	if config < 12 {
		return 0
	}
	if config < 16 {
		return 1
	}
	return 2
}

// readPinnedFile reads a reference file and fails the test unless its SHA-256
// is wantSHA256.
func readPinnedFile(t *testing.T, path, wantSHA256 string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != wantSHA256 {
		t.Fatalf("%s: SHA-256 %s, want %s", path, got, wantSHA256)
	}
	return data
}

// TestTransitionVectors replays thirteen libopus 1.5.2 loss and mode
// transition sequences on a mono decoder. Results, sample counts and final
// ranges match exactly. PCM is gated per frame against the decode of
// libopus's generic C build at the strict limits, and against its default
// build at the strict limits except for CELT pitch concealment and its
// recovery, where the two libopus builds differ (see strictRMS).
func TestTransitionVectors(t *testing.T) {
	root := filepath.Join("testdata", "transition-vectors")
	manifestBytes, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest transitionVectorManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 13 {
		t.Fatalf("expected 13 reference cases, got %d", len(manifest.Cases))
	}
	var totalRecords, totalSamples int
	seen := map[string]bool{}
	for _, entry := range manifest.Cases {
		if seen[entry.Name] || entry.Records < 1 || entry.Samples < 1 || entry.GenericPCMSHA256 == "" {
			t.Fatal("duplicate, empty or incomplete reference case")
		}
		seen[entry.Name] = true
		totalRecords += entry.Records
		totalSamples += entry.Samples
	}
	if totalRecords != 732 || totalSamples != 760800 {
		t.Fatalf("incomplete reference corpus: %d records / %d samples", totalRecords, totalSamples)
	}
	for _, entry := range manifest.Cases {
		t.Run(entry.Name, func(t *testing.T) {
			var vector transitionVectorCase
			if err := json.Unmarshal(readPinnedFile(t, filepath.Join(root, entry.Name+".json"), entry.JSONSHA256), &vector); err != nil {
				t.Fatal(err)
			}
			reference := float32sFromLE(readPinnedFile(t, filepath.Join(root, entry.Name+".f32le"), entry.PCMSHA256))
			generic := float32sFromLE(readPinnedFile(t, filepath.Join(root, entry.Name+".generic.f32le"), entry.GenericPCMSHA256))
			if len(vector.Records) != entry.Records || vector.Samples != entry.Samples || len(reference) != entry.Samples || len(generic) != entry.Samples {
				t.Fatal("fixture counts disagree")
			}
			decoder, err := NewDecoder(1)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]float32, MaxPacketSamples)
			// Per group: this decoder against the default and the generic
			// build, and the two libopus builds against each other.
			type groupErrors struct{ stock, generic, builds transitionVectorError }
			groups := map[string]*groupErrors{}
			decodedSamples := 0
			for index, record := range vector.Records {
				if record.Index != index {
					t.Fatalf("record index %d, want %d", record.Index, index)
				}
				switch record.WaveformClass {
				case "", "normal", "silk-loss-recovery", "celt-pitch-loss-recovery", "celt-noise-loss-recovery":
				default:
					t.Fatalf("unknown waveform class %q", record.WaveformClass)
				}
				packet, err := hex.DecodeString(record.PacketHex)
				if err != nil {
					t.Fatal(err)
				}
				var n int
				if record.Missing {
					n, err = decoder.Conceal(record.Samples, out)
				} else {
					n, err = decoder.Decode(packet, out)
				}
				if record.Result < 0 {
					if err == nil {
						t.Errorf("record %d %s: expected invalid-packet error", record.Index, record.Event)
					}
					if got := decoder.FinalRange(); got != record.FinalRange {
						t.Errorf("record %d failed decode changed range: %#x want %#x", record.Index, got, record.FinalRange)
					}
					continue
				}
				if err != nil {
					t.Fatalf("record %d %s: %v", record.Index, record.Event, err)
				}
				if n != record.Result || n != record.Samples {
					t.Fatalf("record %d: samples %d want %d", record.Index, n, record.Samples)
				}
				if decodedSamples != record.PCMOffset {
					t.Fatalf("record %d: offset %d want %d", record.Index, decodedSamples, record.PCMOffset)
				}
				decodedSamples += n
				if got := decoder.FinalRange(); got != record.FinalRange {
					t.Errorf("record %d %s: final range %#x want %#x", record.Index, record.Event, got, record.FinalRange)
				}
				for i, v := range out[:n] {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Fatalf("record %d sample %d: non-finite PCM", record.Index, i)
					}
				}
				add := func(label string, start, end int) {
					if start == end {
						return
					}
					g := groups[label]
					if g == nil {
						g = &groupErrors{}
						groups[label] = g
					}
					at := record.PCMOffset
					g.stock.add(record.Index, out[start:end], reference[at+start:at+end])
					g.generic.add(record.Index, out[start:end], generic[at+start:at+end])
					g.builds.add(record.Index, generic[at+start:at+end], reference[at+start:at+end])
				}
				previous, current := transitionVectorMode(record.PreviousConfig), transitionVectorMode(record.Config)
				switch {
				case record.WaveformClass != "":
					add(record.WaveformClass, 0, n)
				case strings.HasPrefix(entry.Name, "loss-") && record.Index >= 12:
					add("loss-and-recovery-reference-PLC", 0, n)
				case previous == 1 && current == 0:
					add("hybrid-to-silk-tail", 0, min(n, 120))
					add("hybrid-to-silk-normal", min(n, 120), n)
				case previous >= 0 && previous != current && (previous == 2 || current == 2):
					prefix := "transition-PLC"
					if previous == 2 {
						prefix = "transition-CELT-pitch-PLC"
					}
					add(prefix+"-first-120", 0, min(n, 120))
					add(prefix+"-next-120", min(n, 120), min(n, 240))
					add("transition-normal-after-240", min(n, 240), n)
				default:
					add("normal", 0, n)
				}
			}
			if decodedSamples != entry.Samples {
				t.Errorf("decoded %d samples, want %d", decodedSamples, entry.Samples)
			}
			for _, label := range slices.Sorted(maps.Keys(groups)) {
				g := groups[label]
				t.Logf("%s: against the generic build %v; against the default build %v; libopus builds apart %v",
					label, &g.generic, &g.stock, &g.builds)
				if genericBitExact() && g.generic.Exact != g.generic.Samples {
					t.Errorf("%s: %d of %d samples have the generic build's bits", label, g.generic.Exact, g.generic.Samples)
				}
				if g.generic.WorstRMS > strictRMS || g.generic.Max > strictPeak {
					t.Errorf("%s waveform exceeds RMS %g / max %g against the generic build", label, strictRMS, strictPeak)
				}
				rmsLimit, peakLimit := strictRMS, strictPeak
				if strings.Contains(label, "CELT-pitch-PLC") || label == "celt-pitch-loss-recovery" || (entry.Name == "loss-celt" && label == "loss-and-recovery-reference-PLC") {
					rmsLimit, peakLimit = buildsRMS, buildsPeak
				}
				if g.stock.WorstRMS > rmsLimit || g.stock.Max > peakLimit {
					t.Errorf("%s waveform exceeds RMS %g / max %g against the default build", label, rmsLimit, peakLimit)
				}
			}
		})
	}
}

// float32sFromLE decodes little-endian float32 samples.
func float32sFromLE(raw []byte) []float32 {
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return out
}
