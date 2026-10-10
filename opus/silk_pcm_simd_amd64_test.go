//go:build goexperiment.simd && amd64

package opus

import (
	"math"
	"math/rand/v2"
	"simd/archsimd"
	"testing"
)

// TestSilkPCMTierMatchesCPU checks the kernel selection against the CPU
// features (and any GODEBUG=cpu.* overrides).
func TestSilkPCMTierMatchesCPU(t *testing.T) {
	want := "scalar"
	if archsimd.X86.AVX() {
		want = "simd-amd64-avx"
	}
	if pcmAccel() != want {
		t.Fatalf("pcmAccel() = %q, CPU features select %q", pcmAccel(), want)
	}
}

// TestSilkPCMKernelsMatchScalar runs the vector kernels against the scalar
// loops at every length up to two frames and every alignment.
func TestSilkPCMKernelsMatchScalar(t *testing.T) {
	if !pcmUseAVX {
		t.Skip("no AVX")
	}
	r := rand.New(rand.NewPCG(25, 26))
	for n := range 2*960 + 9 {
		for align := range 4 {
			x := silkPCMInput(r, align+n)[align:]
			base := make([]float32, n+1)
			for i := range base {
				base[i] = float32(r.NormFloat64())
			}
			for _, k := range []struct {
				name        string
				vec, scalar func([]float32, []int16)
			}{{"silkToFloat", silkToFloatAVX, silkToFloatScalar}, {"addSilk", addSilkAVX, addSilkScalar}} {
				got, want := append([]float32(nil), base...), append([]float32(nil), base...)
				k.vec(got[:n], x)
				k.scalar(want[:n], x)
				for i := range got {
					if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
						t.Fatalf("%s n %d align %d sample %d differs", k.name, n, align, i)
					}
				}
			}
		}
	}
}
