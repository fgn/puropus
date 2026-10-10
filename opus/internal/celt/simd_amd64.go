//go:build goexperiment.simd && amd64

package celt

import "simd/archsimd"

// The vector kernels use 256-bit vectors and need AVX2: Go's asynchronous
// preemption preserves the upper halves of the vector registers only when
// the CPU reports AVX2. Without it the decoder runs the scalar code. Each
// kernel ends with ClearAVXUpperBits so the scalar float code after it does
// not run with dirty upper state.
var useAVX2 = archsimd.X86.AVX() && archsimd.X86.AVX2()

// buildSIMDTables lays out the vector kernels' tables (initTables).
func buildSIMDTables() {
	buildSIMDFFT()
	buildIMDCTTables()
}

// Acceleration reports the vector kernels the decoder selected:
// "simd-amd64-avx2" or "scalar".
func Acceleration() string {
	if useAVX2 {
		return "simd-amd64-avx2"
	}
	return "scalar"
}
