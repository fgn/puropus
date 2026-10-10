package opus

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// silkUnitReferences is testdata/silk-units.json at the repository root:
// libopus values for unit tests of the SILK stages and one hybrid packet
// (see testdata/manifest.json for their provenance).
type silkUnitReferences struct {
	Source string `json:"source"`
	// Hybrid is libopus's float decode of hybridAnchorPacket on a fresh
	// mono decoder.
	Hybrid []float32 `json:"hybrid"`
	// Silk is raw SILK integer synthesis.
	Silk []int16 `json:"silk"`
	// Resamplers holds libopus SILK resampler runs from each SILK internal
	// rate to 48 kHz.
	Resamplers []struct {
		Rate   int     `json:"rate"`
		Input  []int16 `json:"input"`
		Output []int16 `json:"output"`
	} `json:"resamplers"`
}

func readSilkUnitReferences(t *testing.T) silkUnitReferences {
	t.Helper()
	raw, err := os.ReadFile("../testdata/silk-units.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref silkUnitReferences
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// checkFloatVec requires got to have want's length and every sample within
// tol of it.
func checkFloatVec(t *testing.T, name string, got, want []float32, tol float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d values, want %d", name, len(got), len(want))
	}
	worst, at := 0.0, 0
	for i := range want {
		d := math.Abs(float64(got[i]) - float64(want[i]))
		if math.IsNaN(d) {
			t.Fatalf("%s[%d]: %v, want %v", name, i, got[i], want[i])
		}
		if d > worst {
			worst, at = d, i
		}
	}
	if worst > tol {
		t.Fatalf("%s[%d]: %v, want %v (difference %.3g, tolerance %.3g)", name, at, got[at], want[at], worst, tol)
	}
}
