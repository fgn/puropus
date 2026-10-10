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
// Ported from libopus 1.5.2 silk/PLC.c and silk/PLC.h (without deep PLC).

package silk

// nbAtt is NB_ATT in PLC.c.
const nbAtt = 2

var (
	harmAttQ15            = [nbAtt]int16{32440, 31130} // 0.99, 0.95
	plcRandAttenuateVQ15  = [nbAtt]int16{31130, 26214} // 0.95, 0.8
	plcRandAttenuateUVQ15 = [nbAtt]int16{32440, 29491} // 0.99, 0.9
)

// plcReset is silk_PLC_Reset.
func plcReset(psDec *decoderState) {
	psDec.sPLC.pitchLQ8 = int32(psDec.frameLength) << (8 - 1)
	psDec.sPLC.prevGainQ16[0] = oneQ16
	psDec.sPLC.prevGainQ16[1] = oneQ16
	psDec.sPLC.subfrLength = 20
	psDec.sPLC.nbSubfr = 2
}

// plc is silk_PLC: it conceals a lost frame into frame, or updates the
// concealment state from a decoded frame.
func plc(psDec *decoderState, psDecCtrl *decoderControl, frame []int16, lost bool) {
	// PLC control function.
	if psDec.fsKHz != psDec.sPLC.fsKHz {
		plcReset(psDec)
		psDec.sPLC.fsKHz = psDec.fsKHz
	}

	if lost {
		// Generate signal.
		plcConceal(psDec, psDecCtrl, frame)

		psDec.lossCnt++
	} else {
		// Update state.
		plcUpdate(psDec, psDecCtrl)
	}
}

// plcUpdate is silk_PLC_update: it updates the parameters used in case of
// packet loss.
func plcUpdate(psDec *decoderState, psDecCtrl *decoderControl) {
	psPLC := &psDec.sPLC

	// Update parameters used in case of packet loss.
	psDec.prevSignalType = int(psDec.indices.signalType)
	ltpGainQ14 := int32(0)
	if psDec.indices.signalType == typeVoiced {
		// Find the parameters for the last subframe which contains a pitch
		// pulse.
		for j := 0; j*psDec.subfrLength < psDecCtrl.pitchL[psDec.nbSubfr-1]; j++ {
			if j == psDec.nbSubfr {
				break
			}
			coefs := psDecCtrl.ltpCoefQ14[(psDec.nbSubfr-1-j)*ltpOrder:][:ltpOrder]
			tempLTPGainQ14 := int32(0)
			for i := range ltpOrder {
				tempLTPGainQ14 += int32(coefs[i])
			}
			if tempLTPGainQ14 > ltpGainQ14 {
				ltpGainQ14 = tempLTPGainQ14
				copy(psPLC.ltpCoefQ14[:], coefs)

				psPLC.pitchLQ8 = int32(psDecCtrl.pitchL[psDec.nbSubfr-1-j]) << 8
			}
		}

		psPLC.ltpCoefQ14 = [ltpOrder]int16{}
		psPLC.ltpCoefQ14[ltpOrder/2] = int16(ltpGainQ14)

		// Limit LT coefs.
		if ltpGainQ14 < vPitchGainStartMinQ14 {
			tmp := int32(vPitchGainStartMinQ14) << 10
			scaleQ10 := tmp / max(ltpGainQ14, 1)
			for i := range ltpOrder {
				psPLC.ltpCoefQ14[i] = int16(smulbb(int32(psPLC.ltpCoefQ14[i]), scaleQ10) >> 10)
			}
		} else if ltpGainQ14 > vPitchGainStartMaxQ14 {
			tmp := int32(vPitchGainStartMaxQ14) << 14
			scaleQ14 := tmp / max(ltpGainQ14, 1)
			for i := range ltpOrder {
				psPLC.ltpCoefQ14[i] = int16(smulbb(int32(psPLC.ltpCoefQ14[i]), scaleQ14) >> 14)
			}
		}
	} else {
		psPLC.pitchLQ8 = smulbb(int32(psDec.fsKHz), 18) << 8
		psPLC.ltpCoefQ14 = [ltpOrder]int16{}
	}

	// Save LPC coefficients.
	copy(psPLC.prevLPCQ12[:psDec.lpcOrder], psDecCtrl.predCoefQ12[1][:psDec.lpcOrder])
	psPLC.prevLTPScaleQ14 = int16(psDecCtrl.ltpScaleQ14)

	// Save last two gains.
	copy(psPLC.prevGainQ16[:], psDecCtrl.gainsQ16[psDec.nbSubfr-2:psDec.nbSubfr])

	psPLC.subfrLength = psDec.subfrLength
	psPLC.nbSubfr = psDec.nbSubfr
}

// plcEnergy is silk_PLC_energy: the energies of the last two subframes of
// excitation, scaled by their gains.
func plcEnergy(excQ14 []int32, prevGainQ10 *[2]int32, subfrLength, nbSubfr int) (energy1 int32, shift1 int, energy2 int32, shift2 int) {
	var excBuf [2 * maxSubFrameLength]int16

	// Find random noise component. Scale previous excitation signal.
	excBufPtr := excBuf[:]
	for k := range 2 {
		exc := excQ14[(k+nbSubfr-2)*subfrLength:][:subfrLength]
		for i := range exc {
			excBufPtr[i] = sat16(smulww(exc[i], prevGainQ10[k]) >> 8)
		}
		excBufPtr = excBufPtr[subfrLength:]
	}
	// Find the subframe with lowest energy of the last two and use that as
	// random noise generator.
	energy1, shift1 = sumSqrShift(excBuf[:], subfrLength)
	energy2, shift2 = sumSqrShift(excBuf[subfrLength:], subfrLength)
	return energy1, shift1, energy2, shift2
}

// plcConceal is silk_PLC_conceal: it extrapolates a lost frame into frame
// from the last decoded excitation, pitch and LPC parameters.
func plcConceal(psDec *decoderState, psDecCtrl *decoderControl, frame []int16) {
	var (
		aQ12        [maxLPCOrder]int16
		prevGainQ10 [2]int32
		// libopus allocates these on the stack for each frame.
		sLTP    [maxLTPMemLength]int16
		sLTPQ14 [maxLTPMemLength + maxFrameLength]int32
	)
	psPLC := &psDec.sPLC

	prevGainQ10[0] = psPLC.prevGainQ16[0] >> 6
	prevGainQ10[1] = psPLC.prevGainQ16[1] >> 6

	if psDec.firstFrameAfterReset != 0 {
		psPLC.prevLPCQ12 = [maxLPCOrder]int16{}
	}

	energy1, shift1, energy2, shift2 := plcEnergy(psDec.excQ14[:], &prevGainQ10, psDec.subfrLength, psDec.nbSubfr)

	var randPtr []int32
	if energy1>>shift2 < energy2>>shift1 {
		// First sub-frame has lowest energy.
		randPtr = psDec.excQ14[max(0, (psPLC.nbSubfr-1)*psPLC.subfrLength-randBufSize):]
	} else {
		// Second sub-frame has lowest energy.
		randPtr = psDec.excQ14[max(0, psPLC.nbSubfr*psPLC.subfrLength-randBufSize):]
	}
	randPtr = randPtr[:randBufSize]

	// Set up Gain to random noise component.
	bQ14 := psPLC.ltpCoefQ14[:]
	randScaleQ14 := psPLC.randScaleQ14

	// Set up attenuation gains.
	harmGainQ15 := int32(harmAttQ15[min(nbAtt-1, psDec.lossCnt)])
	var randGainQ15 int32
	if psDec.prevSignalType == typeVoiced {
		randGainQ15 = int32(plcRandAttenuateVQ15[min(nbAtt-1, psDec.lossCnt)])
	} else {
		randGainQ15 = int32(plcRandAttenuateUVQ15[min(nbAtt-1, psDec.lossCnt)])
	}

	// LPC concealment. Apply BWE to previous LPC.
	bwexpander(psPLC.prevLPCQ12[:], psDec.lpcOrder, plcBWECoefQ16)

	// Preload LPC coefficients to array on stack.
	copy(aQ12[:psDec.lpcOrder], psPLC.prevLPCQ12[:psDec.lpcOrder])

	// First lost frame.
	if psDec.lossCnt == 0 {
		randScaleQ14 = 1 << 14

		// Reduce random noise Gain for voiced frames.
		if psDec.prevSignalType == typeVoiced {
			for i := range ltpOrder {
				randScaleQ14 -= bQ14[i]
			}
			randScaleQ14 = max(3277, randScaleQ14) // 0.2
			randScaleQ14 = int16(smulbb(int32(randScaleQ14), int32(psPLC.prevLTPScaleQ14)) >> 14)
		} else {
			// Reduce random noise for unvoiced frames with high LPC gain.
			invGainQ30 := lpcInversePredGain(psPLC.prevLPCQ12[:], psDec.lpcOrder)

			downScaleQ30 := min(int32(1<<30)>>log2InvLPCGainHighThres, invGainQ30)
			downScaleQ30 = max(int32(1<<30)>>log2InvLPCGainLowThres, downScaleQ30)
			downScaleQ30 <<= log2InvLPCGainHighThres

			randGainQ15 = smulwb(downScaleQ30, randGainQ15) >> 14
		}
	}

	randSeed := psPLC.randSeed
	lag := int(rshiftRound(psPLC.pitchLQ8, 8))
	sLTPBufIdx := psDec.ltpMemLength

	// Rewhiten LTP state.
	idx := psDec.ltpMemLength - lag - psDec.lpcOrder - ltpOrder/2
	lpcAnalysisFilter(sLTP[idx:], psDec.outBuf[idx:], aQ12[:], psDec.ltpMemLength-idx, psDec.lpcOrder)
	// Scale LTP state.
	invGainQ30 := inverse32VarQ(psPLC.prevGainQ16[1], 46)
	invGainQ30 = min(invGainQ30, int32Max>>1)
	for i := idx + psDec.lpcOrder; i < psDec.ltpMemLength; i++ {
		sLTPQ14[i] = smulwb(invGainQ30, int32(sLTP[i]))
	}

	// LTP synthesis filtering.
	for range psDec.nbSubfr {
		// Set up pointer.
		predLag := sLTPBufIdx - lag + ltpOrder/2
		b0, b1, b2, b3, b4 := int32(bQ14[0]), int32(bQ14[1]), int32(bQ14[2]), int32(bQ14[3]), int32(bQ14[4])
		for range psDec.subfrLength {
			// Avoids introducing a bias because silk_SMLAWB() always rounds
			// to -inf.
			ltpPredQ12 := int32(2)
			ltpPredQ12 = smlawb(ltpPredQ12, sLTPQ14[predLag], b0)
			ltpPredQ12 = smlawb(ltpPredQ12, sLTPQ14[predLag-1], b1)
			ltpPredQ12 = smlawb(ltpPredQ12, sLTPQ14[predLag-2], b2)
			ltpPredQ12 = smlawb(ltpPredQ12, sLTPQ14[predLag-3], b3)
			ltpPredQ12 = smlawb(ltpPredQ12, sLTPQ14[predLag-4], b4)
			predLag++

			// Generate LPC excitation.
			randSeed = randNext(randSeed)
			idx = int(randSeed>>25) & randBufMask
			sLTPQ14[sLTPBufIdx] = smlawb(ltpPredQ12, randPtr[idx], int32(randScaleQ14)) << 2
			sLTPBufIdx++
		}

		// Gradually reduce LTP gain.
		for j := range ltpOrder {
			bQ14[j] = int16(smulbb(harmGainQ15, int32(bQ14[j])) >> 15)
		}
		// Gradually reduce excitation gain.
		randScaleQ14 = int16(smulbb(int32(randScaleQ14), randGainQ15) >> 15)

		// Slowly increase pitch lag.
		psPLC.pitchLQ8 = smlawb(psPLC.pitchLQ8, psPLC.pitchLQ8, pitchDriftFacQ16)
		psPLC.pitchLQ8 = min(psPLC.pitchLQ8, smulbb(maxPitchLagMs, int32(psDec.fsKHz))<<8)
		lag = int(rshiftRound(psPLC.pitchLQ8, 8))
	}

	// LPC synthesis filtering.
	sLPCQ14Ptr := sLTPQ14[psDec.ltpMemLength-maxLPCOrder:]

	// Copy LPC state.
	copy(sLPCQ14Ptr[:maxLPCOrder], psDec.sLPCQ14Buf[:])

	for i := 0; i < psDec.frameLength; i++ {
		// Partly unrolled. Avoids introducing a bias because silk_SMLAWB()
		// always rounds to -inf.
		s := sLPCQ14Ptr[i : i+maxLPCOrder]
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
		for j := 10; j < psDec.lpcOrder; j++ {
			lpcPredQ10 = smlawb(lpcPredQ10, s[maxLPCOrder-j-1], int32(aQ12[j]))
		}

		// Add prediction to LPC excitation.
		sLPCQ14Ptr[maxLPCOrder+i] = addSat32(sLPCQ14Ptr[maxLPCOrder+i], lshiftSat32(lpcPredQ10, 4))

		// Scale with Gain.
		frame[i] = sat16(int32(sat16(rshiftRound(smulww(sLPCQ14Ptr[maxLPCOrder+i], prevGainQ10[1]), 8))))
	}

	// Save LPC state.
	copy(psDec.sLPCQ14Buf[:], sLPCQ14Ptr[psDec.frameLength:psDec.frameLength+maxLPCOrder])

	// Update states.
	psPLC.randSeed = randSeed
	psPLC.randScaleQ14 = randScaleQ14
	for i := range maxNbSubfr {
		psDecCtrl.pitchL[i] = lag
	}
}

// plcGlueFrames is silk_PLC_glue_frames: it glues concealed frames with new
// good received frames, fading in the energy difference.
func plcGlueFrames(psDec *decoderState, frame []int16, length int) {
	psPLC := &psDec.sPLC

	if psDec.lossCnt != 0 {
		// Calculate energy in concealed residual.
		psPLC.concEnergy, psPLC.concEnergyShift = sumSqrShift(frame, length)

		psPLC.lastFrameLost = 1
	} else {
		if psDec.sPLC.lastFrameLost != 0 {
			// Calculate residual in decoded signal if last frame was lost.
			energy, energyShift := sumSqrShift(frame, length)

			// Normalize energies.
			if energyShift > psPLC.concEnergyShift {
				psPLC.concEnergy >>= energyShift - psPLC.concEnergyShift
			} else if energyShift < psPLC.concEnergyShift {
				energy >>= psPLC.concEnergyShift - energyShift
			}

			// Fade in the energy difference.
			if energy > psPLC.concEnergy {
				lz := clz32(psPLC.concEnergy)
				lz--
				psPLC.concEnergy <<= lz
				energy >>= max(24-lz, 0)

				fracQ24 := psPLC.concEnergy / max(energy, 1)

				gainQ16 := sqrtApprox(fracQ24) << 4
				slopeQ16 := (int32(1)<<16 - gainQ16) / int32(length)
				// Make slope 4x steeper to avoid missing onsets after DTX.
				slopeQ16 <<= 2

				for i := range length {
					frame[i] = int16(smulwb(gainQ16, int32(frame[i])))
					gainQ16 += slopeQ16
					if gainQ16 > 1<<16 {
						break
					}
				}
			}
		}
		psPLC.lastFrameLost = 0
	}
}
