//go:build goexperiment.simd && amd64

// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2008 Xiph.Org Foundation
// Written by Jean-Marc Valin
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
//
// - Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//
// - Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER
// OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
// PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
// NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
// Ported from libopus 1.5.2 celt/mdct.c (clt_mdct_backward_c), vectorized:
// each lane computes one rotation or mirror step with the scalar code's
// arithmetic, so the result is bit for bit that of mdct.go.

package celt

import (
	"math"
	"simd/archsimd"
)

// The vector IMDCT works on eight consecutive steps i..i+7 of a loop at a
// time. De-interleaving pairs of complex values within each 128-bit half
// puts step i+k in lane imdctLane[k]: the lanes hold steps 0, 1, 4, 5, 2,
// 3, 6, 7. The tables below hold, per MDCT size, each loop's twiddles and
// bit-reversal offsets in that lane order.
var imdctLaneStep = [8]int{0, 1, 4, 5, 2, 3, 6, 7}

// imdctTables are the lane-ordered tables of one MDCT size: for step i of
// the pre-rotation, t[i] in pre0, t[N4+i] in pre1 and 2*bitrev[i] in rev;
// for step i of the post-rotation also t[N4-1-i] in post2 and t[N2-1-i] in
// post3. Steps that do not fill eight lanes are left to the scalar code.
type imdctTables struct {
	pre0, pre1, post2, post3 []float32
	rev                      []int32
}

var (
	imdctTablesOf [4]imdctTables
	// windowRev is the static mode's window reversed.
	windowRev [maxOverlap]float32
)

// buildIMDCTTables fills imdctTablesOf and windowRev (buildSIMDTables).
func buildIMDCTTables() {
	m := StaticMode()
	l := &m.mdct
	trig := l.trig
	N := l.n
	for shift := 0; shift <= l.maxshift; shift++ {
		if shift > 0 {
			trig = trig[N/2:]
			N >>= 1
		}
		N2, N4 := N>>1, N>>2
		t := trig[:N2]
		bitrev := l.kfft[shift].bitrev
		tb := &imdctTablesOf[shift]
		pre := N4 &^ 7
		post := (N4 >> 1) &^ 7
		tb.pre0 = make([]float32, pre)
		tb.pre1 = make([]float32, pre)
		tb.rev = make([]int32, pre)
		tb.post2 = make([]float32, post)
		tb.post3 = make([]float32, post)
		for i := 0; i < pre; i += 8 {
			for lane, k := range imdctLaneStep {
				tb.pre0[i+lane] = t[i+k]
				tb.pre1[i+lane] = t[N4+i+k]
				tb.rev[i+lane] = 2 * int32(bitrev[i+k])
				if i < post {
					tb.post2[i+lane] = t[N4-1-(i+k)]
					tb.post3[i+lane] = t[N2-1-(i+k)]
				}
			}
		}
	}
	for i := range windowRev {
		windowRev[i] = m.window[maxOverlap-1-i]
	}
}

// swapHalves8 exchanges the 128-bit halves of v.
func swapHalves8(v archsimd.Float32x8) archsimd.Float32x8 {
	return v.ConcatPermute128Scalars(1, 0, v)
}

// mdctBackwardAVX2 is cltMdctBackward for stride 1 and the static mode's
// window and overlap.
//
//go:noinline
func mdctBackwardAVX2(l *mdctLookup, in []float32, out []float32, window []float32, shift int) {
	const overlap = maxOverlap
	N := l.n >> shift
	N2 := N >> 1
	N4 := N >> 2
	trig := l.trig
	for i := 0; i < shift; i++ {
		trig = trig[l.n>>(i+1):]
	}
	t := trig[:N2]
	tb := &imdctTablesOf[shift]
	in = in[:N2]
	out = out[:overlap+N2]

	// Pre-rotate, storing the pre-rotation directly in the bitrev order:
	// the products leave the vectors as integer bit patterns, so scalar
	// float code never runs between the 256-bit operations.
	yp := out[overlap>>1 : overlap>>1+N2]
	var yiBits, yrBits [8]uint32
	i := 0
	for ; i < len(tb.rev); i += 8 {
		a1 := load8(in[2*i:])
		a2 := load8(in[2*i+8:])
		x1 := a1.ConcatPermuteScalarsGrouped(0, 2, 4, 6, a2) // in[2(i+k)]
		B := N2 - 2*i
		w1 := swapHalves8(load8(in[B-16:]))
		w2 := swapHalves8(load8(in[B-8:]))
		x2 := w2.ConcatPermuteScalarsGrouped(3, 1, 7, 5, w1) // in[N2-1-2(i+k)]
		t0 := load8(tb.pre0[i:])
		t1 := load8(tb.pre1[i:])
		x2.Mul(t0).Add(x1.Mul(t1)).ToBits().StoreArray(&yrBits)
		x1.Mul(t0).Sub(x2.Mul(t1)).ToBits().StoreArray(&yiBits)
		rev := (*[8]int32)(tb.rev[i : i+8])
		for k, r := range rev {
			// We swap real and imag because we use an FFT instead of an
			// IFFT.
			yp[r] = math.Float32frombits(yiBits[k])
			yp[r+1] = math.Float32frombits(yrBits[k])
		}
	}
	archsimd.ClearAVXUpperBits()
	for ; i < N4; i++ {
		rev := 2 * int(l.kfft[shift].bitrev[i])
		x1, x2 := in[2*i], in[N2-1-2*i]
		yp[rev+1] = float32(x2*t[i]) + float32(x1*t[N4+i])
		yp[rev] = float32(x1*t[i]) - float32(x2*t[N4+i])
	}

	opusFFTAVX2(&simdFFTs[shift], yp)

	// Post-rotate and de-shuffle from both ends of the buffer at once to
	// make it in-place: step i rotates value i (front) and value N4-1-i
	// (back), each stored as (imag, real).
	half := N4 >> 1
	i = 0
	for ; i < len(tb.post2); i += 8 {
		f := yp[2*i : 2*i+16]
		f1 := load8(f[0:])
		f2 := load8(f[8:])
		re := f1.ConcatPermuteScalarsGrouped(1, 3, 5, 7, f2)
		im := f1.ConcatPermuteScalarsGrouped(0, 2, 4, 6, f2)
		b := yp[2*(N4-8-i) : 2*(N4-i)]
		b1 := swapHalves8(load8(b[0:]))
		b2 := swapHalves8(load8(b[8:]))
		reB := b2.ConcatPermuteScalarsGrouped(3, 1, 7, 5, b1)
		imB := b2.ConcatPermuteScalarsGrouped(2, 0, 6, 4, b1)
		t0 := load8(tb.pre0[i:])
		t1 := load8(tb.pre1[i:])
		t2 := load8(tb.post2[i:])
		t3 := load8(tb.post3[i:])
		yr := re.Mul(t0).Add(im.Mul(t1))
		yi := re.Mul(t1).Sub(im.Mul(t0))
		yrB := reB.Mul(t2).Add(imB.Mul(t3))
		yiB := reB.Mul(t3).Sub(imB.Mul(t2))
		// The front values become (yr, yiB), the back ones (yrB, yi).
		store8(yr.ConcatPermuteScalarsGrouped(0, 4, 1, 5, yiB), f[0:])
		store8(yr.ConcatPermuteScalarsGrouped(2, 6, 3, 7, yiB), f[8:])
		store8(swapHalves8(yrB.ConcatPermuteScalarsGrouped(1, 5, 0, 4, yi)), b[8:])
		store8(swapHalves8(yrB.ConcatPermuteScalarsGrouped(3, 7, 2, 6, yi)), b[0:])
	}
	archsimd.ClearAVXUpperBits()
	for ; i < half; i++ {
		yp0, yp1 := 2*i, 2*(N4-1-i)
		re, im := yp[yp0+1], yp[yp0]
		yr := float32(re*t[i]) + float32(im*t[N4+i])
		yi := float32(re*t[N4+i]) - float32(im*t[i])
		re, im = yp[yp1+1], yp[yp1]
		yp[yp0] = yr
		yp[yp1+1] = yi
		yr = float32(re*t[N4-i-1]) + float32(im*t[N2-i-1])
		yi = float32(re*t[N2-i-1]) - float32(im*t[N4-i-1])
		yp[yp1] = yr
		yp[yp0+1] = yi
	}

	// Mirror on both sides for TDAC: step i combines out[i] and
	// out[overlap-1-i].
	reverse := archsimd.LoadUint32x8Array(&[8]uint32{7, 6, 5, 4, 3, 2, 1, 0})
	o := out[:overlap]
	i = 0
	for ; i+8 <= overlap/2; i += 8 {
		x2 := load8(o[i:])
		x1 := load8(o[overlap-8-i:]).Permute(reverse)
		w1 := load8(window[i:])
		w2 := load8(windowRev[i:])
		store8(w2.Mul(x2).Sub(w1.Mul(x1)), o[i:])
		store8(w1.Mul(x2).Add(w2.Mul(x1)).Permute(reverse), o[overlap-8-i:])
	}
	for ; i < overlap/2; i += 4 {
		x2 := archsimd.LoadFloat32x4Array((*[4]float32)(o[i : i+4]))
		x1r := archsimd.LoadFloat32x4Array((*[4]float32)(o[overlap-4-i : overlap-i]))
		x1 := x1r.ConcatPermuteScalars(3, 2, 1, 0, x1r)
		w1 := archsimd.LoadFloat32x4Array((*[4]float32)(window[i : i+4]))
		w2 := archsimd.LoadFloat32x4Array((*[4]float32)(windowRev[i : i+4]))
		w2.Mul(x2).Sub(w1.Mul(x1)).StoreArray((*[4]float32)(o[i : i+4]))
		b := w1.Mul(x2).Add(w2.Mul(x1))
		b.ConcatPermuteScalars(3, 2, 1, 0, b).StoreArray((*[4]float32)(o[overlap-4-i : overlap-i]))
	}
	archsimd.ClearAVXUpperBits()
}
