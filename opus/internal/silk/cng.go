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
// Ported from libopus 1.5.2 silk/CNG.c.

package silk

// cngExc is silk_CNG_exc: it generates the excitation for the CNG LPC
// synthesis from random samples of excBufQ14.
func cngExc(excQ14 []int32, excBufQ14 *[maxFrameLength]int32, length int, randSeed *int32) {
	excMask := cngBufMaskMax
	for excMask > length {
		excMask >>= 1
	}

	seed := *randSeed
	for i := range length {
		seed = randNext(seed)
		idx := int(seed>>24) & excMask
		excQ14[i] = excBufQ14[idx]
	}
	*randSeed = seed
}

// cngReset is silk_CNG_Reset.
func cngReset(psDec *decoderState) {
	nlsfStepQ15 := int32(int16Max) / int32(psDec.lpcOrder+1)
	nlsfAccQ15 := int32(0)
	for i := 0; i < psDec.lpcOrder; i++ {
		nlsfAccQ15 += nlsfStepQ15
		psDec.sCNG.cngSmthNLSFQ15[i] = int16(nlsfAccQ15)
	}
	psDec.sCNG.cngSmthGainQ16 = 0
	psDec.sCNG.randSeed = 3176576
}

// cng is silk_CNG: it updates the comfort noise estimate from a decoded
// frame, and adds comfort noise to frame when the packet was lost.
func cng(psDec *decoderState, psDecCtrl *decoderControl, frame []int16, length int) {
	var aQ12 [maxLPCOrder]int16
	psCNG := &psDec.sCNG

	if psDec.fsKHz != psCNG.fsKHz {
		// Reset state.
		cngReset(psDec)

		psCNG.fsKHz = psDec.fsKHz
	}
	if psDec.lossCnt == 0 && psDec.prevSignalType == typeNoVoiceActivity {
		// Update CNG parameters.

		// Smoothing of LSF's.
		for i := 0; i < psDec.lpcOrder; i++ {
			psCNG.cngSmthNLSFQ15[i] += int16(smulwb(int32(psDec.prevNLSFQ15[i])-int32(psCNG.cngSmthNLSFQ15[i]), cngNLSFSmthQ16))
		}
		// Find the subframe with the highest gain.
		maxGainQ16 := int32(0)
		subfr := 0
		for i := 0; i < psDec.nbSubfr; i++ {
			if psDecCtrl.gainsQ16[i] > maxGainQ16 {
				maxGainQ16 = psDecCtrl.gainsQ16[i]
				subfr = i
			}
		}
		// Update CNG excitation buffer with excitation from this subframe.
		copy(psCNG.cngExcBufQ14[psDec.subfrLength:psDec.nbSubfr*psDec.subfrLength], psCNG.cngExcBufQ14[:(psDec.nbSubfr-1)*psDec.subfrLength])
		copy(psCNG.cngExcBufQ14[:psDec.subfrLength], psDec.excQ14[subfr*psDec.subfrLength:])

		// Smooth gains.
		for i := 0; i < psDec.nbSubfr; i++ {
			psCNG.cngSmthGainQ16 += smulwb(psDecCtrl.gainsQ16[i]-psCNG.cngSmthGainQ16, cngGainSmthQ16)
			// If the smoothed gain is 3 dB greater than this subframe's
			// gain, use this subframe's gain to adapt faster.
			if smulww(psCNG.cngSmthGainQ16, cngGainSmthThresholdQ16) > psDecCtrl.gainsQ16[i] {
				psCNG.cngSmthGainQ16 = psDecCtrl.gainsQ16[i]
			}
		}
	}

	// Add CNG when packet is lost or during DTX.
	if psDec.lossCnt != 0 {
		// libopus allocates this on the stack for each frame.
		var cngSigQ14 [maxFrameLength + maxLPCOrder]int32

		// Generate CNG excitation.
		gainQ16 := smulww(int32(psDec.sPLC.randScaleQ14), psDec.sPLC.prevGainQ16[1])
		if gainQ16 >= 1<<21 || psCNG.cngSmthGainQ16 > 1<<23 {
			gainQ16 = smultt(gainQ16, gainQ16)
			gainQ16 = smultt(psCNG.cngSmthGainQ16, psCNG.cngSmthGainQ16) - gainQ16<<5
			gainQ16 = sqrtApprox(gainQ16) << 16
		} else {
			gainQ16 = smulww(gainQ16, gainQ16)
			gainQ16 = smulww(psCNG.cngSmthGainQ16, psCNG.cngSmthGainQ16) - gainQ16<<5
			gainQ16 = sqrtApprox(gainQ16) << 8
		}
		gainQ10 := gainQ16 >> 6

		cngExc(cngSigQ14[maxLPCOrder:], &psCNG.cngExcBufQ14, length, &psCNG.randSeed)

		// Convert CNG NLSF to filter representation.
		nlsf2A(aQ12[:], psCNG.cngSmthNLSFQ15[:], psDec.lpcOrder)

		// Generate CNG signal, by synthesis filtering.
		copy(cngSigQ14[:maxLPCOrder], psCNG.cngSynthState[:])
		for i := range length {
			// Avoids introducing a bias because silk_SMLAWB() always rounds
			// to -inf.
			s := cngSigQ14[i : i+maxLPCOrder]
			lpcPredQ10 := int32(psDec.lpcOrder >> 1)
			lpcPredQ10 = smlawb(lpcPredQ10, s[15], int32(aQ12[0]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[14], int32(aQ12[1]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[13], int32(aQ12[2]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[12], int32(aQ12[3]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[11], int32(aQ12[4]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[10], int32(aQ12[5]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[9], int32(aQ12[6]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[8], int32(aQ12[7]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[7], int32(aQ12[8]))
			lpcPredQ10 = smlawb(lpcPredQ10, s[6], int32(aQ12[9]))
			if psDec.lpcOrder == 16 {
				lpcPredQ10 = smlawb(lpcPredQ10, s[5], int32(aQ12[10]))
				lpcPredQ10 = smlawb(lpcPredQ10, s[4], int32(aQ12[11]))
				lpcPredQ10 = smlawb(lpcPredQ10, s[3], int32(aQ12[12]))
				lpcPredQ10 = smlawb(lpcPredQ10, s[2], int32(aQ12[13]))
				lpcPredQ10 = smlawb(lpcPredQ10, s[1], int32(aQ12[14]))
				lpcPredQ10 = smlawb(lpcPredQ10, s[0], int32(aQ12[15]))
			}

			// Update states.
			cngSigQ14[maxLPCOrder+i] = addSat32(cngSigQ14[maxLPCOrder+i], lshiftSat32(lpcPredQ10, 4))

			// Scale with Gain and add to input signal.
			frame[i] = sat16(int32(frame[i]) + int32(sat16(rshiftRound(smulww(cngSigQ14[maxLPCOrder+i], gainQ10), 8))))
		}
		copy(psCNG.cngSynthState[:], cngSigQ14[length:length+maxLPCOrder])
	} else {
		clear(psCNG.cngSynthState[:psDec.lpcOrder])
	}
}
