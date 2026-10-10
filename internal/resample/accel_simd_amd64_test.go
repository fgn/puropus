//go:build goexperiment.simd && amd64

package resample

import (
	"simd/archsimd"
	"testing"
)

// TestTierMatchesCPU checks the dispatch against the CPU features (and any
// GODEBUG=cpu.* overrides).
func TestTierMatchesCPU(t *testing.T) {
	x := archsimd.X86
	want := "scalar"
	switch {
	case x.AVX() && x.AVX2() && x.FMA() && x.AVX512():
		want = "simd-amd64-avx512"
	case x.AVX() && x.AVX2() && x.FMA():
		want = "simd-amd64-avx2-fma"
	case x.AVX() && x.AVX2():
		want = "simd-amd64-avx2"
	case x.AVX():
		want = "simd-amd64-avx"
	}
	if Acceleration() != want {
		t.Fatalf("Acceleration() = %q, CPU features select %q", Acceleration(), want)
	}
	t.Logf("AVX %v AVX2 %v FMA %v AVX-512 %v: %s", x.AVX(), x.AVX2(), x.FMA(), x.AVX512(), want)
}
