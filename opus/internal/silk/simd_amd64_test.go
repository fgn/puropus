//go:build goexperiment.simd && amd64

package silk

import (
	"math/rand/v2"
	"simd/archsimd"
	"slices"
	"testing"
)

// TestTierMatchesCPU checks the kernel selection against the CPU features
// (and any GODEBUG=cpu.* overrides).
func TestTierMatchesCPU(t *testing.T) {
	want := "scalar"
	if archsimd.X86.AVX() {
		want = "simd-amd64-avx"
	}
	if accel() != want {
		t.Fatalf("accel() = %q, CPU features select %q", accel(), want)
	}
	t.Logf("AVX %v: %s", archsimd.X86.AVX(), want)
}

// TestKernelsMatchScalar runs each vector kernel against the scalar code it
// replaces, for every length up to the largest batch and at every
// alignment of the input.
func TestKernelsMatchScalar(t *testing.T) {
	if !useAVX {
		t.Skip("no AVX")
	}
	r := rand.New(rand.NewPCG(21, 22))
	fir := []struct {
		name        string
		ratio       int
		vec, scalar func(out, buf []int16, n int) int
	}{
		{"interpolFIR3Over2", 3, interpolFIR3Over2AVX, interpolFIR3Over2Scalar},
		{"interpolFIR2", 4, interpolFIR2AVX, interpolFIR2Scalar},
		{"interpolFIR3", 6, interpolFIR3AVX, interpolFIR3Scalar},
	}
	for _, k := range fir {
		for n := 1; n <= resamplerFixedRatioMaxIn; n++ {
			for align := range 4 {
				buf := make([]int16, align+2*n+resamplerOrderFIR12)[align:]
				randInt16s(r, buf)
				got := make([]int16, k.ratio*n+1)
				want := make([]int16, k.ratio*n+1)
				got[k.ratio*n], want[k.ratio*n] = 12345, 12345
				if a, b := k.vec(got[:k.ratio*n], buf, n), k.scalar(want[:k.ratio*n], buf, n); a != b {
					t.Fatalf("%s n %d: %d and %d outputs", k.name, n, a, b)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%s n %d align %d: output differs", k.name, n, align)
				}
			}
		}
	}
	for length := 1; length <= 2*maxFsKHz*resamplerMaxBatchSizeMs; length++ {
		for align := range 4 {
			in := make([]int16, align+length)[align:]
			randInt16s(r, in)
			var st [resamplerMaxIIROrder]int32
			for i := range st {
				st[i] = int32(r.Uint32()) >> r.IntN(12)
			}
			stRef := st
			got, want := make([]int16, 2*length+1), make([]int16, 2*length+1)
			got[2*length], want[2*length] = 12345, 12345
			up2HQAVX(&st, got[:2*length], in, length)
			resamplerPrivateUp2HQScalar(&stRef, want[:2*length], in, length)
			if !slices.Equal(got, want) || st != stRef {
				t.Fatalf("up2HQ length %d align %d: output or state differs", length, align)
			}
		}
	}
	for n := range maxAPIFrameLength + 1 {
		for align := range 4 {
			left := make([]int16, align+n)[align:]
			right := make([]int16, n)
			randInt16s(r, left)
			randInt16s(r, right)
			got, want := make([]int16, 2*n+1), make([]int16, 2*n+1)
			got[2*n], want[2*n] = 12345, 12345
			interleaveAVX(got[:2*n], left, right)
			interleaveScalar(want[:2*n], left, right)
			if !slices.Equal(got, want) {
				t.Fatalf("interleave n %d align %d: output differs", n, align)
			}
		}
	}
	for n := range maxFrameLength + 1 {
		mid, side := make([]int16, n), make([]int16, n)
		randInt16s(r, mid)
		randInt16s(r, side)
		m1, s1 := slices.Clone(mid), slices.Clone(side)
		msToLRAVX(mid, side)
		msToLRScalar(m1, s1)
		if !slices.Equal(mid, m1) || !slices.Equal(side, s1) {
			t.Fatalf("msToLR n %d: output differs", n)
		}
	}
}
