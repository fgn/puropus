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
// Ported from libopus 1.5.2 silk/LPC_inv_pred_gain.c (the generic C path).

package silk

const (
	// invPredGainQA is QA in LPC_inv_pred_gain.c.
	invPredGainQA = 24
	// invPredGainALimit is A_LIMIT: SILK_FIX_CONST( 0.99975, QA ).
	invPredGainALimit = 16773022
)

// mul32FracQ is MUL32_FRAC_Q: (a32 * b32) >> Q, rounded.
func mul32FracQ(a32, b32 int32, q int) int32 {
	return int32(rshiftRound64(smull(a32, b32), q))
}

// lpcInversePredGainQA is LPC_inverse_pred_gain_QA_c: the inverse prediction
// gain in the energy domain (Q30) of the coefficients aQA (Q24, modified in
// place), or 0 if the filter is unstable.
func lpcInversePredGainQA(aQA *[silkMaxOrderLPC]int32, order int) int32 {
	var k int

	invGainQ30 := int32(oneQ30)
	for k = order - 1; k > 0; k-- {
		// Check for stability.
		if aQA[k] > invPredGainALimit || aQA[k] < -invPredGainALimit {
			return 0
		}

		// Set RC equal to negated AR coef.
		rcQ31 := -(aQA[k] << (31 - invPredGainQA))

		// rcMult1Q30 range: [ 1 : 2^30 ].
		rcMult1Q30 := oneQ30 - smmul(rcQ31, rcQ31)

		// Update inverse gain. invGainQ30 range: [ 0 : 2^30 ].
		invGainQ30 = smmul(invGainQ30, rcMult1Q30) << 2
		if invGainQ30 < invMaxPredictionGainQ30 {
			return 0
		}

		// rcMult2 range: [ 2^30 : silk_int32_MAX ].
		mult2Q := 32 - clz32(abs32(rcMult1Q30))
		rcMult2 := inverse32VarQ(rcMult1Q30, mult2Q+30)

		// Update AR coefficient.
		for n := 0; n < (k+1)>>1; n++ {
			tmp1 := aQA[n]
			tmp2 := aQA[k-n-1]
			tmp64 := rshiftRound64(smull(subSat32(tmp1, mul32FracQ(tmp2, rcQ31, 31)), rcMult2), mult2Q)
			if tmp64 > int32Max || tmp64 < int32Min {
				return 0
			}
			aQA[n] = int32(tmp64)
			tmp64 = rshiftRound64(smull(subSat32(tmp2, mul32FracQ(tmp1, rcQ31, 31)), rcMult2), mult2Q)
			if tmp64 > int32Max || tmp64 < int32Min {
				return 0
			}
			aQA[k-n-1] = int32(tmp64)
		}
	}

	// Check for stability.
	if aQA[k] > invPredGainALimit || aQA[k] < -invPredGainALimit {
		return 0
	}

	// Set RC equal to negated AR coef.
	rcQ31 := -(aQA[0] << (31 - invPredGainQA))

	// Range: [ 1 : 2^30 ].
	rcMult1Q30 := oneQ30 - smmul(rcQ31, rcQ31)

	// Update inverse gain. Range: [ 0 : 2^30 ].
	invGainQ30 = smmul(invGainQ30, rcMult1Q30) << 2
	if invGainQ30 < invMaxPredictionGainQ30 {
		return 0
	}

	return invGainQ30
}

// lpcInversePredGain is silk_LPC_inverse_pred_gain_c: the inverse prediction
// gain in the energy domain (Q30) of the prediction coefficients aQ12, or 0
// if the filter is unstable (all poles not within the unit circle).
func lpcInversePredGain(aQ12 []int16, order int) int32 {
	var atmpQA [silkMaxOrderLPC]int32
	var dcResp int32

	// Increase Q domain of the AR coefficients.
	for k := range order {
		dcResp += int32(aQ12[k])
		atmpQA[k] = int32(aQ12[k]) << (invPredGainQA - 12)
	}
	// If the DC is unstable, we don't even need to do the full
	// calculations.
	if dcResp >= 4096 {
		return 0
	}
	return lpcInversePredGainQA(&atmpQA, order)
}
