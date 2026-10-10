package puropus

import (
	"bytes"
	"math"
	"slices"
	"testing"

	"github.com/fgn/puropus/opus"
)

// TestModeSwitchRedundancyMatchesLibopus checks the SILK<->CELT transitions
// of a libopus stream against libopus's own decode. The fixture was produced
// by forcing the encoder's mode per frame (SSSSSccccc, wideband CELT, VBR);
// each switch carries a redundant 5 ms CELT frame at the end of the last
// SILK-only packet or the start of the first one. libopus (RFC 6716
// reference) locates that frame by the bit position after the celt_to_silk
// bit. FFmpeg's native decoder output departs from libopus's at the switch
// at frame 26, where the SILK data ends on a byte boundary, so the
// golden comes from libopus. Every frame, including SILK and the
// transition fades, must meet the strict waveform gates.
func TestModeSwitchRedundancyMatchesLibopus(t *testing.T) {
	data := readFixture(t, "ogg-switch-silk-celt-wb.opus")
	want := readPCMGolden(t, "ogg-switch-silk-celt-wb.libopus48.f32le")
	got, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	if len(got) != len(want) {
		t.Fatalf("%d samples, golden has %d", len(got), len(want))
	}
	src, err := ReadPackets(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Packets) != 40 || src.Header.PreSkip != 0 {
		t.Fatalf("%d packets with pre-skip %d, want 40 with none", len(src.Packets), src.Header.PreSkip)
	}
	celtFrames, switches := 0, 0
	prevMode := opus.Mode(-1)
	for i, p := range src.Packets {
		parsed, err := opus.Parse(p.Data, false)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Mode != prevMode && prevMode >= 0 {
			switches++
		}
		prevMode = parsed.Mode
		frame := got[i*960 : (i+1)*960]
		ref := want[i*960 : (i+1)*960]
		worst := 0.0
		var sum float64
		for j := range frame {
			d := math.Abs(float64(frame[j] - ref[j]))
			worst = math.Max(worst, d)
			sum += d * d
		}
		rms := math.Sqrt(sum / 960)
		switch parsed.Mode {
		case opus.ModeCELT:
			celtFrames++
			if worst > 1e-5 || rms > 1e-6 {
				t.Errorf("CELT frame %d: max %.3g rms %.3g vs libopus", i, worst, rms)
			}
		default:
			if worst > gateMax || rms > gateRMS {
				t.Errorf("%v frame %d: max %.3g vs libopus, above the waveform gate", parsed.Mode, i, worst)
			}
		}
	}
	if celtFrames < 10 || switches < 6 {
		t.Fatalf("fixture has %d CELT frames and %d mode switches; expected a stream that exercises transitions", celtFrames, switches)
	}
}

// TestSilkConfigurationsDecodeInBothModes requires every SILK and hybrid
// fixture to decode in full to non-silent audio, and tolerant mode to return
// exactly the strict decode: valid packets are never concealed.
func TestSilkConfigurationsDecodeInBothModes(t *testing.T) {
	for _, name := range fixtures {
		if !silkFixture(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			data := readFixture(t, name)
			strict, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
			tolerant, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{Tolerant: true})
			if len(strict) != readGolden48k(t, name).Samples {
				t.Fatalf("decoded %d samples", len(strict))
			}
			if !slices.Equal(strict, tolerant) {
				t.Fatal("tolerant mode changed valid audio")
			}
			var energy float64
			for _, sample := range strict {
				energy += float64(sample) * float64(sample)
			}
			if energy < 1 {
				t.Fatal("speech fixture decoded to silence")
			}
		})
	}
}

// TestFECStreamsCarryLBRR guards the FEC fixtures: the LBRR flag must be set
// on most of their SILK frames, otherwise the FEC path is untested.
func TestFECStreamsCarryLBRR(t *testing.T) {
	for _, name := range []string{"ogg-silk-wb-fec.opus", "ogg-hybrid-fb-fec.opus", "ogg-silk-nb-fec.opus", "ogg-silk-mb-fec.opus", "ogg-silk-wb-60ms-fec.opus"} {
		src, err := ReadPackets(bytes.NewReader(readFixture(t, name)))
		if err != nil {
			t.Fatal(err)
		}
		flagged := 0
		for _, p := range src.Packets {
			if opus.HasLBRR(p.Data) {
				flagged++
			}
		}
		if flagged < len(src.Packets)/2 {
			t.Fatalf("%s: only %d of %d packets carry LBRR", name, flagged, len(src.Packets))
		}
	}
}

// TestSilkStreamsMatchLibopus decodes streams whose reference must be
// libopus, because FFmpeg's native decoder output departs from it on
// multi-frame LBRR and SILK bandwidth changes. The opus package checks their entropy decoding and
// integer synthesis exactly (TestSilkFramesMatchLibopus); here every 20 ms
// of output must also meet the waveform gates.
func TestSilkStreamsMatchLibopus(t *testing.T) {
	for _, name := range []string{"ogg-silk-wb-60ms-fec", "ogg-silk-bandwidth-switch"} {
		t.Run(name, func(t *testing.T) {
			data := readFixture(t, name+".opus")
			want := readPCMGolden(t, name+".libopus48.f32le")
			got, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
			if len(got) != len(want) {
				t.Fatalf("samples %d, want %d", len(got), len(want))
			}
			maxRMS, maxPeak := 0.0, 0.0
			for start := 0; start < len(got); start += 960 {
				end := min(start+960, len(got))
				var sum, peak float64
				for i := start; i < end; i++ {
					diff := math.Abs(float64(got[i] - want[i]))
					if math.IsNaN(diff) || math.IsInf(diff, 0) {
						t.Fatalf("non-finite sample %d", i)
					}
					sum += diff * diff
					peak = max(peak, diff)
				}
				rms := math.Sqrt(sum / float64(end-start))
				maxRMS, maxPeak = max(maxRMS, rms), max(maxPeak, peak)
				if rms > gateRMS || peak > gateMax {
					t.Fatalf("frame %d: rms %.5g peak %.5g vs libopus", start/960, rms, peak)
				}
			}
			t.Logf("%d samples; worst 20 ms RMS %.5g, peak %.5g", len(got), maxRMS, maxPeak)
		})
	}
}
