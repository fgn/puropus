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
// Ported from libopus 1.5.2 silk/NLSF_decode.c.

package silk

// nlsfResidualDequant is silk_NLSF_residual_dequant: the predictive
// dequantizer of the NLSF residuals.
func nlsfResidualDequant(xQ10 []int16, indices []int8, predCoefQ8 []uint8, quantStepSizeQ16 int32, order int) {
	var outQ10, predQ10 int32

	for i := order - 1; i >= 0; i-- {
		predQ10 = smulbb(outQ10, int32(predCoefQ8[i])) >> 8
		outQ10 = int32(indices[i]) << 10
		if outQ10 > 0 {
			outQ10 -= nlsfQuantLevelAdjQ10
		} else if outQ10 < 0 {
			outQ10 += nlsfQuantLevelAdjQ10
		}
		outQ10 = smlawb(predQ10, outQ10, quantStepSizeQ16)
		xQ10[i] = int16(outQ10)
	}
}

// nlsfDecode is silk_NLSF_decode: the NLSF vector of the codebook path
// nlsfIndices (the first-stage index followed by order residual indices).
func nlsfDecode(pNLSFQ15 []int16, nlsfIndices []int8, psNLSFCB *nlsfCB) {
	var (
		predQ8 [maxLPCOrder]uint8
		ecIx   [maxLPCOrder]int16
		resQ10 [maxLPCOrder]int16
	)
	order := int(psNLSFCB.order)

	// Unpack entropy table indices and predictor for current CB1 index.
	nlsfUnpack(ecIx[:], predQ8[:], psNLSFCB, int(nlsfIndices[0]))

	// Predictive residual dequantizer.
	nlsfResidualDequant(resQ10[:], nlsfIndices[1:], predQ8[:], int32(psNLSFCB.quantStepSizeQ16), order)

	// Apply inverse square-rooted weights to first stage and add to output.
	pCBElement := psNLSFCB.cb1NLSFQ8[int(nlsfIndices[0])*order:]
	pCBWghtQ9 := psNLSFCB.cb1WghtQ9[int(nlsfIndices[0])*order:]
	for i := range order {
		nlsfQ15Tmp := (int32(resQ10[i])<<14)/int32(pCBWghtQ9[i]) + int32(pCBElement[i])<<7
		pNLSFQ15[i] = int16(limit(nlsfQ15Tmp, 0, 32767))
	}

	// NLSF stabilization.
	nlsfStabilize(pNLSFQ15, psNLSFCB.deltaMinQ15, order)
}
