//go:build goexperiment.simd && amd64

// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2009 Xiph.Org Foundation
// Copyright (c) 2008-2009 Gregory Maxwell
// Written by Jean-Marc Valin and Gregory Maxwell
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
// Ported from libopus 1.5.2 celt/bands.c (denormalise_bands, haar1 and
// quant_band's folding copy) and celt/vq.c (normalise_residual),
// vectorized: each lane computes one coefficient with the scalar code's
// arithmetic, so the results are bit for bit those of bands.go and vq.go.

package celt

import "simd/archsimd"

// scaleAVX2 sets dst[i] = g*src[i]: the values that do not fill a vector
// first, in scalar code, then eight at a time.
//
//go:noinline
func scaleAVX2(dst, src []float32, g float32) {
	src = src[:len(dst)]
	n := len(dst) &^ 7
	for i := n; i < len(dst); i++ {
		dst[i] = g * src[i]
	}
	v := archsimd.BroadcastFloat32x8(g)
	for i := 0; i < n; i += 8 {
		store8(v.Mul(load8(src[i:])), dst[i:])
	}
	archsimd.ClearAVXUpperBits()
}

// normaliseAVX2 sets X[i] = g*float32(iy[i]) (the scaling of
// normaliseResidual): the values that do not fill a vector first, in
// scalar code, then eight at a time.
//
//go:noinline
func normaliseAVX2(X []float32, iy []int32, g float32) {
	iy = iy[:len(X)]
	n := len(X) &^ 7
	for i := n; i < len(X); i++ {
		X[i] = g * float32(iy[i])
	}
	v := archsimd.BroadcastFloat32x8(g)
	for i := 0; i < n; i += 8 {
		y := archsimd.LoadInt32x8Array((*[8]int32)(iy[i : i+8])).ConvertToFloat32()
		store8(v.Mul(y), X[i:])
	}
	archsimd.ClearAVXUpperBits()
}

// haar1AVX2 is haar1 for stride 1 or 2, on the N0*stride values of X: the
// pairs (a, b) of a vector are scaled, swapped, and summed (a+b) in a's
// lanes and differenced (a-b) in b's. Groups of eight values that do not
// fill a vector go first, in scalar code.
//
//go:noinline
func haar1AVX2(X []float32, stride int) {
	const s = 0.70710678
	n := len(X) &^ 7
	for j := n; j+stride < len(X); j += 2 * stride {
		for i := j; i < j+stride; i++ {
			tmp1 := float32(s * X[i])
			tmp2 := float32(s * X[i+stride])
			X[i] = tmp1 + tmp2
			X[i+stride] = tmp1 - tmp2
		}
	}
	c := archsimd.BroadcastFloat32x8(s)
	if stride == 1 {
		odd := archsimd.LoadInt32x8Array(&[8]int32{0, -1, 0, -1, 0, -1, 0, -1}).ToMask()
		for j := 0; j < n; j += 8 {
			v := load8(X[j:]).Mul(c)
			sw := v.ConcatPermuteScalarsGrouped(1, 0, 3, 2, v)
			store8(sw.Sub(v).IfElse(odd, v.Add(sw)), X[j:])
		}
	} else {
		odd := archsimd.LoadInt32x8Array(&[8]int32{0, 0, -1, -1, 0, 0, -1, -1}).ToMask()
		for j := 0; j < n; j += 8 {
			v := load8(X[j:]).Mul(c)
			sw := v.ConcatPermuteScalarsGrouped(2, 3, 0, 1, v)
			store8(sw.Sub(v).IfElse(odd, v.Add(sw)), X[j:])
		}
	}
	archsimd.ClearAVXUpperBits()
}

// denormaliseAVX2 is the band loop of denormaliseBands for M == 8:
// freq[j] = X[j]*gains[i] for the coefficients j of the bands from start to
// end.
//
//go:noinline
func denormaliseAVX2(X, freq []float32, gains *[maxBands]float32, eBands []int, start, end int) {
	for i := start; i < end; i++ {
		lo, hi := 8*eBands[i], 8*eBands[i+1]
		g := archsimd.BroadcastFloat32x8(gains[i])
		x := X[lo:hi]
		y := freq[lo:hi]
		for j := 0; j+8 <= len(x); j += 8 {
			store8(load8(x[j:]).Mul(g), y[j:])
		}
	}
	archsimd.ClearAVXUpperBits()
}
