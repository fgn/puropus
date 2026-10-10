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
// Ported from libopus 1.5.2 silk/decode_indices.c.

package silk

import "github.com/fgn/puropus/opus/internal/entcode"

// decodeIndices is silk_decode_indices: it decodes the side information of
// frame frameIndex (or of its LBRR copy when decodeLBRR is nonzero).
func decodeIndices(psDec *decoderState, psRangeDec *entcode.Decoder, frameIndex, decodeLBRR, condCoding int) {
	var (
		ix     int
		ecIx   [maxLPCOrder]int16
		predQ8 [maxLPCOrder]uint8
	)
	indices := &psDec.indices

	// Decode signal type and quantizer offset.
	if decodeLBRR != 0 || psDec.vadFlags[frameIndex] != 0 {
		ix = psRangeDec.DecICDF(typeOffsetVADICDF[:], 8) + 2
	} else {
		ix = psRangeDec.DecICDF(typeOffsetNoVADICDF[:], 8)
	}
	indices.signalType = int8(ix >> 1)
	indices.quantOffsetType = int8(ix & 1)

	// Decode gains.
	// First subframe.
	if condCoding == codeConditionally {
		// Conditional coding.
		indices.gainsIndices[0] = int8(psRangeDec.DecICDF(deltaGainICDF[:], 8))
	} else {
		// Independent coding, in two stages: MSB bits followed by 3 LSBs.
		indices.gainsIndices[0] = int8(psRangeDec.DecICDF(gainICDF[indices.signalType][:], 8) << 3)
		indices.gainsIndices[0] += int8(psRangeDec.DecICDF(uniform8ICDF[:], 8))
	}

	// Remaining subframes.
	for i := 1; i < psDec.nbSubfr; i++ {
		indices.gainsIndices[i] = int8(psRangeDec.DecICDF(deltaGainICDF[:], 8))
	}

	// Decode LSF indices.
	cb := psDec.psNLSFCB
	indices.nlsfIndices[0] = int8(psRangeDec.DecICDF(cb.cb1ICDF[(int(indices.signalType)>>1)*int(cb.nVectors):], 8))
	nlsfUnpack(ecIx[:], predQ8[:], cb, int(indices.nlsfIndices[0]))
	for i := 0; i < int(cb.order); i++ {
		ix = psRangeDec.DecICDF(cb.ecICDF[ecIx[i]:], 8)
		if ix == 0 {
			ix -= psRangeDec.DecICDF(nlsfEXTICDF[:], 8)
		} else if ix == 2*nlsfQuantMaxAmplitude {
			ix += psRangeDec.DecICDF(nlsfEXTICDF[:], 8)
		}
		indices.nlsfIndices[i+1] = int8(ix - nlsfQuantMaxAmplitude)
	}

	// Decode LSF interpolation factor.
	if psDec.nbSubfr == maxNbSubfr {
		indices.nlsfInterpCoefQ2 = int8(psRangeDec.DecICDF(nlsfInterpolationFactorICDF[:], 8))
	} else {
		indices.nlsfInterpCoefQ2 = 4
	}

	if indices.signalType == typeVoiced {
		// Decode pitch lags.
		// Get lag index.
		decodeAbsoluteLagIndex := true
		if condCoding == codeConditionally && psDec.ecPrevSignalType == typeVoiced {
			// Decode Delta index.
			deltaLagIndex := int16(psRangeDec.DecICDF(pitchDeltaICDF[:], 8))
			if deltaLagIndex > 0 {
				deltaLagIndex -= 9
				indices.lagIndex = psDec.ecPrevLagIndex + deltaLagIndex
				decodeAbsoluteLagIndex = false
			}
		}
		if decodeAbsoluteLagIndex {
			// Absolute decoding.
			indices.lagIndex = int16(psRangeDec.DecICDF(pitchLagICDF[:], 8) * (psDec.fsKHz >> 1))
			indices.lagIndex += int16(psRangeDec.DecICDF(psDec.pitchLagLowBitsICDF, 8))
		}
		psDec.ecPrevLagIndex = indices.lagIndex

		// Get contour index.
		indices.contourIndex = int8(psRangeDec.DecICDF(psDec.pitchContourICDF, 8))

		// Decode LTP gains.
		// Decode PERIndex value.
		indices.perIndex = int8(psRangeDec.DecICDF(ltpPerIndexICDF[:], 8))

		for k := 0; k < psDec.nbSubfr; k++ {
			indices.ltpIndex[k] = int8(psRangeDec.DecICDF(ltpGainICDFPtrs[indices.perIndex], 8))
		}

		// Decode LTP scaling.
		if condCoding == codeIndependently {
			indices.ltpScaleIndex = int8(psRangeDec.DecICDF(ltpScaleICDF[:], 8))
		} else {
			indices.ltpScaleIndex = 0
		}
	}
	psDec.ecPrevSignalType = int(indices.signalType)

	// Decode seed.
	indices.seed = int8(psRangeDec.DecICDF(uniform4ICDF[:], 8))
}
