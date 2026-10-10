package resample

import (
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
)

// fusedBound is the largest difference allowed between a kernel that fuses
// multiply-adds and filterScalar, in units of Σ|taps[i]·x[i]|: each sum takes
// at most 16 roundings of relative size 2^-24 along any path (13 additions
// into a lane, 3 reduction steps), so the two results lie within 32·2^-24 of
// each other relative to the sum of absolute products.
const fusedBound = 32.0 / (1 << 24)

func TestKernelsMatchScalar(t *testing.T) {
	rng := rand.New(rand.NewPCG(20, 21))
	for _, scale := range []float32{1, 1e-3, 3e4} {
		for _, n := range []int{0, 1, 2, 3, 4, 5, 17, 1920} {
			src := randomSignal(rng, 3*max(n-1, 0)+nTaps)
			for i := range src {
				src[i] *= scale
			}
			want := make([]float32, n)
			filterScalar(want, src)
			for _, k := range kernels() {
				got := make([]float32, n)
				k.fn(got, src) // src is exactly long enough: any over-read panics
				for i := range got {
					if k.exact {
						if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
							t.Fatalf("%s: output %d is %g, scalar %g", k.name, i, got[i], want[i])
						}
						continue
					}
					abs := 0.0
					for j, v := range src[3*i : 3*i+nTaps] {
						abs += math.Abs(float64(v) * float64(taps[j]))
					}
					if d := math.Abs(float64(got[i]) - float64(want[i])); d > fusedBound*abs {
						t.Fatalf("%s: output %d differs from scalar by %g (bound %g)", k.name, i, d, fusedBound*abs)
					}
				}
			}
		}
	}
}

func TestAcceleration(t *testing.T) {
	ks := kernels()
	if Acceleration() != ks[0].name {
		t.Fatalf("Acceleration() = %q, preferred kernel %q", Acceleration(), ks[0].name)
	}
	if last := ks[len(ks)-1]; last.name != "scalar" || !last.exact {
		t.Fatal("scalar kernel missing")
	}
	valid := map[string]bool{
		"scalar": true, "simd-amd64-avx512": true, "simd-amd64-avx2-fma": true,
		"simd-amd64-avx2": true, "simd-amd64-avx": true,
	}
	for _, k := range ks {
		if !valid[k.name] {
			t.Errorf("unknown kernel %q", k.name)
		}
	}
	t.Logf("kernels: %v", kernelNames(ks))
}

// TestNoAVXFallsBackToScalar runs with GODEBUG=cpu.avx=off (see the
// Taskfile) and checks that no vector kernel is selected.
func TestNoAVXFallsBackToScalar(t *testing.T) {
	if !strings.Contains(os.Getenv("GODEBUG"), "cpu.avx=off") {
		t.Skip("run with GODEBUG=cpu.avx=off")
	}
	if Acceleration() != "scalar" || len(kernels()) != 1 {
		t.Fatalf("AVX disabled, yet kernels %v", kernelNames(kernels()))
	}
	x := randomSignal(rand.New(rand.NewPCG(1, 2)), 3000)
	if got := resampleChunks(x, []int{960}); len(got) != 1000 {
		t.Fatalf("%d outputs", len(got))
	}
}

func kernelNames(ks []kernel) []string {
	names := make([]string, 0, len(ks))
	for _, k := range ks {
		names = append(names, k.name)
	}
	return names
}
