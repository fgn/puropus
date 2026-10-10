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
//   * Redistributions of source code must retain the above copyright notice,
//     this list of conditions and the following disclaimer.
//   * Redistributions in binary form must reproduce the above copyright notice,
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
// Ported from libopus 1.5.2 celt/kiss_fft.c, celt/kiss_fft.h and
// celt/_kiss_fft_guts.h (opus_fft_impl and its butterflies, float build).

package celt

// maxFactors is the largest number of radix stages (MAXFACTORS).
const maxFactors = 8

// kissTwiddleCpx is a complex twiddle factor (kiss_twiddle_cpx).
type kissTwiddleCpx struct {
	r, i float32
}

// kissFFTState is the configuration of one FFT size of the static mode
// (kiss_fft_state). factors holds the radix p and the remaining length m of
// each stage; twiddles are shared between sizes, with shift the log2 of the
// stride into them (-1 for the largest size).
type kissFFTState struct {
	nfft     int
	scale    float32
	shift    int
	factors  [2 * maxFactors]int
	bitrev   []int16
	twiddles []kissTwiddleCpx
}

// The FFT works in place on interleaved complex values: fout[2*k] is the
// real and fout[2*k+1] the imaginary part of value k (kiss_fft_cpx). The
// products are rounded to float before they are added, as in libopus's float
// build (C_MUL, S_MUL).

// kfBfly2 is a radix-2 stage, which only follows a radix-4 stage (m == 4)
// in the standard modes (kf_bfly2).
func kfBfly2(fout []float32, m, N int) {
	_ = m
	const tw float32 = 0.7071067812
	for i := 0; i < N; i++ {
		f := fout[16*i : 16*i+16]
		// Fout2 = Fout + 4: f[8:16].
		tr, ti := f[8], f[9]
		f[8], f[9] = f[0]-tr, f[1]-ti
		f[0] += tr
		f[1] += ti

		tr = float32((f[10] + f[11]) * tw)
		ti = float32((f[11] - f[10]) * tw)
		f[10], f[11] = f[2]-tr, f[3]-ti
		f[2] += tr
		f[3] += ti

		tr = f[13]
		ti = -f[12]
		f[12], f[13] = f[4]-tr, f[5]-ti
		f[4] += tr
		f[5] += ti

		tr = float32((f[15] - f[14]) * tw)
		ti = float32(-(f[15] + f[14]) * tw)
		f[14], f[15] = f[6]-tr, f[7]-ti
		f[6] += tr
		f[7] += ti
	}
}

// kfBfly4m1 is a radix-4 stage with m == 1: the degenerate case where all
// the twiddles are 1 (kf_bfly4).
func kfBfly4m1(fout []float32, N int) {
	for i := 0; i < N; i++ {
		f := fout[8*i : 8*i+8]
		s0r, s0i := f[0]-f[4], f[1]-f[5]
		f[0] += f[4]
		f[1] += f[5]
		s1r, s1i := f[2]+f[6], f[3]+f[7]
		f[4], f[5] = f[0]-s1r, f[1]-s1i
		f[0] += s1r
		f[1] += s1i
		s1r, s1i = f[2]-f[6], f[3]-f[7]
		f[2] = s0r + s1i
		f[3] = s0i - s1r
		f[6] = s0r - s1i
		f[7] = s0i + s1r
	}
}

// fftStage is one radix stage of opusFFTImpl: radix p, butterfly span m,
// N repeats mm values apart, and the stage's twiddles tw[q*j*fstride] for
// butterfly j and q = 1..p-1, stored together per butterfly at
// tw[(p-1)*j+q-1] (libopus steps through the shared table with stride
// fstride instead). c1 and c2 are the stage's constant twiddles: epi3 for
// radix 3, ya and yb for radix 5.
type fftStage struct {
	p, m, N, mm int
	tw          []kissTwiddleCpx
	c1, c2      kissTwiddleCpx
}

// fftConfig is the stage list of one kissFFTState, in the order
// opus_fft_impl runs the stages.
type fftConfig struct {
	stages  [maxFactors]fftStage
	nstages int
}

// fftConfigs holds the stages of fftState48000x960[shift] (initTables).
var fftConfigs [len(fftState48000x960)]fftConfig

// buildFFTConfigs fills fftConfigs from the static mode's FFT states
// (initTables).
func buildFFTConfigs() {
	for k := range fftState48000x960 {
		st := &fftState48000x960[k]
		var fstride [maxFactors + 1]int
		// st.shift can be -1.
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
		cfg := &fftConfigs[k]
		for i := L - 1; i >= 0; i-- {
			p, m := st.factors[2*i], st.factors[2*i+1]
			mm := 1
			if i != 0 {
				mm = st.factors[2*i-1]
			}
			fs := fstride[i] << shift
			g := fftStage{p: p, m: m, N: fstride[i], mm: mm}
			if m > 1 && p != 2 {
				g.tw = make([]kissTwiddleCpx, (p-1)*m)
				for j := 0; j < m; j++ {
					for q := 1; q < p; q++ {
						g.tw[(p-1)*j+q-1] = st.twiddles[q*j*fs]
					}
				}
			}
			switch p {
			case 3:
				g.c1 = st.twiddles[fs*m]
			case 5:
				g.c1, g.c2 = st.twiddles[fs*m], st.twiddles[fs*2*m]
			}
			cfg.stages[cfg.nstages] = g
			cfg.nstages++
		}
	}
}

// kfBfly4 is a radix-4 stage with m > 1 (kf_bfly4); m is a multiple of 4.
func kfBfly4(fout []float32, g *fftStage) {
	m := g.m
	tw := g.tw[:3*m]
	for i := 0; i < g.N; i++ {
		f := fout[2*i*g.mm : 2*i*g.mm+8*m]
		fa, fb, fc, fd := f[:2*m], f[2*m:4*m], f[4*m:6*m], f[6*m:]
		for j := 0; j < m; j++ {
			a := (*[2]float32)(fa[2*j:])
			b := (*[2]float32)(fb[2*j:])
			c := (*[2]float32)(fc[2*j:])
			d := (*[2]float32)(fd[2*j:])
			t := (*[3]kissTwiddleCpx)(tw[3*j:])
			t1, t2, t3 := t[0], t[1], t[2]
			s0r := float32(b[0]*t1.r) - float32(b[1]*t1.i)
			s0i := float32(b[0]*t1.i) + float32(b[1]*t1.r)
			s1r := float32(c[0]*t2.r) - float32(c[1]*t2.i)
			s1i := float32(c[0]*t2.i) + float32(c[1]*t2.r)
			s2r := float32(d[0]*t3.r) - float32(d[1]*t3.i)
			s2i := float32(d[0]*t3.i) + float32(d[1]*t3.r)
			s5r, s5i := a[0]-s1r, a[1]-s1i
			a0 := a[0] + s1r
			a1 := a[1] + s1i
			s3r, s3i := s0r+s2r, s0i+s2i
			s4r, s4i := s0r-s2r, s0i-s2i
			c[0], c[1] = a0-s3r, a1-s3i
			a[0], a[1] = a0+s3r, a1+s3i
			b[0] = s5r + s4i
			b[1] = s5i - s4r
			d[0] = s5r - s4i
			d[1] = s5i + s4r
		}
	}
}

// kfBfly3 is a radix-3 stage (kf_bfly3); m is a multiple of 4.
func kfBfly3(fout []float32, g *fftStage) {
	m := g.m
	tw := g.tw[:2*m]
	epi3 := g.c1
	for i := 0; i < g.N; i++ {
		f := fout[2*i*g.mm : 2*i*g.mm+6*m]
		fa, fb, fc := f[:2*m], f[2*m:4*m], f[4*m:]
		for k := 0; k < m; k++ {
			a := (*[2]float32)(fa[2*k:])
			b := (*[2]float32)(fb[2*k:])
			c := (*[2]float32)(fc[2*k:])
			t := (*[2]kissTwiddleCpx)(tw[2*k:])
			t1, t2 := t[0], t[1]
			s1r := float32(b[0]*t1.r) - float32(b[1]*t1.i)
			s1i := float32(b[0]*t1.i) + float32(b[1]*t1.r)
			s2r := float32(c[0]*t2.r) - float32(c[1]*t2.i)
			s2i := float32(c[0]*t2.i) + float32(c[1]*t2.r)
			s3r, s3i := s1r+s2r, s1i+s2i
			s0r, s0i := s1r-s2r, s1i-s2i
			br := a[0] - float32(s3r*0.5)
			bi := a[1] - float32(s3i*0.5)
			s0r = float32(s0r * epi3.i)
			s0i = float32(s0i * epi3.i)
			a[0] += s3r
			a[1] += s3i
			c[0] = br + s0i
			c[1] = bi - s0r
			b[0] = br - s0i
			b[1] = bi + s0r
		}
	}
}

// kfBfly5 is a radix-5 stage (kf_bfly5); m is a multiple of 4.
func kfBfly5(fout []float32, g *fftStage) {
	m := g.m
	tw := g.tw[:4*m]
	ya, yb := g.c1, g.c2
	for i := 0; i < g.N; i++ {
		f := fout[2*i*g.mm : 2*i*g.mm+10*m]
		q0, q1, q2, q3, q4 := f[:2*m], f[2*m:4*m], f[4*m:6*m], f[6*m:8*m], f[8*m:]
		for u := 0; u < m; u++ {
			f0 := (*[2]float32)(q0[2*u:])
			f1 := (*[2]float32)(q1[2*u:])
			f2 := (*[2]float32)(q2[2*u:])
			f3 := (*[2]float32)(q3[2*u:])
			f4 := (*[2]float32)(q4[2*u:])
			t := (*[4]kissTwiddleCpx)(tw[4*u:])
			s0r, s0i := f0[0], f0[1]
			s1r := float32(f1[0]*t[0].r) - float32(f1[1]*t[0].i)
			s1i := float32(f1[0]*t[0].i) + float32(f1[1]*t[0].r)
			s2r := float32(f2[0]*t[1].r) - float32(f2[1]*t[1].i)
			s2i := float32(f2[0]*t[1].i) + float32(f2[1]*t[1].r)
			s3r := float32(f3[0]*t[2].r) - float32(f3[1]*t[2].i)
			s3i := float32(f3[0]*t[2].i) + float32(f3[1]*t[2].r)
			s4r := float32(f4[0]*t[3].r) - float32(f4[1]*t[3].i)
			s4i := float32(f4[0]*t[3].i) + float32(f4[1]*t[3].r)

			s7r, s7i := s1r+s4r, s1i+s4i
			s10r, s10i := s1r-s4r, s1i-s4i
			s8r, s8i := s2r+s3r, s2i+s3i
			s9r, s9i := s2r-s3r, s2i-s3i

			f0[0] = s0r + (s7r + s8r)
			f0[1] = s0i + (s7i + s8i)

			s5r := s0r + (float32(s7r*ya.r) + float32(s8r*yb.r))
			s5i := s0i + (float32(s7i*ya.r) + float32(s8i*yb.r))

			s6r := float32(s10i*ya.i) + float32(s9i*yb.i)
			s6i := -(float32(s10r*ya.i) + float32(s9r*yb.i))

			f1[0], f1[1] = s5r-s6r, s5i-s6i
			f4[0], f4[1] = s5r+s6r, s5i+s6i

			s11r := s0r + (float32(s7r*yb.r) + float32(s8r*ya.r))
			s11i := s0i + (float32(s7i*yb.r) + float32(s8i*ya.r))
			s12r := float32(s9i*ya.i) - float32(s10i*yb.i)
			s12i := float32(s10r*yb.i) - float32(s9r*ya.i)

			f2[0], f2[1] = s11r+s12r, s11i+s12i
			f3[0], f3[1] = s11r-s12r, s11i-s12i
		}
	}
}

// opusFFTImpl computes the FFT of the configuration's size in place, on
// input already in bit-reversed order (opus_fft_impl).
func opusFFTImpl(cfg *fftConfig, fout []float32) {
	for k := 0; k < cfg.nstages; k++ {
		g := &cfg.stages[k]
		switch {
		case g.p == 2:
			kfBfly2(fout, g.m, g.N)
		case g.p == 4 && g.m == 1:
			kfBfly4m1(fout, g.N)
		case g.p == 4:
			kfBfly4(fout, g)
		case g.p == 3:
			kfBfly3(fout, g)
		case g.p == 5:
			kfBfly5(fout, g)
		}
	}
}
