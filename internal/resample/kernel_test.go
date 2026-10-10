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

// filterLoop is filterScalar with its taps in a loop, eight at a time, rather
// than written out: the same products and sums in the same order.
func filterLoop(dst, src []float32) {
	for k := range dst {
		w := (*[nTaps]float32)(src[3*k:])
		var s0, s1, s2, s3, s4, s5, s6, s7 float32
		for i := 0; i < vecTaps; i += 8 {
			x := (*[8]float32)(w[i:])
			t := (*[8]float32)(taps[i:])
			s0 += float32(x[0] * t[0])
			s1 += float32(x[1] * t[1])
			s2 += float32(x[2] * t[2])
			s3 += float32(x[3] * t[3])
			s4 += float32(x[4] * t[4])
			s5 += float32(x[5] * t[5])
			s6 += float32(x[6] * t[6])
			s7 += float32(x[7] * t[7])
		}
		s0 += float32(w[vecTaps] * taps[vecTaps])
		s1 += float32(w[vecTaps+1] * taps[vecTaps+1])
		s2 += float32(w[vecTaps+2] * taps[vecTaps+2])
		s3 += float32(w[vecTaps+3] * taps[vecTaps+3])
		dst[k] = ((s0 + s4) + (s2 + s6)) + ((s1 + s5) + (s3 + s7))
	}
}

// TestScalarMatchesLoop checks filterScalar's written-out taps against
// filterLoop bit for bit, on random input of several scales mixed with
// signed zeros, subnormals and infinities, and on a window of zeros whose
// products are all -0: its output is +0 only because each sum starts at +0.
func TestScalarMatchesLoop(t *testing.T) {
	rng := rand.New(rand.NewPCG(22, 23))
	const n = 500
	scales := []float32{1, 1e-3, 3e4, 1e-40}
	inputs := make([][]float32, 0, len(scales)+1)
	for _, scale := range scales {
		x := randomSignal(rng, 3*(n-1)+nTaps)
		for i := range x {
			x[i] *= scale
			switch rng.IntN(40) {
			case 0:
				x[i] = float32(math.Copysign(0, -1))
			case 1:
				x[i] = 0
			case 2:
				x[i] = math.Float32frombits(rng.Uint32() & 0x807fffff) // subnormal
			case 3:
				x[i] = float32(math.Inf(1 - 2*rng.IntN(2)))
			}
		}
		inputs = append(inputs, x)
	}
	negZero := make([]float32, nTaps)
	for i, v := range taps {
		negZero[i] = float32(math.Copysign(0, -float64(v)))
	}
	inputs = append(inputs, negZero)
	for _, x := range inputs {
		n := (len(x)-nTaps)/3 + 1
		got, want := make([]float32, n), make([]float32, n)
		filterScalar(got, x)
		filterLoop(want, x)
		for i := range got {
			if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
				t.Fatalf("output %d is %g (%#08x), the loop gives %g (%#08x)", i,
					got[i], math.Float32bits(got[i]), want[i], math.Float32bits(want[i]))
			}
		}
	}
}

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
