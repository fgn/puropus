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
// Ported from libopus 1.5.2 celt/mdct.c and celt/mdct.h (clt_mdct_backward_c).

package celt

// maxLMShift is the static mode's maxLM: the MDCT of a short block has
// 2*maxFrameSize >> maxLMShift samples.
const maxLMShift = 3

// mdctLookup holds the MDCT of the static mode for its four sizes
// (mdct_lookup): n is the largest size, kfft the FFT for each shift and trig
// the twiddles of all sizes, n/2 values for size n followed by those of the
// next smaller size.
type mdctLookup struct {
	n        int
	maxshift int
	kfft     [4]*kissFFTState
	trig     []float32
}

// cltMdctBackward computes an inverse MDCT of size l.n>>shift and performs
// the weighted overlap-add with the window (clt_mdct_backward_c). It reads
// in[0], in[stride], ..., in[stride*(n/2-1)], where n is the MDCT size. out
// holds the overlap/2 samples of history the previous call left at its
// start; the call writes out[0:n/2+overlap/2] (no scaling; the overlap-add
// scales by 1/2 implicitly).
func (l *mdctLookup) cltMdctBackward(in []float32, out []float32, window []float32, overlap, shift, stride int) {
	if useAVX2 && overlap == maxOverlap && len(window) == maxOverlap {
		if stride == 1 {
			mdctBackwardAVX2(l, in, out, window, shift)
			return
		}
		// The short blocks of a transient frame: gather the block's
		// coefficients first.
		var block [2 * maxFrameSize >> maxLMShift >> 1]float32
		if n2 := l.n >> shift >> 1; n2 <= len(block) {
			for j := range block[:n2] {
				block[j] = in[stride*j]
			}
			mdctBackwardAVX2(l, block[:n2], out, window, shift)
			return
		}
	}
	N := l.n
	trig := l.trig
	for i := 0; i < shift; i++ {
		N >>= 1
		trig = trig[N:]
	}
	N2 := N >> 1
	N4 := N >> 2
	t := trig[:N2]

	// Pre-rotate. t0 and t1 are the halves of the twiddles; the views
	// below only let the compiler drop bounds checks.
	t0, t1 := t[:N4], t[N4:N2]
	t1 = t1[:len(t0)]
	yp := out[overlap>>1 : overlap>>1+N2]
	bitrev := l.kfft[shift].bitrev[:N4]
	if stride == 1 {
		x := in[:N2]
		for i, r := range bitrev {
			x1 := x[2*i]
			x2 := x[N2-1-2*i]
			yr := float32(x2*t0[i]) + float32(x1*t1[i])
			yi := float32(x1*t0[i]) - float32(x2*t1[i])
			// We swap real and imag because we use an FFT instead of an
			// IFFT. Storing the pre-rotation directly in the bitrev order.
			y := (*[2]float32)(yp[2*int(r):])
			y[1] = yr
			y[0] = yi
		}
	} else {
		xp1 := 0
		xp2 := stride * (N2 - 1)
		for i, r := range bitrev {
			yr := float32(in[xp2]*t0[i]) + float32(in[xp1]*t1[i])
			yi := float32(in[xp1]*t0[i]) - float32(in[xp2]*t1[i])
			y := (*[2]float32)(yp[2*int(r):])
			y[1] = yr
			y[0] = yi
			xp1 += 2 * stride
			xp2 -= 2 * stride
		}
	}

	if useAVX2 {
		opusFFTAVX2(&simdFFTs[shift], yp)
	} else {
		opusFFTImpl(&fftConfigs[shift], yp)
	}

	// Post-rotate and de-shuffle from both ends of the buffer at once to
	// make it in-place: step i rotates value i (front) and value N4-1-i
	// (back), each stored as (imag, real). Loop to (N4+1)>>1 to handle odd
	// N4. When N4 is odd, the middle pair will be computed twice.
	for i := 0; i < (N4+1)>>1; i++ {
		f := (*[2]float32)(yp[2*i:])
		b := (*[2]float32)(yp[N2-2-2*i:])
		// We swap real and imag because we're using an FFT instead of an
		// IFFT.
		re, im := f[1], f[0]
		// We'd scale up by 2 here, but instead it's done when mixing the
		// windows.
		yr := float32(re*t0[i]) + float32(im*t1[i])
		yi := float32(re*t1[i]) - float32(im*t0[i])
		re, im = b[1], b[0]
		f[0] = yr
		b[1] = yi
		j := N4 - 1 - i
		yr = float32(re*t0[j]) + float32(im*t1[j])
		yi = float32(re*t1[j]) - float32(im*t0[j])
		b[0] = yr
		f[1] = yi
	}

	// Mirror on both sides for TDAC.
	o := out[:overlap]
	w := window[:overlap]
	for i := 0; i < overlap/2; i++ {
		j := overlap - 1 - i
		x1 := o[j]
		x2 := o[i]
		o[i] = float32(w[j]*x2) - float32(w[i]*x1)
		o[j] = float32(w[i]*x2) + float32(w[j]*x1)
	}
}
