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
// Ported from libopus 1.5.2 silk/decode_core.c.

package silk

// decodeCore is silk_decode_core: the inverse noise shaping quantization,
// long-term (LTP) and short-term (LPC) synthesis of one frame into xq.
func decodeCore(psDec *decoderState, psDecCtrl *decoderControl, xq []int16, pulses *[maxFrameLength]int16) {
	var (
		lag     int
		aQ12Tmp [maxLPCOrder]int16
		// libopus allocates these on the stack for each frame.
		sLTP    [maxLTPMemLength]int16
		sLTPQ15 [maxLTPMemLength + maxFrameLength]int32
		resQ14  [maxSubFrameLength]int32
		sLPCQ14 [maxSubFrameLength + maxLPCOrder]int32
	)

	offsetQ10 := int32(quantizationOffsetsQ10[psDec.indices.signalType>>1][psDec.indices.quantOffsetType])

	nlsfInterpolationFlag := psDec.indices.nlsfInterpCoefQ2 < 1<<2

	// Decode excitation.
	decodeExcitation(psDec.excQ14[:psDec.frameLength], pulses[:psDec.frameLength], int32(psDec.indices.seed), offsetQ10)

	// Copy LPC state.
	copy(sLPCQ14[:maxLPCOrder], psDec.sLPCQ14Buf[:])

	pexcQ14 := psDec.excQ14[:]
	pxq := xq
	sLTPBufIdx := psDec.ltpMemLength
	// Loop over subframes.
	for k := 0; k < psDec.nbSubfr; k++ {
		presQ14 := resQ14[:]
		aQ12 := psDecCtrl.predCoefQ12[k>>1][:]

		// Preload LPC coefficients to array on stack.
		copy(aQ12Tmp[:psDec.lpcOrder], aQ12[:psDec.lpcOrder])
		bQ14 := psDecCtrl.ltpCoefQ14[k*ltpOrder : (k+1)*ltpOrder]
		signalType := int(psDec.indices.signalType)

		gainQ10 := psDecCtrl.gainsQ16[k] >> 6
		invGainQ31 := inverse32VarQ(psDecCtrl.gainsQ16[k], 47)

		// Calculate gain adjustment factor.
		var gainAdjQ16 int32
		if psDecCtrl.gainsQ16[k] != psDec.prevGainQ16 {
			gainAdjQ16 = div32VarQ(psDec.prevGainQ16, psDecCtrl.gainsQ16[k], 16)

			// Scale short term state.
			for i := range maxLPCOrder {
				sLPCQ14[i] = smulww(gainAdjQ16, sLPCQ14[i])
			}
		} else {
			gainAdjQ16 = 1 << 16
		}

		// Save inv_gain.
		psDec.prevGainQ16 = psDecCtrl.gainsQ16[k]

		// Avoid abrupt transition from voiced PLC to unvoiced normal
		// decoding.
		if psDec.lossCnt != 0 && psDec.prevSignalType == typeVoiced &&
			psDec.indices.signalType != typeVoiced && k < maxNbSubfr/2 {
			clear(bQ14)
			bQ14[ltpOrder/2] = ltpTransitionGainQ14

			signalType = typeVoiced
			psDecCtrl.pitchL[k] = psDec.lagPrev
		}

		if signalType == typeVoiced {
			// Voiced.
			lag = psDecCtrl.pitchL[k]

			// Re-whitening.
			if k == 0 || (k == 2 && nlsfInterpolationFlag) {
				// Rewhiten with new A coefs.
				startIdx := psDec.ltpMemLength - lag - psDec.lpcOrder - ltpOrder/2

				if k == 2 {
					copy(psDec.outBuf[psDec.ltpMemLength:psDec.ltpMemLength+2*psDec.subfrLength], xq[:2*psDec.subfrLength])
				}

				lpcAnalysisFilter(sLTP[startIdx:], psDec.outBuf[startIdx+k*psDec.subfrLength:],
					aQ12, psDec.ltpMemLength-startIdx, psDec.lpcOrder)

				// After rewhitening the LTP state is unscaled.
				if k == 0 {
					// Do LTP downscaling to reduce inter-packet dependency.
					invGainQ31 = smulwb(invGainQ31, psDecCtrl.ltpScaleQ14) << 2
				}
				for i := 0; i < lag+ltpOrder/2; i++ {
					sLTPQ15[sLTPBufIdx-i-1] = smulwb(invGainQ31, int32(sLTP[psDec.ltpMemLength-i-1]))
				}
			} else if gainAdjQ16 != 1<<16 {
				// Update LTP state when Gain changes.
				for i := 0; i < lag+ltpOrder/2; i++ {
					sLTPQ15[sLTPBufIdx-i-1] = smulww(gainAdjQ16, sLTPQ15[sLTPBufIdx-i-1])
				}
			}
		}

		// Long-term prediction.
		if signalType == typeVoiced {
			// Set up pointer.
			predLag := sLTPBufIdx - lag + ltpOrder/2
			ltpSynthesis(sLTPQ15[:], predLag, sLTPBufIdx, presQ14[:psDec.subfrLength], pexcQ14[:psDec.subfrLength],
				(*[ltpOrder]int16)(bQ14))
			sLTPBufIdx += psDec.subfrLength
		} else {
			presQ14 = pexcQ14
		}

		// Short-term prediction.
		if psDec.lpcOrder == 16 {
			lpcSynthesis16(sLPCQ14[:maxLPCOrder+psDec.subfrLength], presQ14, pxq[:psDec.subfrLength], &aQ12Tmp, gainQ10)
		} else {
			lpcSynthesis10(sLPCQ14[:maxLPCOrder+psDec.subfrLength], presQ14, pxq[:psDec.subfrLength], &aQ12Tmp, gainQ10)
		}

		// Update LPC filter state.
		copy(sLPCQ14[:maxLPCOrder], sLPCQ14[psDec.subfrLength:psDec.subfrLength+maxLPCOrder])
		pexcQ14 = pexcQ14[psDec.subfrLength:]
		pxq = pxq[psDec.subfrLength:]
	}

	// Save LPC state.
	copy(psDec.sLPCQ14Buf[:], sLPCQ14[:maxLPCOrder])
}

// ltpSynthesis is the long-term prediction loop of silk_decode_core for a
// voiced subframe: output i adds to the excitation excQ14[i] the five-tap
// prediction from sLTPQ15 around predLag+i, and is stored to presQ14[i]
// and, times two, to sLTPQ15[bufIdx+i]. The five products are truncated on
// their own and summed in wrapping opus_int32, so they are formed
// independently and summed as a tree; each output still reads its taps
// before it is stored, as in libopus, whatever the lag.
func ltpSynthesis(sLTPQ15 []int32, predLag, bufIdx int, presQ14, excQ14 []int32, bQ14 *[ltpOrder]int16) {
	n := len(presQ14)
	excQ14 = excQ14[:n]
	b0, b1, b2, b3, b4 := int64(bQ14[0]), int64(bQ14[1]), int64(bQ14[2]), int64(bQ14[3]), int64(bQ14[4])
	// w slides along the taps and o along the outputs.
	w := sLTPQ15[predLag-4 : predLag+n]
	o := sLTPQ15[bufIdx : bufIdx+n]
	for i := range n {
		s := (*[ltpOrder]int32)(w)
		// Avoids introducing a bias because silk_SMLAWB() always rounds to
		// -inf: the sum starts at 2.
		p := int32((int64(s[4])*b0)>>16) + int32((int64(s[3])*b1)>>16)
		q := int32((int64(s[2])*b2)>>16) + int32((int64(s[1])*b3)>>16)
		ltpPredQ13 := (p + q) + (int32((int64(s[0])*b4)>>16) + 2)

		// Generate LPC excitation.
		r := excQ14[i] + ltpPredQ13<<1
		presQ14[i] = r

		// Update states.
		o[i] = r << 1
		w = w[1:]
	}
}

// decodeExcitation is the excitation loop of silk_decode_core: it turns the
// pulses into the excitation excQ14 with the quantization offset and the
// pseudo-random sign of the seed. The pulse sign and the random sign (that
// of the seed) select their adjustments with masks: the seed's sign is a
// coin flip a branch would mispredict.
//
// The seed is r_i = silk_RAND(s_i) with s_{i+1} = r_i + pulses[i], so
// r_{i+1} = I + M*(r_i + pulses[i]) for silk_RAND's multiplier M and
// increment I, and r_{i+2} = M^2*r_i + (I + M*I) + M^2*pulses[i] +
// M*pulses[i+1]. In uint32 arithmetic, which wraps like silk_RAND, the
// identity is exact; stepping two samples at a time halves the chain of
// multiplies from sample to sample.
func decodeExcitation(excQ14 []int32, pulses []int16, randSeed, offsetQ10 int32) {
	const (
		m  = randMultiplier
		m2 = uint32(m * m % (1 << 32))
		i2 = uint32((randIncrement + m*randIncrement) % (1 << 32))
	)
	pulses = pulses[:len(excQ14)]
	r := uint32(randNext(randSeed))
	i := 0
	for ; i+2 <= len(pulses); i += 2 {
		p0, p1 := uint32(int32(pulses[i])), uint32(int32(pulses[i+1]))
		r1 := randIncrement + (r+p0)*m
		excQ14[i] = excitation(pulses[i], int32(r), offsetQ10)
		excQ14[i+1] = excitation(pulses[i+1], int32(r1), offsetQ10)
		r = m2*r + (i2 + m2*p0 + m*p1)
	}
	if i < len(pulses) {
		excQ14[i] = excitation(pulses[i], int32(r), offsetQ10)
	}
}

// excitation returns the excitation of one pulse p with the seed's sign
// taken from rand.
func excitation(p int16, rand, offsetQ10 int32) int32 {
	exc := int32(p) << 14
	// sign is -1, 0 or 1 as exc is negative, zero or positive.
	sign := exc>>31 | int32(uint32(-exc)>>31)
	exc -= sign * (quantLevelAdjustQ10 << 4)
	exc += offsetQ10 << 4
	neg := rand >> 31
	return (exc ^ neg) - neg
}

// lpcAdd is silk_ADD_SAT32(res, silk_LSHIFT_SAT32(pred, 4)), the last
// step of each LPC synthesis output, as an int64. Neither saturates unless
// the stream is extreme, so it returns the shift and add and whether that
// is the result; the caller passes the other cases, a predictable branch,
// to lpcAddSat. The dependency from one output to the next is then short.
func lpcAdd(res, pred int32) (int64, bool) {
	x := int64(res) + int64(pred)<<4
	return x, uint32(pred-int32Min>>4) <= uint32(int32Max>>4-int32Min>>4) && uint64(x-int32Min) <= uint64(int32Max-int32Min)
}

// lpcAddSat is silk_ADD_SAT32(res, silk_LSHIFT_SAT32(pred, 4)) as an int64,
// for the cases lpcAdd leaves.
//
//go:noinline
func lpcAddSat(res, pred int32) int64 {
	return int64(addSat32(res, lshiftSat32(pred, 4)))
}

// lpcSynthesis16 is the short-term prediction loop of silk_decode_core for
// an order-16 filter over one subframe of len(xq) samples: sLPCQ14 holds
// the MAX_LPC_ORDER previous outputs followed by room for the new ones,
// resQ14 the LPC excitation, aQ12 the coefficients; xq receives the outputs
// scaled with gainQ10.
//
// libopus accumulates the sixteen silk_SMLAWB terms in one chain, newest
// output first. Each term is a product truncated on its own (silk_SMULWB,
// written out as mulQ16 does) and the sum wraps in opus_int32, so the order
// of the additions does not change the result: the fifteen terms of older
// outputs are summed as a tree, and the newest output, kept from the
// previous iteration, is added last. The sample-to-sample dependency is then
// one multiply, one add and the saturations. The products are written out
// pairwise, in order, because Go schedules the operations of a block in
// source order where it can (with the multiplies in an inlined helper all
// loads would come first and spill), and the window slides so that its base
// stays in a register.
func lpcSynthesis16(sLPCQ14 []int32, resQ14 []int32, xq []int16, aQ12 *[maxLPCOrder]int16, gainQ10 int32) {
	n := len(xq)
	resQ14 = resQ14[:n]
	sLPCQ14 = sLPCQ14[:n+maxLPCOrder]
	a := aQ12
	prev := int64(sLPCQ14[maxLPCOrder-1])
	// w slides along sLPCQ14, so its base stays in a register.
	w := sLPCQ14
	for i := range n {
		s := (*[maxLPCOrder + 1]int32)(w)
		p0 := int32((int64(s[14])*int64(a[1]))>>16) + int32((int64(s[13])*int64(a[2]))>>16)
		p1 := int32((int64(s[12])*int64(a[3]))>>16) + int32((int64(s[11])*int64(a[4]))>>16)
		q0 := p0 + p1
		p2 := int32((int64(s[10])*int64(a[5]))>>16) + int32((int64(s[9])*int64(a[6]))>>16)
		p3 := int32((int64(s[8])*int64(a[7]))>>16) + int32((int64(s[7])*int64(a[8]))>>16)
		q1 := p2 + p3
		p4 := int32((int64(s[6])*int64(a[9]))>>16) + int32((int64(s[5])*int64(a[10]))>>16)
		p5 := int32((int64(s[4])*int64(a[11]))>>16) + int32((int64(s[3])*int64(a[12]))>>16)
		q2 := p4 + p5
		p6 := int32((int64(s[2])*int64(a[13]))>>16) + int32((int64(s[1])*int64(a[14]))>>16)
		// Avoids introducing a bias because silk_SMLAWB() always rounds to
		// -inf: the sum starts at order/2.
		p7 := int32((int64(s[0])*int64(a[15]))>>16) + 16/2
		q3 := p6 + p7
		lpcPredQ10 := (q0 + q1) + (q2 + q3) + int32((prev*int64(a[0]))>>16)

		// Add prediction to LPC excitation.
		var ok bool
		if prev, ok = lpcAdd(resQ14[i], lpcPredQ10); !ok {
			prev = lpcAddSat(resQ14[i], lpcPredQ10)
		}
		y := int32(prev)
		s[maxLPCOrder] = y
		w = w[1:]

		// Scale with gain.
		xq[i] = sat16(rshiftRound(smulww(y, gainQ10), 8))
	}
}

// lpcSynthesis10 is lpcSynthesis16 for an order-10 filter: the outputs older
// than ten samples in sLPCQ14 do not contribute.
func lpcSynthesis10(sLPCQ14 []int32, resQ14 []int32, xq []int16, aQ12 *[maxLPCOrder]int16, gainQ10 int32) {
	n := len(xq)
	resQ14 = resQ14[:n]
	sLPCQ14 = sLPCQ14[:n+maxLPCOrder]
	a := aQ12
	prev := int64(sLPCQ14[maxLPCOrder-1])
	w := sLPCQ14
	for i := range n {
		s := (*[maxLPCOrder + 1]int32)(w)
		p0 := int32((int64(s[14])*int64(a[1]))>>16) + int32((int64(s[13])*int64(a[2]))>>16)
		p1 := int32((int64(s[12])*int64(a[3]))>>16) + int32((int64(s[11])*int64(a[4]))>>16)
		q0 := p0 + p1
		p2 := int32((int64(s[10])*int64(a[5]))>>16) + int32((int64(s[9])*int64(a[6]))>>16)
		p3 := int32((int64(s[8])*int64(a[7]))>>16) + int32((int64(s[7])*int64(a[8]))>>16)
		q1 := p2 + p3
		p4 := int32((int64(s[6])*int64(a[9]))>>16) + 10/2
		lpcPredQ10 := (q0 + q1) + p4 + int32((prev*int64(a[0]))>>16)
		var ok bool
		if prev, ok = lpcAdd(resQ14[i], lpcPredQ10); !ok {
			prev = lpcAddSat(resQ14[i], lpcPredQ10)
		}
		y := int32(prev)
		s[maxLPCOrder] = y
		w = w[1:]
		xq[i] = sat16(rshiftRound(smulww(y, gainQ10), 8))
	}
}
