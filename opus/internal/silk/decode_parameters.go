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
// Ported from libopus 1.5.2 silk/decode_parameters.c.

package silk

// decodeParameters is silk_decode_parameters: it turns the decoded indices
// into gains, prediction coefficients, pitch lags and LTP coefficients.
func decodeParameters(psDec *decoderState, psDecCtrl *decoderControl, condCoding int) {
	var pNLSFQ15, pNLSF0Q15 [maxLPCOrder]int16
	order := psDec.lpcOrder

	// Dequant gains.
	gainsDequant(&psDecCtrl.gainsQ16, &psDec.indices.gainsIndices,
		&psDec.lastGainIndex, condCoding == codeConditionally, psDec.nbSubfr)

	// Decode NLSFs.
	nlsfDecode(pNLSFQ15[:], psDec.indices.nlsfIndices[:], psDec.psNLSFCB)

	// Convert NLSF parameters to AR prediction filter coefficients.
	nlsf2A(psDecCtrl.predCoefQ12[1][:], pNLSFQ15[:], order)

	// If just reset, e.g., because the internal Fs changed, do not allow
	// interpolation: improves the case of packet loss in the first frame
	// after a switch.
	if psDec.firstFrameAfterReset == 1 {
		psDec.indices.nlsfInterpCoefQ2 = 4
	}

	if psDec.indices.nlsfInterpCoefQ2 < 4 {
		// Calculation of the interpolated NLSF0 vector from the
		// interpolation factor, the previous NLSF1, and the current NLSF1.
		for i := 0; i < order; i++ {
			pNLSF0Q15[i] = int16(int32(psDec.prevNLSFQ15[i]) +
				(int32(psDec.indices.nlsfInterpCoefQ2)*(int32(pNLSFQ15[i])-int32(psDec.prevNLSFQ15[i])))>>2)
		}

		// Convert NLSF parameters to AR prediction filter coefficients.
		nlsf2A(psDecCtrl.predCoefQ12[0][:], pNLSF0Q15[:], order)
	} else {
		// Copy LPC coefficients for first half from second half.
		copy(psDecCtrl.predCoefQ12[0][:order], psDecCtrl.predCoefQ12[1][:order])
	}

	copy(psDec.prevNLSFQ15[:order], pNLSFQ15[:order])

	// After a packet loss do BWE of LPC coefs.
	if psDec.lossCnt != 0 {
		bwexpander(psDecCtrl.predCoefQ12[0][:], order, bweAfterLossQ16)
		bwexpander(psDecCtrl.predCoefQ12[1][:], order, bweAfterLossQ16)
	}

	if psDec.indices.signalType == typeVoiced {
		// Decode pitch values.
		decodePitch(psDec.indices.lagIndex, psDec.indices.contourIndex, psDecCtrl.pitchL[:], psDec.fsKHz, psDec.nbSubfr)

		// Decode Codebook Index.
		cbkPtrQ7 := ltpVQPtrsQ7[psDec.indices.perIndex] // set pointer to start of codebook

		for k := 0; k < psDec.nbSubfr; k++ {
			ix := int(psDec.indices.ltpIndex[k])
			for i := range ltpOrder {
				psDecCtrl.ltpCoefQ14[k*ltpOrder+i] = int16(int32(cbkPtrQ7[ix*ltpOrder+i]) << 7)
			}
		}

		// Decode LTP scaling.
		psDecCtrl.ltpScaleQ14 = int32(ltpScalesTableQ14[psDec.indices.ltpScaleIndex])
	} else {
		clear(psDecCtrl.pitchL[:psDec.nbSubfr])
		clear(psDecCtrl.ltpCoefQ14[:ltpOrder*psDec.nbSubfr])
		psDec.indices.perIndex = 0
		psDecCtrl.ltpScaleQ14 = 0
	}
}
