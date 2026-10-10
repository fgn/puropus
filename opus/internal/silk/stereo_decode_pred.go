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
// Ported from libopus 1.5.2 silk/stereo_decode_pred.c.

package silk

import "github.com/fgn/puropus/opus/internal/entcode"

// stereoDecodePred is silk_stereo_decode_pred: it decodes the mid/side
// predictors.
func stereoDecodePred(psRangeDec *entcode.Decoder, predQ13 *[2]int32) {
	var ix [2][3]int

	// Entropy decoding.
	n := psRangeDec.DecICDF(stereoPredJointICDF[:], 8)
	ix[0][2] = n / 5
	ix[1][2] = n - 5*ix[0][2]
	for n := range 2 {
		ix[n][0] = psRangeDec.DecICDF(uniform3ICDF[:], 8)
		ix[n][1] = psRangeDec.DecICDF(uniform5ICDF[:], 8)
	}

	// Dequantize.
	for n := range 2 {
		ix[n][0] += 3 * ix[n][2]
		lowQ13 := int32(stereoPredQuantQ13[ix[n][0]])
		stepQ13 := smulwb(int32(stereoPredQuantQ13[ix[n][0]+1])-lowQ13, stereoQuantStepQ16)
		predQ13[n] = smlabb(lowQ13, stepQ13, int32(2*ix[n][1]+1))
	}

	// Subtract second from first predictor (helps when actually applying
	// these).
	predQ13[0] -= predQ13[1]
}

// stereoDecodeMidOnly is silk_stereo_decode_mid_only: the flag that only the
// mid channel has been coded.
func stereoDecodeMidOnly(psRangeDec *entcode.Decoder) int {
	return psRangeDec.DecICDF(stereoOnlyCodeMidICDF[:], 8)
}
