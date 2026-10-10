// Copyright (c) 2009-2010 Xiph.Org Foundation
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
// Ported from libopus 1.5.2 celt/celt_lpc.c and celt/celt_lpc.h (generic C
// paths).

package celt

// lpcOrder is the order of the PLC's LPC filter (CELT_LPC_ORDER).
const lpcOrder = 24

// celtLpc computes the p LPC coefficients lpc[0:p] from the autocorrelation
// ac[0:p+1] with the Levinson-Durbin recursion (_celt_lpc, float build).
func celtLpc(lpc []float32, ac []float32, p int) {
	errv := ac[0]
	lpc = lpc[:p]
	clear(lpc)
	if ac[0] > 1e-10 {
		for i := 0; i < p; i++ {
			// Sum up this iteration's reflection coefficient.
			var rr float32
			for j := 0; j < i; j++ {
				rr += float32(lpc[j] * ac[i-j])
			}
			rr += ac[i+1]
			r := -(rr / errv)
			// Update LPC coefficients and total error.
			lpc[i] = r
			for j := 0; j < (i+1)>>1; j++ {
				tmp1 := lpc[j]
				tmp2 := lpc[i-1-j]
				lpc[j] = tmp1 + float32(r*tmp2)
				lpc[i-1-j] = tmp2 + float32(r*tmp1)
			}
			errv -= float32(float32(r*r) * errv)
			// Bail out once we get 30 dB gain.
			if errv <= float32(0.001*ac[0]) {
				break
			}
		}
	}
}

// celtFir filters x with the FIR num of order ord into y[0:n]
// (celt_fir_c): y[i] = x[ord+i] + sum(num[j]*x[ord+i-j-1]). x holds ord
// samples of history before the n input samples, so x[ord+i] is libopus's
// x[i]. x and y must not overlap.
func celtFir(x []float32, num []float32, y []float32, n, ord int) {
	var rnum [lpcOrder]float32
	for i := 0; i < ord; i++ {
		rnum[i] = num[ord-i-1]
	}
	i := 0
	for ; i < n-3; i += 4 {
		var sum [4]float32
		sum[0] = x[ord+i]
		sum[1] = x[ord+i+1]
		sum[2] = x[ord+i+2]
		sum[3] = x[ord+i+3]
		xcorrKernel(rnum[:ord], x[i:], &sum, ord)
		y[i] = sum[0]
		y[i+1] = sum[1]
		y[i+2] = sum[2]
		y[i+3] = sum[3]
	}
	for ; i < n; i++ {
		sum := x[ord+i]
		for j := 0; j < ord; j++ {
			sum += float32(rnum[j] * x[i+j])
		}
		y[i] = sum
	}
}

// celtIirMaxLen is the longest celtIir input: the PLC filters a frame plus
// the overlap.
const celtIirMaxLen = maxFrameSize + maxOverlap

// celtIir filters x[0:n] with the all-pole filter 1/(1+sum(den[j]*z^-j-1))
// of order ord (a multiple of 4) into y[0:n] (celt_iir, the unrolled
// version). mem holds the last ord outputs, most recent first, and is
// updated. y may be x. n may be at most celtIirMaxLen.
func celtIir(x []float32, den []float32, y []float32, n, ord int, mem []float32) {
	var rden [lpcOrder]float32
	var yy [celtIirMaxLen + lpcOrder]float32
	for i := 0; i < ord; i++ {
		rden[i] = den[ord-i-1]
	}
	i := 0
	for ; i < ord; i++ {
		yy[i] = -mem[ord-i-1]
	}
	for ; i < n+ord; i++ {
		yy[i] = 0
	}
	for i = 0; i < n-3; i += 4 {
		// Unroll by 4 as if it were an FIR filter.
		var sum [4]float32
		sum[0] = x[i]
		sum[1] = x[i+1]
		sum[2] = x[i+2]
		sum[3] = x[i+3]
		xcorrKernel(rden[:ord], yy[i:], &sum, ord)

		// Patch up the result to compensate for the fact that this is an
		// IIR.
		yy[i+ord] = -sum[0]
		y[i] = sum[0]
		sum[1] += float32(yy[i+ord] * den[0])
		yy[i+ord+1] = -sum[1]
		y[i+1] = sum[1]
		sum[2] += float32(yy[i+ord+1] * den[0])
		sum[2] += float32(yy[i+ord] * den[1])
		yy[i+ord+2] = -sum[2]
		y[i+2] = sum[2]

		sum[3] += float32(yy[i+ord+2] * den[0])
		sum[3] += float32(yy[i+ord+1] * den[1])
		sum[3] += float32(yy[i+ord] * den[2])
		yy[i+ord+3] = -sum[3]
		y[i+3] = sum[3]
	}
	for ; i < n; i++ {
		sum := x[i]
		for j := 0; j < ord; j++ {
			sum -= float32(rden[j] * yy[i+j])
		}
		yy[i+ord] = sum
		y[i] = sum
	}
	for i = 0; i < ord; i++ {
		mem[i] = y[n-i-1]
	}
}

// celtAutocorrMaxLen is the longest celtAutocorr input with a window: the
// PLC's MAX_PERIOD samples.
const celtAutocorrMaxLen = maxPeriod

// celtAutocorr computes the autocorrelation ac[0:lag+1] of x[0:n], first
// applying window to overlap samples at both ends when overlap is nonzero
// (_celt_autocorr, float build, which always returns a shift of 0). n may
// be at most celtAutocorrMaxLen when overlap is nonzero.
func celtAutocorr(x []float32, ac []float32, window []float32, overlap, lag, n int) {
	var xx [celtAutocorrMaxLen]float32
	fastN := n - lag
	xptr := x[:n]
	if overlap != 0 {
		copy(xx[:n], x[:n])
		for i := 0; i < overlap; i++ {
			xx[i] = x[i] * window[i]
			xx[n-i-1] = x[n-i-1] * window[i]
		}
		xptr = xx[:n]
	}
	celtPitchXcorr(xptr, xptr, ac, fastN, lag+1)
	for k := 0; k <= lag; k++ {
		var d float32
		for i := k + fastN; i < n; i++ {
			d += float32(xptr[i] * xptr[i-k])
		}
		ac[k] += d
	}
}
