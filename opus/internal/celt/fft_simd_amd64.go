//go:build goexperiment.simd && amd64

// Copyright (c) 2003-2004, Mark Borgerding
// Lots of modifications by Jean-Marc Valin
// Copyright (c) 2005-2007, Xiph.Org Foundation
// Copyright (c) 2008,      Xiph.Org Foundation, CSIRO
//
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met:
//
//   - Redistributions of source code must retain the above copyright notice,
//     this list of conditions and the following disclaimer.
//   - Redistributions in binary form must reproduce the above copyright notice,
//     this list of conditions and the following disclaimer in the
//     documentation and/or other materials provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
// AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
// IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
// ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE
// LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
// CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
// SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
// INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
// CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
// ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
// POSSIBILITY OF SUCH DAMAGE.
//
// This code is originally from Mark Borgerding's KISS-FFT but has been
// heavily modified to better suit Opus.
//
// Ported from libopus 1.5.2 celt/kiss_fft.c (opus_fft_impl and its
// butterflies, float build), vectorized: the kernels compute four
// butterflies at a time with each lane's arithmetic that of the scalar
// code, so their results are bit for bit those of kiss_fft.go.

package celt

import "simd/archsimd"

// simdStage is one radix stage of opusFFTImpl for the vector kernels: the
// radix p, the butterfly span m, the N repeats mm apart, and the twiddles
// tw[q*j*fstride] of butterfly j, q = 1..p-1, each part duplicated for the
// interleaved complex layout: for each q, 2m real parts (two of each) and
// then 2m imaginary parts. c1 and c2 are the stage's constant twiddles
// (epi3 for radix 3, ya and yb for radix 5).
type simdStage struct {
	p, m, N, mm int
	tw          []float32
	c1, c2      kissTwiddleCpx
}

// simdFFTState is the vector FFT's configuration of one kissFFTState, its
// stages in the order opusFFTImpl runs them.
type simdFFTState struct {
	stages  [maxFactors]simdStage
	nstages int
}

// simdFFTs holds the vector configuration of fftState48000x960[shift].
var simdFFTs [len(fftState48000x960)]simdFFTState

// buildSIMDFFT fills simdFFTs (buildSIMDTables).
func buildSIMDFFT() {
	for k := range fftState48000x960 {
		st := &fftState48000x960[k]
		var fstride [maxFactors + 1]int
		shift := max(st.shift, 0)
		fstride[0] = 1
		L := 0
		for {
			p, m := st.factors[2*L], st.factors[2*L+1]
			fstride[L+1] = fstride[L] * p
			L++
			if m == 1 {
				break
			}
		}
		s := &simdFFTs[k]
		for i := L - 1; i >= 0; i-- {
			p, m := st.factors[2*i], st.factors[2*i+1]
			mm := 1
			if i != 0 {
				mm = st.factors[2*i-1]
			}
			fs := fstride[i] << shift
			g := simdStage{p: p, m: m, N: fstride[i], mm: mm}
			if m > 1 && p != 2 {
				g.tw = make([]float32, 4*m*(p-1))
				for q := 1; q < p; q++ {
					t := g.tw[4*m*(q-1):]
					for j := 0; j < m; j++ {
						w := st.twiddles[q*j*fs]
						t[2*j], t[2*j+1] = w.r, w.r
						t[2*m+2*j], t[2*m+2*j+1] = w.i, w.i
					}
				}
			}
			switch p {
			case 3:
				g.c1 = st.twiddles[fs*m]
			case 5:
				g.c1, g.c2 = st.twiddles[fs*m], st.twiddles[fs*2*m]
			}
			s.stages[s.nstages] = g
			s.nstages++
		}
	}
}

func load8(s []float32) archsimd.Float32x8 {
	return archsimd.LoadFloat32x8Array((*[8]float32)(s))
}

func store8(v archsimd.Float32x8, s []float32) {
	v.StoreArray((*[8]float32)(s))
}

// swap8 exchanges the real and imaginary parts of four complex values.
func swap8(v archsimd.Float32x8) archsimd.Float32x8 {
	return v.ConcatPermuteScalarsGrouped(1, 0, 3, 2, v)
}

// cmul8 is C_MUL of four complex values by their twiddles, given as
// duplicated real parts tr and imaginary parts ti: (xr*tr - xi*ti,
// xi*tr + xr*ti), each product rounded.
func cmul8(x, tr, ti archsimd.Float32x8) archsimd.Float32x8 {
	return x.Mul(tr).AddOddSubEven(swap8(x).Mul(ti))
}

// xor8 flips the sign bits of v set in sign: an exact negation of those
// lanes.
func xor8(v archsimd.Float32x8, sign archsimd.Uint32x8) archsimd.Float32x8 {
	return v.ToBits().Xor(sign).BitsToFloat32()
}

// opusFFTAVX2 is opusFFTImpl in 256-bit vectors.
//
//go:noinline
func opusFFTAVX2(s *simdFFTState, fout []float32) {
	for k := 0; k < s.nstages; k++ {
		g := &s.stages[k]
		switch {
		case g.p == 2:
			bfly2AVX2(fout, g.N)
		case g.p == 4 && g.m == 1:
			bfly4m1AVX2(fout, g.N)
		case g.p == 4:
			bfly4AVX2(fout, g)
		case g.p == 3:
			bfly3AVX2(fout, g)
		case g.p == 5:
			bfly5AVX2(fout, g)
		}
	}
	archsimd.ClearAVXUpperBits()
}

// bfly2AVX2 is kfBfly2 (m == 4), one group of four butterflies per step:
// Fout in lo, Fout2 in hi.
func bfly2AVX2(fout []float32, N int) {
	tw := archsimd.BroadcastFloat32x8(0.7071067812)
	one := archsimd.BroadcastFloat32x8(1)
	// mul scales lanes 2, 3, 6 and 7 by tw; neg negates lanes 5 and 7.
	mul := one.IfElse(archsimd.LoadInt32x8Array(&[8]int32{-1, -1, 0, 0, -1, -1, 0, 0}).ToMask(), tw)
	neg := archsimd.LoadUint32x8Array(&[8]uint32{0, 0, 0, 0, 0, 1 << 31, 0, 1 << 31})
	isA := archsimd.LoadInt32x8Array(&[8]int32{0, 0, -1, 0, 0, 0, 0, -1}).ToMask()
	isB := archsimd.LoadInt32x8Array(&[8]int32{0, 0, 0, -1, 0, 0, 0, 0}).ToMask()
	isSw := archsimd.LoadInt32x8Array(&[8]int32{0, 0, 0, 0, -1, -1, 0, 0}).ToMask()
	isC := archsimd.LoadInt32x8Array(&[8]int32{0, 0, 0, 0, 0, 0, -1, 0}).ToMask()
	for i := 0; i < N; i++ {
		f := fout[16*i : 16*i+16]
		lo := load8(f[0:])
		hi := load8(f[8:])
		sw := swap8(hi)
		// The twiddled Fout2 values: hi0; ((h1r + h1i)*tw, (h1i - h1r)*tw);
		// (h2i, -h2r); ((h3i - h3r)*tw, -(h3i + h3r)*tw).
		a := hi.Add(sw)
		t := a.IfElse(isA, hi)
		t = hi.Sub(sw).IfElse(isB, t)
		t = sw.IfElse(isSw, t)
		t = sw.Sub(hi).IfElse(isC, t)
		t = xor8(t, neg).Mul(mul)
		store8(lo.Sub(t), f[8:])
		store8(lo.Add(t), f[0:])
	}
}

// bfly4m1AVX2 is kfBfly4 for m == 1, two butterflies per step.
func bfly4m1AVX2(fout []float32, N int) {
	// Signs of (f0' + s1, f0' - s1) and of (s0r + s1i, s0i - s1r, s0r - s1i,
	// s0i + s1r), per 128-bit half.
	negHi := archsimd.LoadUint32x8Array(&[8]uint32{0, 0, 1 << 31, 1 << 31, 0, 0, 1 << 31, 1 << 31})
	negMid := archsimd.LoadUint32x8Array(&[8]uint32{0, 1 << 31, 1 << 31, 0, 0, 1 << 31, 1 << 31, 0})
	i := 0
	for ; i+2 <= N; i += 2 {
		f := fout[8*i : 8*i+16]
		v1 := load8(f[0:])
		v2 := load8(f[8:])
		// The first and the last two values of both butterflies.
		lo := v1.ConcatPermute128Scalars(0, 2, v2)
		hi := v1.ConcatPermute128Scalars(1, 3, v2)
		u := lo.Add(hi) // (f0', s1): f0 + f2, f1 + f3
		w := lo.Sub(hi) // (s0, s1'): f0 - f2, f1 - f3
		// (f0' + s1, f0' - s1) and (f1'', f3'').
		r1 := u.ConcatPermuteScalarsGrouped(0, 1, 4, 5, u).Add(xor8(u.ConcatPermuteScalarsGrouped(2, 3, 6, 7, u), negHi))
		r2 := w.ConcatPermuteScalarsGrouped(0, 1, 4, 5, w).Add(xor8(w.ConcatPermuteScalarsGrouped(3, 2, 7, 6, w), negMid))
		// (f0'', f1'') and (f2'', f3'') of both butterflies.
		o1 := r1.ConcatPermuteScalarsGrouped(0, 1, 4, 5, r2)
		o2 := r1.ConcatPermuteScalarsGrouped(2, 3, 6, 7, r2)
		store8(o1.ConcatPermute128Scalars(0, 2, o2), f[0:])
		store8(o1.ConcatPermute128Scalars(1, 3, o2), f[8:])
	}
	if i < N {
		archsimd.ClearAVXUpperBits()
		kfBfly4m1(fout[8*i:], N-i)
	}
}

// bfly4AVX2 is kfBfly4 for m > 1, four butterflies per step.
func bfly4AVX2(fout []float32, g *simdStage) {
	m := g.m
	tw := g.tw
	negAll := archsimd.BroadcastUint32x8(1 << 31)
	for i := 0; i < g.N; i++ {
		f := fout[2*i*g.mm:]
		for j := 0; j < 2*m; j += 8 {
			a := load8(f[j:])
			s0 := cmul8(load8(f[j+2*m:]), load8(tw[j:]), load8(tw[2*m+j:]))
			s1 := cmul8(load8(f[j+4*m:]), load8(tw[4*m+j:]), load8(tw[6*m+j:]))
			s2 := cmul8(load8(f[j+6*m:]), load8(tw[8*m+j:]), load8(tw[10*m+j:]))
			s5 := a.Sub(s1)
			a = a.Add(s1)
			s3 := s0.Add(s2)
			s4 := swap8(s0.Sub(s2))
			store8(a.Sub(s3), f[j+4*m:])
			store8(a.Add(s3), f[j:])
			// (s5r + s4i, s5i - s4r) and (s5r - s4i, s5i + s4r).
			store8(s5.AddOddSubEven(xor8(s4, negAll)), f[j+2*m:])
			store8(s5.AddOddSubEven(s4), f[j+6*m:])
		}
	}
}

// bfly3AVX2 is kfBfly3, four butterflies per step.
func bfly3AVX2(fout []float32, g *simdStage) {
	m := g.m
	tw := g.tw
	half := archsimd.BroadcastFloat32x8(0.5)
	epi3i := archsimd.BroadcastFloat32x8(g.c1.i)
	negAll := archsimd.BroadcastUint32x8(1 << 31)
	for i := 0; i < g.N; i++ {
		f := fout[2*i*g.mm:]
		for j := 0; j < 2*m; j += 8 {
			a := load8(f[j:])
			s1 := cmul8(load8(f[j+2*m:]), load8(tw[j:]), load8(tw[2*m+j:]))
			s2 := cmul8(load8(f[j+4*m:]), load8(tw[4*m+j:]), load8(tw[6*m+j:]))
			s3 := s1.Add(s2)
			s0 := swap8(s1.Sub(s2).Mul(epi3i))
			b := a.Sub(s3.Mul(half))
			store8(a.Add(s3), f[j:])
			// (br + s0i, bi - s0r) and (br - s0i, bi + s0r).
			store8(b.AddOddSubEven(xor8(s0, negAll)), f[j+4*m:])
			store8(b.AddOddSubEven(s0), f[j+2*m:])
		}
	}
}

// bfly5AVX2 is kfBfly5, four butterflies per step.
func bfly5AVX2(fout []float32, g *simdStage) {
	m := g.m
	tw := g.tw
	yar := archsimd.BroadcastFloat32x8(g.c1.r)
	yai := archsimd.BroadcastFloat32x8(g.c1.i)
	ybr := archsimd.BroadcastFloat32x8(g.c2.r)
	ybi := archsimd.BroadcastFloat32x8(g.c2.i)
	negOdd := archsimd.LoadUint32x8Array(&[8]uint32{0, 1 << 31, 0, 1 << 31, 0, 1 << 31, 0, 1 << 31})
	odd := archsimd.LoadInt32x8Array(&[8]int32{0, -1, 0, -1, 0, -1, 0, -1}).ToMask()
	for i := 0; i < g.N; i++ {
		f := fout[2*i*g.mm:]
		for u := 0; u < 2*m; u += 8 {
			s0 := load8(f[u:])
			s1 := cmul8(load8(f[u+2*m:]), load8(tw[u:]), load8(tw[2*m+u:]))
			s2 := cmul8(load8(f[u+4*m:]), load8(tw[4*m+u:]), load8(tw[6*m+u:]))
			s3 := cmul8(load8(f[u+6*m:]), load8(tw[8*m+u:]), load8(tw[10*m+u:]))
			s4 := cmul8(load8(f[u+8*m:]), load8(tw[12*m+u:]), load8(tw[14*m+u:]))
			s7 := s1.Add(s4)
			s10 := swap8(s1.Sub(s4))
			s8 := s2.Add(s3)
			s9 := swap8(s2.Sub(s3))
			store8(s0.Add(s7.Add(s8)), f[u:])
			s5 := s0.Add(s7.Mul(yar).Add(s8.Mul(ybr)))
			// (s10i*yai + s9i*ybi, -(s10r*yai + s9r*ybi)).
			s6 := xor8(s10.Mul(yai).Add(s9.Mul(ybi)), negOdd)
			store8(s5.Sub(s6), f[u+2*m:])
			store8(s5.Add(s6), f[u+8*m:])
			s11 := s0.Add(s7.Mul(ybr).Add(s8.Mul(yar)))
			// (s9i*yai - s10i*ybi, s10r*ybi - s9r*yai).
			p := s9.Mul(yai)
			q := s10.Mul(ybi)
			s12 := q.IfElse(odd, p).Sub(p.IfElse(odd, q))
			store8(s11.Add(s12), f[u+4*m:])
			store8(s11.Sub(s12), f[u+6*m:])
		}
	}
}
