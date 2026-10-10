//go:build goexperiment.simd && amd64

package celt

import (
	"simd/archsimd"
	"testing"
)

// TestTierMatchesCPU checks the kernel selection against the CPU features
// (and any GODEBUG=cpu.* overrides).
func TestTierMatchesCPU(t *testing.T) {
	x := archsimd.X86
	want := "scalar"
	if x.AVX() && x.AVX2() {
		want = "simd-amd64-avx2"
	}
	if got := Acceleration(); got != want {
		t.Fatalf("Acceleration() = %q, CPU features select %q", got, want)
	}
	t.Logf("AVX %v AVX2 %v: %s", x.AVX(), x.AVX2(), want)
}
