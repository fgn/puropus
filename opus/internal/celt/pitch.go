// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2009 Xiph.Org Foundation
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
// Ported from libopus 1.5.2 celt/pitch.c and celt/pitch.h (what the decoder's
// packet loss concealment uses; generic C paths).

package celt

// The float build's MAC16_16(c, a, b) is c + a*b; the product is rounded to
// float before the addition, as libopus's generic C code computes it.

// xcorrKernel adds to sum[k] the correlation of x[0:n] with y[k:k+n] for k =
// 0..3 (xcorr_kernel_c). y must hold n+3 values; n must be at least 3.
func xcorrKernel(x, y []float32, sum *[4]float32, n int) {
	x = x[:n]
	y = y[:n+3]
	y0 := y[0]
	y1 := y[1]
	y2 := y[2]
	var y3 float32
	yi := 3
	xi := 0
	j := 0
	for ; j < n-3; j += 4 {
		tmp := x[xi]
		xi++
		y3 = y[yi]
		yi++
		sum[0] += float32(tmp * y0)
		sum[1] += float32(tmp * y1)
		sum[2] += float32(tmp * y2)
		sum[3] += float32(tmp * y3)
		tmp = x[xi]
		xi++
		y0 = y[yi]
		yi++
		sum[0] += float32(tmp * y1)
		sum[1] += float32(tmp * y2)
		sum[2] += float32(tmp * y3)
		sum[3] += float32(tmp * y0)
		tmp = x[xi]
		xi++
		y1 = y[yi]
		yi++
		sum[0] += float32(tmp * y2)
		sum[1] += float32(tmp * y3)
		sum[2] += float32(tmp * y0)
		sum[3] += float32(tmp * y1)
		tmp = x[xi]
		xi++
		y2 = y[yi]
		yi++
		sum[0] += float32(tmp * y3)
		sum[1] += float32(tmp * y0)
		sum[2] += float32(tmp * y1)
		sum[3] += float32(tmp * y2)
	}
	if j < n {
		j++
		tmp := x[xi]
		xi++
		y3 = y[yi]
		yi++
		sum[0] += float32(tmp * y0)
		sum[1] += float32(tmp * y1)
		sum[2] += float32(tmp * y2)
		sum[3] += float32(tmp * y3)
	} else {
		j++
	}
	if j < n {
		j++
		tmp := x[xi]
		xi++
		y0 = y[yi]
		yi++
		sum[0] += float32(tmp * y1)
		sum[1] += float32(tmp * y2)
		sum[2] += float32(tmp * y3)
		sum[3] += float32(tmp * y0)
	} else {
		j++
	}
	if j < n {
		tmp := x[xi]
		y1 = y[yi]
		sum[0] += float32(tmp * y2)
		sum[1] += float32(tmp * y3)
		sum[2] += float32(tmp * y0)
		sum[3] += float32(tmp * y1)
	}
}

// dualInnerProd returns the inner products of x with y01 and with y02 over n
// values (dual_inner_prod_c).
func dualInnerProd(x, y01, y02 []float32, n int) (xy1, xy2 float32) {
	x = x[:n]
	y01 = y01[:n]
	y02 = y02[:n]
	var xy01, xy02 float32
	for i := range x {
		xy01 += float32(x[i] * y01[i])
		xy02 += float32(x[i] * y02[i])
	}
	return xy01, xy02
}

// celtInnerProd returns the inner product of x and y over n values
// (celt_inner_prod_c).
func celtInnerProd(x, y []float32, n int) float32 {
	x = x[:n]
	y = y[:n]
	var xy float32
	for i := range x {
		xy += float32(x[i] * y[i])
	}
	return xy
}

// findBestPitch finds the two lags with the best normalized correlation in
// xcorr[0:maxPitch], where y holds the signal for the energies
// (find_best_pitch, float build).
func findBestPitch(xcorr, y []float32, n, maxPitch int, bestPitch *[2]int) {
	Syy := float32(1)
	var bestNum, bestDen [2]float32
	bestNum[0] = -1
	bestNum[1] = -1
	bestDen[0] = 0
	bestDen[1] = 0
	bestPitch[0] = 0
	bestPitch[1] = 1
	for j := 0; j < n; j++ {
		Syy += float32(y[j] * y[j])
	}
	for i := 0; i < maxPitch; i++ {
		if xcorr[i] > 0 {
			xcorr16 := xcorr[i]
			// Considering the range of xcorr16, this should avoid both
			// underflows and overflows (inf) when squaring xcorr16.
			xcorr16 *= 1e-12
			num := xcorr16 * xcorr16
			if num*bestDen[1] > bestNum[1]*Syy {
				if num*bestDen[0] > bestNum[0]*Syy {
					bestNum[1] = bestNum[0]
					bestDen[1] = bestDen[0]
					bestPitch[1] = bestPitch[0]
					bestNum[0] = num
					bestDen[0] = Syy
					bestPitch[0] = i
				} else {
					bestNum[1] = num
					bestDen[1] = Syy
					bestPitch[1] = i
				}
			}
		}
		Syy += float32(y[i+n]*y[i+n]) - float32(y[i]*y[i])
		Syy = max32(1, Syy)
	}
}

// celtFir5 filters x[0:n] in place with the 5-tap FIR num (celt_fir5).
func celtFir5(x []float32, num *[5]float32, n int) {
	num0 := num[0]
	num1 := num[1]
	num2 := num[2]
	num3 := num[3]
	num4 := num[4]
	var mem0, mem1, mem2, mem3, mem4 float32
	x = x[:n]
	for i := range x {
		sum := x[i]
		sum += float32(num0 * mem0)
		sum += float32(num1 * mem1)
		sum += float32(num2 * mem2)
		sum += float32(num3 * mem3)
		sum += float32(num4 * mem4)
		mem4 = mem3
		mem3 = mem2
		mem2 = mem1
		mem1 = mem0
		mem0 = x[i]
		x[i] = sum
	}
}

// pitchDownsample low-passes and decimates by 2 the sum of the C channels
// x[c][0:n] into xLp[0:n/2], then whitens it with a 4th-order LPC and a
// zero (pitch_downsample, float build).
func pitchDownsample(x [2][]float32, xLp []float32, n, C int) {
	var ac [5]float32
	tmp := q15One
	var lpc [4]float32
	var lpc2 [5]float32
	const c1 float32 = 0.8
	x0 := x[0][:n]
	for i := 1; i < n>>1; i++ {
		xLp[i] = float32(0.25*x0[2*i-1]) + float32(0.25*x0[2*i+1]) + float32(0.5*x0[2*i])
	}
	xLp[0] = float32(0.25*x0[1]) + float32(0.5*x0[0])
	if C == 2 {
		x1 := x[1][:n]
		for i := 1; i < n>>1; i++ {
			xLp[i] += float32(0.25*x1[2*i-1]) + float32(0.25*x1[2*i+1]) + float32(0.5*x1[2*i])
		}
		xLp[0] += float32(0.25*x1[1]) + float32(0.5*x1[0])
	}
	celtAutocorr(xLp, ac[:], nil, 0, 4, n>>1)

	// Noise floor -40 dB.
	ac[0] *= 1.0001
	// Lag windowing.
	for i := 1; i <= 4; i++ {
		// ac[i] *= exp(-.5*(2*M_PI*.002*i)*(2*M_PI*.002*i));
		ac[i] -= float32(ac[i] * float32(0.008*float32(i)) * float32(0.008*float32(i)))
	}

	celtLpc(lpc[:], ac[:], 4)
	for i := 0; i < 4; i++ {
		tmp = 0.9 * tmp
		lpc[i] *= tmp
	}
	// Add a zero.
	lpc2[0] = lpc[0] + 0.8
	lpc2[1] = lpc[1] + float32(c1*lpc[0])
	lpc2[2] = lpc[2] + float32(c1*lpc[1])
	lpc2[3] = lpc[3] + float32(c1*lpc[2])
	lpc2[4] = c1 * lpc[3]
	celtFir5(xLp, &lpc2, n>>1)
}

// celtPitchXcorr computes xcorr[i], the correlation of x[0:n] with
// y[i:i+n], for i = 0..maxPitch-1 (celt_pitch_xcorr_c, the unrolled
// version).
func celtPitchXcorr(x, y, xcorr []float32, n, maxPitch int) {
	i := 0
	for ; i < maxPitch-3; i += 4 {
		var sum [4]float32
		xcorrKernel(x, y[i:], &sum, n)
		xcorr[i] = sum[0]
		xcorr[i+1] = sum[1]
		xcorr[i+2] = sum[2]
		xcorr[i+3] = sum[3]
	}
	// In case maxPitch isn't a multiple of 4, do non-unrolled version.
	for ; i < maxPitch; i++ {
		xcorr[i] = celtInnerProd(x, y[i:], n)
	}
}

// Limits of pitchSearch's work buffers: the PLC searches a 1328-sample
// window (DECODE_BUFFER_SIZE-PLC_PITCH_LAG_MAX) over 620 lags
// (PLC_PITCH_LAG_MAX-PLC_PITCH_LAG_MIN).
const (
	pitchSearchMaxLen   = decodeBufferSize - plcPitchLagMax
	pitchSearchMaxPitch = plcPitchLagMax - plcPitchLagMin
)

// pitchSearch searches the pitch lag of xLp[0:n/2] (decimated by 2 by
// pitchDownsample) in y over maxPitch lags at the 48 kHz rate, first with
// another 2x decimation, then refining (pitch_search, float build). n may be
// at most pitchSearchMaxLen and maxPitch at most pitchSearchMaxPitch.
func pitchSearch(xLp, y []float32, n, maxPitch int) int {
	var bestPitch [2]int
	var xLp4 [pitchSearchMaxLen >> 2]float32
	var yLp4 [(pitchSearchMaxLen + pitchSearchMaxPitch) >> 2]float32
	var xcorr [pitchSearchMaxPitch >> 1]float32
	lag := n + maxPitch

	// Downsample by 2 again.
	for j := 0; j < n>>2; j++ {
		xLp4[j] = xLp[2*j]
	}
	for j := 0; j < lag>>2; j++ {
		yLp4[j] = y[2*j]
	}

	// Coarse search with 4x decimation.
	celtPitchXcorr(xLp4[:n>>2], yLp4[:lag>>2], xcorr[:], n>>2, maxPitch>>2)
	findBestPitch(xcorr[:], yLp4[:], n>>2, maxPitch>>2, &bestPitch)

	// Finer search with 2x decimation.
	for i := 0; i < maxPitch>>1; i++ {
		xcorr[i] = 0
		if abs(i-2*bestPitch[0]) > 2 && abs(i-2*bestPitch[1]) > 2 {
			continue
		}
		sum := celtInnerProd(xLp, y[i:], n>>1)
		xcorr[i] = max32(-1, sum)
	}
	findBestPitch(xcorr[:], y, n>>1, maxPitch>>1, &bestPitch)

	// Refine by pseudo-interpolation.
	offset := 0
	if bestPitch[0] > 0 && bestPitch[0] < (maxPitch>>1)-1 {
		a := xcorr[bestPitch[0]-1]
		b := xcorr[bestPitch[0]]
		c := xcorr[bestPitch[0]+1]
		if c-a > 0.7*(b-a) {
			offset = 1
		} else if a-c > 0.7*(b-c) {
			offset = -1
		}
	}
	return 2*bestPitch[0] - offset
}

// abs is the C library's abs for ints.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
