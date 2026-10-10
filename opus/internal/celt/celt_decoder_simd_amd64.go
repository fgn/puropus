//go:build goexperiment.simd && amd64

// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2010 Xiph.Org Foundation
// Copyright (c) 2008 Gregory Maxwell
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
// Ported from libopus 1.5.2 celt/celt_decoder.c and celt/celt.c (the
// stereo post-filter fused with the de-emphasis, see
// postfilterDeemphasisStereo): the comb filter computes four outputs at a
// time in 128-bit vectors, each lane with the scalar code's arithmetic, so
// the results are bit for bit those of celt_decoder.go. 128-bit operations
// leave the upper halves of the vector registers clean, so the
// de-emphasis recurrences run in scalar code in the same loop. (In mono
// the recurrence alone limits the fused loop and the scalar comb filter
// costs nothing extra.)

package celt

import "simd/archsimd"

func load4(s []float32) archsimd.Float32x4 {
	return archsimd.LoadFloat32x4Array((*[4]float32)(s))
}

// combConst4 is the constant comb filter for the four samples y[0:4], with
// h[q] = x[j-T1-2+q] for q = 0..6.
func combConst4(y []float32, h []float32, g10, g11, g12 archsimd.Float32x4) archsimd.Float32x4 {
	return load4(y).
		Add(g10.Mul(load4(h[2:]))).
		Add(g11.Mul(load4(h[3:]).Add(load4(h[1:])))).
		Add(g12.Mul(load4(h[4:]).Add(load4(h[0:]))))
}

// combFade4 is the comb filter's cross-fade for the four samples y[0:4],
// with window w[0:4], g[q] = x[j-T0-2+q] and h[q] = x[j-T1-2+q].
func combFade4(y, w, g, h []float32, g00, g01, g02, g10, g11, g12, one archsimd.Float32x4) archsimd.Float32x4 {
	wv := load4(w)
	f := wv.Mul(wv)
	omf := one.Sub(f)
	return load4(y).
		Add(omf.Mul(g00).Mul(load4(g[2:]))).
		Add(omf.Mul(g01).Mul(load4(g[3:]).Add(load4(g[1:])))).
		Add(omf.Mul(g02).Mul(load4(g[4:]).Add(load4(g[0:])))).
		Add(f.Mul(g10).Mul(load4(h[2:]))).
		Add(f.Mul(g11).Mul(load4(h[3:]).Add(load4(h[1:])))).
		Add(f.Mul(g12).Mul(load4(h[4:]).Add(load4(h[0:]))))
}

// postfilterDeemphasisStereoAVX2 is postfilterDeemphasisStereo with the
// comb filter four samples at a time per channel; samples that do not fill
// four, and the spans the filter leaves alone, take the scalar loop.
//
//go:noinline
func postfilterDeemphasisStereoAVX2(x0, x1 []float32, xi int, spans []combSpan, window, pcm []float32, coef float32,
	mem []float32,
) {
	one := archsimd.BroadcastFloat32x4(q15One)
	for k := range spans {
		sp := &spans[k]
		y0 := x0[xi+sp.start : xi+sp.end]
		y1 := x1[xi+sp.start : xi+sp.end]
		y1 = y1[:len(y0)]
		out := pcm[2*sp.start : 2*sp.end]
		out = out[:2*len(y0)]
		n := 0
		if sp.kind != combKeep {
			m0, m1 := mem[0], mem[1]
			v10 := archsimd.BroadcastFloat32x4(sp.g10)
			v11 := archsimd.BroadcastFloat32x4(sp.g11)
			v12 := archsimd.BroadcastFloat32x4(sp.g12)
			v00 := archsimd.BroadcastFloat32x4(sp.g00)
			v01 := archsimd.BroadcastFloat32x4(sp.g01)
			v02 := archsimd.BroadcastFloat32x4(sp.g02)
			a := xi + sp.start - sp.T0
			b := xi + sp.start - sp.T1
			for ; n+4 <= len(y0); n += 4 {
				var v, u archsimd.Float32x4
				if sp.kind == combConst {
					v = combConst4(y0[n:], x0[b+n-2:b+n+6], v10, v11, v12)
					u = combConst4(y1[n:], x1[b+n-2:b+n+6], v10, v11, v12)
				} else {
					v = combFade4(y0[n:], window[n:], x0[a+n-2:a+n+6], x0[b+n-2:b+n+6], v00, v01, v02, v10, v11, v12, one)
					u = combFade4(y1[n:], window[n:], x1[a+n-2:a+n+6], x1[b+n-2:b+n+6], v00, v01, v02, v10, v11, v12, one)
				}
				yy0 := (*[4]float32)(y0[n : n+4])
				yy1 := (*[4]float32)(y1[n : n+4])
				v.StoreArray(yy0)
				u.StoreArray(yy1)
				o := (*[8]float32)(out[2*n : 2*n+8])
				for q := range yy0 {
					tmp0 := yy0[q] + verySmall + m0
					tmp1 := yy1[q] + verySmall + m1
					m0 = float32(coef * tmp0)
					m1 = float32(coef * tmp1)
					o[2*q] = tmp0 * (1 / celtSigScale)
					o[2*q+1] = tmp1 * (1 / celtSigScale)
				}
			}
			mem[0], mem[1] = m0, m1
		}
		postfilterSpanStereo(x0, x1, xi, sp, n, window, pcm, coef, mem)
	}
	archsimd.ClearAVXUpperBits()
}
