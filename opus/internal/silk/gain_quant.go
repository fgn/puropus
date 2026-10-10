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
// Ported from libopus 1.5.2 silk/gain_quant.c (silk_gains_dequant).

package silk

const (
	// gainOffset is OFFSET in gain_quant.c.
	gainOffset = (minQGainDB*128)/6 + 16*128
	// gainInvScaleQ16 is INV_SCALE_Q16 in gain_quant.c.
	gainInvScaleQ16 = (65536 * (((maxQGainDB - minQGainDB) * 128) / 6)) / (nLevelsQGain - 1)
)

// gainsDequant is silk_gains_dequant: the gains of nbSubfr subframes from
// their indices, uniform on the log scale. prevInd is the last index of the
// previous frame; the first gain is delta coded if conditional.
func gainsDequant(gainQ16 *[maxNbSubfr]int32, ind *[maxNbSubfr]int8, prevInd *int8, conditional bool, nbSubfr int) {
	for k := 0; k < nbSubfr; k++ {
		if k == 0 && !conditional {
			// Gain index is not allowed to go down more than 16 steps
			// (~21.8 dB).
			*prevInd = int8(max(int(ind[k]), int(*prevInd)-16))
		} else {
			// Delta index.
			indTmp := int(ind[k]) + minDeltaGainQuant

			// Accumulate deltas.
			doubleStepSizeThreshold := 2*maxDeltaGainQuant - nLevelsQGain + int(*prevInd)
			if indTmp > doubleStepSizeThreshold {
				*prevInd = int8(int(*prevInd) + indTmp<<1 - doubleStepSizeThreshold)
			} else {
				*prevInd = int8(int(*prevInd) + indTmp)
			}
		}
		*prevInd = int8(limit(int(*prevInd), 0, nLevelsQGain-1))

		// Scale and convert to linear scale.
		gainQ16[k] = log2lin(min(smulwb(gainInvScaleQ16, int32(*prevInd))+gainOffset, 3967)) // 3967 = 31 in Q7
	}
}
