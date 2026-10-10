//go:build goexperiment.simd && amd64

package resample

import "simd/archsimd"

// kernels lists the filter kernels this CPU can run, preferred first. 256-bit
// kernels need AVX2 and 512-bit kernels AVX-512, the levels at which the
// runtime preserves the full vector registers; with only AVX the kernel uses
// 128-bit vectors.
func kernels() []kernel {
	x := archsimd.X86
	avx := x.AVX()
	avx2 := avx && x.AVX2()
	fma := avx2 && x.FMA()
	var ks []kernel
	if fma && x.AVX512() {
		ks = append(ks, kernel{name: "simd-amd64-avx512", fn: filterAVX512})
	}
	if fma {
		ks = append(ks, kernel{name: "simd-amd64-avx2-fma", fn: filterAVX2FMA})
	}
	if avx2 {
		ks = append(ks, kernel{name: "simd-amd64-avx2", fn: filterAVX2, exact: true})
	}
	if avx {
		ks = append(ks, kernel{name: "simd-amd64-avx", fn: filterAVX, exact: true})
	}
	return append(ks, kernel{name: "scalar", fn: filterScalar, exact: true})
}

// The vector kernels compute four outputs at a time. For each output they
// accumulate tap index i in lane i mod 8 (in filterAVX, lanes 0-3 and 4-7
// are two 128-bit accumulators), add the four remaining taps into lanes 0-3,
// and fold the upper four lanes onto the lower four; reduce4 then finishes
// the four sums in the order filterScalar uses. filterAVX and filterAVX2
// round every product and sum, so they match filterScalar exactly.
// filterAVX2FMA fuses each multiply-add, and filterAVX512 also accumulates
// sixteen lanes and folds them to eight first.
//
// A group needs src[0 : 9+nTaps]. Trailing outputs that do not fill a group
// go through the same group code on a zero-padded copy of their input, so
// every output has the same bits however the outputs are grouped.
//
// Each tier has its own driver, filterAVX, filterAVX2, filterAVX2FMA and
// filterAVX512, which calls its group function directly. Called through a
// function value, the group function could keep its arguments for all escape
// analysis knows, so the partial group's buffers would move to the heap and
// every call ending in one would allocate.

// groupSrc is the input one group of four outputs reads.
const groupSrc = 3*3 + nTaps

// filterAVX computes dst with groupAVX, four outputs at a time.
func filterAVX(dst, src []float32) {
	k := 0
	for ; k+4 <= len(dst); k += 4 {
		groupAVX((*[4]float32)(dst[k:]), src[3*k:])
	}
	if k < len(dst) {
		var w [groupSrc]float32
		var y [4]float32
		copy(w[:], src[3*k:])
		groupAVX(&y, w[:])
		archsimd.ClearAVXUpperBits()
		copy(dst[k:], y[:])
	}
	archsimd.ClearAVXUpperBits()
}

// filterAVX2 computes dst with groupAVX2, four outputs at a time.
func filterAVX2(dst, src []float32) {
	k := 0
	for ; k+4 <= len(dst); k += 4 {
		groupAVX2((*[4]float32)(dst[k:]), src[3*k:])
	}
	if k < len(dst) {
		var w [groupSrc]float32
		var y [4]float32
		copy(w[:], src[3*k:])
		groupAVX2(&y, w[:])
		archsimd.ClearAVXUpperBits()
		copy(dst[k:], y[:])
	}
	archsimd.ClearAVXUpperBits()
}

// filterAVX2FMA computes dst with groupAVX2FMA, four outputs at a time.
func filterAVX2FMA(dst, src []float32) {
	k := 0
	for ; k+4 <= len(dst); k += 4 {
		groupAVX2FMA((*[4]float32)(dst[k:]), src[3*k:])
	}
	if k < len(dst) {
		var w [groupSrc]float32
		var y [4]float32
		copy(w[:], src[3*k:])
		groupAVX2FMA(&y, w[:])
		archsimd.ClearAVXUpperBits()
		copy(dst[k:], y[:])
	}
	archsimd.ClearAVXUpperBits()
}

// filterAVX512 computes dst with groupAVX512, four outputs at a time.
func filterAVX512(dst, src []float32) {
	k := 0
	for ; k+4 <= len(dst); k += 4 {
		groupAVX512((*[4]float32)(dst[k:]), src[3*k:])
	}
	if k < len(dst) {
		var w [groupSrc]float32
		var y [4]float32
		copy(w[:], src[3*k:])
		groupAVX512(&y, w[:])
		archsimd.ClearAVXUpperBits()
		copy(dst[k:], y[:])
	}
	archsimd.ClearAVXUpperBits()
}

// groupAVX uses 128-bit vectors, multiplies and adds.
func groupAVX(dst *[4]float32, src []float32) {
	w0, w1, w2, w3 := windows(src)
	var l0, h0, l1, h1, l2, h2, l3, h3 archsimd.Float32x4
	for i := 0; i < vecTaps; i += 8 {
		tl, th := load4(taps[i:]), load4(taps[i+4:])
		l0 = l0.Add(load4(w0[i:]).Mul(tl))
		h0 = h0.Add(load4(w0[i+4:]).Mul(th))
		l1 = l1.Add(load4(w1[i:]).Mul(tl))
		h1 = h1.Add(load4(w1[i+4:]).Mul(th))
		l2 = l2.Add(load4(w2[i:]).Mul(tl))
		h2 = h2.Add(load4(w2[i+4:]).Mul(th))
		l3 = l3.Add(load4(w3[i:]).Mul(tl))
		h3 = h3.Add(load4(w3[i+4:]).Mul(th))
	}
	t := load4(taps[vecTaps:])
	l0 = l0.Add(load4(w0[vecTaps:]).Mul(t))
	l1 = l1.Add(load4(w1[vecTaps:]).Mul(t))
	l2 = l2.Add(load4(w2[vecTaps:]).Mul(t))
	l3 = l3.Add(load4(w3[vecTaps:]).Mul(t))
	reduce4(l0.Add(h0), l1.Add(h1), l2.Add(h2), l3.Add(h3)).StoreArray(dst)
}

// groupAVX2 uses 256-bit vectors, multiplies and adds.
func groupAVX2(dst *[4]float32, src []float32) {
	w0, w1, w2, w3 := windows(src)
	var a0, a1, a2, a3 archsimd.Float32x8
	for i := 0; i < vecTaps; i += 8 {
		t := load8(taps[i:])
		a0 = a0.Add(load8(w0[i:]).Mul(t))
		a1 = a1.Add(load8(w1[i:]).Mul(t))
		a2 = a2.Add(load8(w2[i:]).Mul(t))
		a3 = a3.Add(load8(w3[i:]).Mul(t))
	}
	t := load4(taps[vecTaps:])
	v0 := a0.GetLo().Add(load4(w0[vecTaps:]).Mul(t)).Add(a0.GetHi())
	v1 := a1.GetLo().Add(load4(w1[vecTaps:]).Mul(t)).Add(a1.GetHi())
	v2 := a2.GetLo().Add(load4(w2[vecTaps:]).Mul(t)).Add(a2.GetHi())
	v3 := a3.GetLo().Add(load4(w3[vecTaps:]).Mul(t)).Add(a3.GetHi())
	reduce4(v0, v1, v2, v3).StoreArray(dst)
}

// groupAVX2FMA uses 256-bit fused multiply-adds.
func groupAVX2FMA(dst *[4]float32, src []float32) {
	w0, w1, w2, w3 := windows(src)
	var a0, a1, a2, a3 archsimd.Float32x8
	for i := 0; i < vecTaps; i += 8 {
		t := load8(taps[i:])
		a0 = load8(w0[i:]).MulAdd(t, a0)
		a1 = load8(w1[i:]).MulAdd(t, a1)
		a2 = load8(w2[i:]).MulAdd(t, a2)
		a3 = load8(w3[i:]).MulAdd(t, a3)
	}
	t := load4(taps[vecTaps:])
	v0 := load4(w0[vecTaps:]).MulAdd(t, a0.GetLo()).Add(a0.GetHi())
	v1 := load4(w1[vecTaps:]).MulAdd(t, a1.GetLo()).Add(a1.GetHi())
	v2 := load4(w2[vecTaps:]).MulAdd(t, a2.GetLo()).Add(a2.GetHi())
	v3 := load4(w3[vecTaps:]).MulAdd(t, a3.GetLo()).Add(a3.GetHi())
	reduce4(v0, v1, v2, v3).StoreArray(dst)
}

// groupAVX512 uses 512-bit fused multiply-adds.
func groupAVX512(dst *[4]float32, src []float32) {
	w0, w1, w2, w3 := windows(src)
	var a0, a1, a2, a3 archsimd.Float32x16
	for i := 0; i < vecTaps; i += 16 {
		t := load16(taps[i:])
		a0 = load16(w0[i:]).MulAdd(t, a0)
		a1 = load16(w1[i:]).MulAdd(t, a1)
		a2 = load16(w2[i:]).MulAdd(t, a2)
		a3 = load16(w3[i:]).MulAdd(t, a3)
	}
	b0 := a0.GetLo().Add(a0.GetHi())
	b1 := a1.GetLo().Add(a1.GetHi())
	b2 := a2.GetLo().Add(a2.GetHi())
	b3 := a3.GetLo().Add(a3.GetHi())
	t := load4(taps[vecTaps:])
	v0 := load4(w0[vecTaps:]).MulAdd(t, b0.GetLo()).Add(b0.GetHi())
	v1 := load4(w1[vecTaps:]).MulAdd(t, b1.GetLo()).Add(b1.GetHi())
	v2 := load4(w2[vecTaps:]).MulAdd(t, b2.GetLo()).Add(b2.GetHi())
	v3 := load4(w3[vecTaps:]).MulAdd(t, b3.GetLo()).Add(b3.GetHi())
	reduce4(v0, v1, v2, v3).StoreArray(dst)
}

// windows returns the input windows of four consecutive outputs.
func windows(src []float32) (w0, w1, w2, w3 *[nTaps]float32) {
	src = src[:groupSrc]
	return (*[nTaps]float32)(src), (*[nTaps]float32)(src[3:]),
		(*[nTaps]float32)(src[6:]), (*[nTaps]float32)(src[9:])
}

// reduce4 returns, for each vk = [a, b, c, d], (a+c) + (b+d) in lane k.
func reduce4(v0, v1, v2, v3 archsimd.Float32x4) archsimd.Float32x4 {
	p01 := v0.ConcatPermuteScalars(0, 1, 4, 5, v1).Add(v0.ConcatPermuteScalars(2, 3, 6, 7, v1))
	p23 := v2.ConcatPermuteScalars(0, 1, 4, 5, v3).Add(v2.ConcatPermuteScalars(2, 3, 6, 7, v3))
	return p01.ConcatAddPairs(p23)
}

func load4(s []float32) archsimd.Float32x4 {
	return archsimd.LoadFloat32x4Array((*[4]float32)(s))
}

func load8(s []float32) archsimd.Float32x8 {
	return archsimd.LoadFloat32x8Array((*[8]float32)(s))
}

func load16(s []float32) archsimd.Float32x16 {
	return archsimd.LoadFloat32x16Array((*[16]float32)(s))
}
