// Copyright (c) 2006-2011, Skype Limited. All rights reserved.
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
// - Redistributions of source code must retain the above copyright notice,
// this list of conditions and the following disclaimer.
// - Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
// - Neither the name of Internet Society, IETF or IETF Trust, nor the
// names of specific contributors, may be used to endorse or promote
// products derived from this software without specific prior written
// permission.
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
// Ported from libopus 1.5.2 silk/resampler_private_IIR_FIR.c.

package silk

// resamplerPrivateIIRFIRInterpol is silk_resampler_private_IIR_FIR_INTERPOL:
// it interpolates the 2x upsampled signal buf and stores the result in out,
// returning the rest of out.
//
// For the decoder's ratios to 48 kHz the phase of each output follows a
// short cycle, so fixed-ratio cores with the taps of each phase as constants
// compute them (see interpolFIR3Over2); other ratios take the loop of
// libopus.
func resamplerPrivateIIRFIRInterpol(out []int16, buf []int16, maxIndexQ16, indexIncrementQ16 int32) []int16 {
	// The batch has n input samples: buf holds 2n upsampled ones after
	// RESAMPLER_ORDER_FIR_12 of history.
	n := int(maxIndexQ16 >> 17)
	if maxIndexQ16 == int32(n)<<17 && n <= resamplerFixedRatioMaxIn {
		switch indexIncrementQ16 {
		case invRatio16To48Q16:
			return out[interpolFIR3Over2(out, buf, n):]
		case invRatio12To48Q16:
			return out[interpolFIR2(out, buf, n):]
		case invRatio8To48Q16:
			return out[interpolFIR3(out, buf, n):]
		}
	}
	return resamplerIIRFIRInterpolGeneric(out, buf, maxIndexQ16, indexIncrementQ16)
}

// resamplerIIRFIRInterpolGeneric is the loop of
// silk_resampler_private_IIR_FIR_INTERPOL for any ratio.
func resamplerIIRFIRInterpolGeneric(out []int16, buf []int16, maxIndexQ16, indexIncrementQ16 int32) []int16 {
	// Interpolate upsampled signal and store in output array.
	n := 0
	for indexQ16 := int32(0); indexQ16 < maxIndexQ16; indexQ16 += indexIncrementQ16 {
		tableIndex := smulwb(indexQ16&0xFFFF, 12)
		bufPtr := buf[indexQ16>>16:][:resamplerOrderFIR12]
		c0 := &resamplerFracFIR12[tableIndex]
		c1 := &resamplerFracFIR12[11-tableIndex]

		resQ15 := smulbb(int32(bufPtr[0]), int32(c0[0]))
		resQ15 = smlabb(resQ15, int32(bufPtr[1]), int32(c0[1]))
		resQ15 = smlabb(resQ15, int32(bufPtr[2]), int32(c0[2]))
		resQ15 = smlabb(resQ15, int32(bufPtr[3]), int32(c0[3]))
		resQ15 = smlabb(resQ15, int32(bufPtr[4]), int32(c1[3]))
		resQ15 = smlabb(resQ15, int32(bufPtr[5]), int32(c1[2]))
		resQ15 = smlabb(resQ15, int32(bufPtr[6]), int32(c1[1]))
		resQ15 = smlabb(resQ15, int32(bufPtr[7]), int32(c1[0]))
		out[n] = sat16(rshiftRound(resQ15, 15))
		n++
	}
	return out[n:]
}

// The index increments (invRatio_Q16) silk_resampler_init computes for the
// IIR_FIR path to 48 kHz.
const (
	invRatio16To48Q16 = 43691 // 2/3 in Q16, rounded up
	invRatio12To48Q16 = 32768 // 1/2
	invRatio8To48Q16  = 21846 // 1/3 in Q16, rounded up
)

// resamplerFixedRatioMaxIn bounds the batch length of the fixed-ratio
// cores. The increments above exceed the exact ratio by at most 2/3 in Q16,
// so the index drifts by less than one Q16 step per output cycle; over
// RESAMPLER_MAX_BATCH_SIZE_MS (at most 160 input samples, from which the
// index restarts at zero) it never reaches the next phase, which takes more
// than 2730 cycles.
const resamplerFixedRatioMaxIn = maxFsKHz * resamplerMaxBatchSizeMs

// Each output of silk_resampler_private_IIR_FIR_INTERPOL is the sum of eight
// products buf[i+t] * h[t] of a window of the upsampled signal with the taps
// h of the output's phase p = table_index: h[t] =
// silk_resampler_frac_FIR_12[p][t] for t < 4 and
// silk_resampler_frac_FIR_12[11-p][7-t] for t >= 4. The sum is formed in
// opus_int32, which no product sum of int16 samples with these taps
// overflows, so the cores below sum in any order and match libopus exactly
// (kernels_test.go checks them against the loop). The window slides along
// buf so that its base stays in a register.

// interpolFIR3Over2Scalar is silk_resampler_private_IIR_FIR_INTERPOL with the
// increment of 16 kHz to 48 kHz on n input samples (2n upsampled): output
// 3m+j has the phases 0, 8 and 4 for j = 0, 1, 2 and starts at upsampled
// sample 2m, 2m and 2m+1. It writes the 3n outputs to out and returns 3n.
//
// interpolFIR3Over2 (simd_*.go) runs a vector kernel where the CPU has one,
// otherwise interpolFIR3Over2Scalar.
func interpolFIR3Over2Scalar(out []int16, buf []int16, n int) int {
	out = out[:3*n]
	buf = buf[:2*n+resamplerOrderFIR12]
	for range n {
		w := (*[resamplerOrderFIR12 + 1]int16)(buf)
		o := (*[3]int16)(out)
		b0, b1, b2, b3, b4 := int32(w[0]), int32(w[1]), int32(w[2]), int32(w[3]), int32(w[4])
		b5, b6, b7, b8 := int32(w[5]), int32(w[6]), int32(w[7]), int32(w[8])
		// Phase 0: rows 0 and 11.
		r0 := b0*189 + b1*-600 + b2*617 + b3*30567 + b4*2996 + b5*-1375 + b6*425 + b7*-46
		// Phase 8: rows 8 and 3.
		r1 := b0*-103 + b1*896 + b2*-3487 + b3*11950 + b4*26341 + b5*-3350 + b6*529 + b7*-4
		// Phase 4, one sample later: rows 4 and 7.
		r2 := b1*-48 + b2*758 + b3*-3956 + b4*23973 + b5*15143 + b6*-3957 + b7*967 + b8*-107
		o[0] = sat16(rshiftRound(r0, 15))
		o[1] = sat16(rshiftRound(r1, 15))
		o[2] = sat16(rshiftRound(r2, 15))
		buf, out = buf[2:], out[3:]
	}
	return 3 * n
}

// interpolFIR2Scalar is silk_resampler_private_IIR_FIR_INTERPOL with the increment
// of 12 kHz to 48 kHz on n input samples: outputs 2j and 2j+1 have the
// phases 0 and 6 and start at upsampled sample j. It writes the 4n outputs
// to out and returns 4n (interpolFIR2 in simd_*.go dispatches).
func interpolFIR2Scalar(out []int16, buf []int16, n int) int {
	out = out[:4*n]
	buf = buf[:2*n+resamplerOrderFIR12]
	for range 2 * n {
		w := (*[resamplerOrderFIR12]int16)(buf)
		o := (*[2]int16)(out)
		b0, b1, b2, b3 := int32(w[0]), int32(w[1]), int32(w[2]), int32(w[3])
		b4, b5, b6, b7 := int32(w[4]), int32(w[5]), int32(w[6]), int32(w[7])
		// Phase 0: rows 0 and 11.
		r0 := b0*189 + b1*-600 + b2*617 + b3*30567 + b4*2996 + b5*-1375 + b6*425 + b7*-46
		// Phase 6: rows 6 and 5.
		r1 := b0*-99 + b1*972 + b2*-4222 + b3*18278 + b4*21254 + b5*-4235 + b6*905 + b7*-80
		o[0] = sat16(rshiftRound(r0, 15))
		o[1] = sat16(rshiftRound(r1, 15))
		buf, out = buf[1:], out[2:]
	}
	return 4 * n
}

// interpolFIR3Scalar is silk_resampler_private_IIR_FIR_INTERPOL with the increment
// of 8 kHz to 48 kHz on n input samples: outputs 3m, 3m+1 and 3m+2 have the
// phases 0, 4 and 8 and start at upsampled sample m. It writes the 6n
// outputs to out and returns 6n (interpolFIR3 in simd_*.go dispatches).
func interpolFIR3Scalar(out []int16, buf []int16, n int) int {
	out = out[:6*n]
	buf = buf[:2*n+resamplerOrderFIR12]
	for range 2 * n {
		w := (*[resamplerOrderFIR12]int16)(buf)
		o := (*[3]int16)(out)
		b0, b1, b2, b3 := int32(w[0]), int32(w[1]), int32(w[2]), int32(w[3])
		b4, b5, b6, b7 := int32(w[4]), int32(w[5]), int32(w[6]), int32(w[7])
		// Phase 0: rows 0 and 11.
		r0 := b0*189 + b1*-600 + b2*617 + b3*30567 + b4*2996 + b5*-1375 + b6*425 + b7*-46
		// Phase 4: rows 4 and 7.
		r1 := b0*-48 + b1*758 + b2*-3956 + b3*23973 + b4*15143 + b5*-3957 + b6*967 + b7*-107
		// Phase 8: rows 8 and 3.
		r2 := b0*-103 + b1*896 + b2*-3487 + b3*11950 + b4*26341 + b5*-3350 + b6*529 + b7*-4
		o[0] = sat16(rshiftRound(r0, 15))
		o[1] = sat16(rshiftRound(r1, 15))
		o[2] = sat16(rshiftRound(r2, 15))
		buf, out = buf[1:], out[3:]
	}
	return 6 * n
}

// resamplerIIRFIRBufLen is the size of the buffer of
// silk_resampler_private_IIR_FIR, 2 * batchSize + RESAMPLER_ORDER_FIR_12, for
// the largest input rate of the decoder.
const resamplerIIRFIRBufLen = 2*maxFsKHz*resamplerMaxBatchSizeMs + resamplerOrderFIR12

// resamplerPrivateIIRFIR is silk_resampler_private_IIR_FIR: it upsamples
// using a combination of allpass-based 2x upsampling and FIR interpolation.
func resamplerPrivateIIRFIR(s *resamplerState, out []int16, in []int16, inLen int) {
	var (
		buf        [resamplerIIRFIRBufLen]int16
		nSamplesIn int
	)

	// Copy buffered samples to start of buffer.
	copy(buf[:resamplerOrderFIR12], s.sFIR[:])

	// Iterate over blocks of frameSizeIn input samples.
	indexIncrementQ16 := s.invRatioQ16
	for {
		nSamplesIn = min(inLen, s.batchSize)

		// Upsample 2x.
		resamplerPrivateUp2HQ(&s.sIIR, buf[resamplerOrderFIR12:], in, nSamplesIn)

		maxIndexQ16 := int32(nSamplesIn) << (16 + 1) // + 1 because 2x upsampling
		out = resamplerPrivateIIRFIRInterpol(out, buf[:], maxIndexQ16, indexIncrementQ16)
		in = in[nSamplesIn:]
		inLen -= nSamplesIn

		if inLen > 0 {
			// More iterations to do; copy last part of filtered signal to
			// beginning of buffer.
			copy(buf[:resamplerOrderFIR12], buf[nSamplesIn<<1:])
		} else {
			break
		}
	}

	// Copy last part of filtered signal to the state for the next call.
	copy(s.sFIR[:], buf[nSamplesIn<<1:])
}
