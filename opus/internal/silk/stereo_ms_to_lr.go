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
// Ported from libopus 1.5.2 silk/stereo_MS_to_LR.c.

package silk

// stereoMSToLR is silk_stereo_MS_to_LR: it converts the adaptive mid/side
// representation in x1 (mid) and x2 (side) to left/right in place. Both hold
// two samples of history followed by frameLength samples.
func stereoMSToLR(state *stereoDecState, x1, x2 []int16, predQ13 *[2]int32, fsKHz, frameLength int) {
	x1 = x1[:frameLength+2]
	x2 = x2[:frameLength+2]

	// Buffering.
	copy(x1[:2], state.sMid[:])
	copy(x2[:2], state.sSide[:])
	copy(state.sMid[:], x1[frameLength:])
	copy(state.sSide[:], x2[frameLength:])

	// Interpolate predictors and add prediction to side channel.
	pred0Q13 := int32(state.predPrevQ13[0])
	pred1Q13 := int32(state.predPrevQ13[1])
	denomQ16 := int32(1<<16) / int32(stereoInterpLenMs*fsKHz)
	delta0Q13 := rshiftRound(smulbb(predQ13[0]-int32(state.predPrevQ13[0]), denomQ16), 16)
	delta1Q13 := rshiftRound(smulbb(predQ13[1]-int32(state.predPrevQ13[1]), denomQ16), 16)
	interpLen := min(stereoInterpLenMs*fsKHz, frameLength)
	for n := range interpLen {
		pred0Q13 += delta0Q13
		pred1Q13 += delta1Q13
		w := (*[3]int16)(x1[n : n+3])
		sum := (int32(w[0]) + int32(w[2]) + int32(w[1])<<1) << 9 // Q11
		sum = smlawb(int32(x2[n+1])<<8, sum, pred0Q13)           // Q8
		sum = smlawb(sum, int32(w[1])<<11, pred1Q13)             // Q8
		x2[n+1] = sat16(rshiftRound(sum, 8))
	}
	pred0Q13 = predQ13[0]
	pred1Q13 = predQ13[1]
	for n := interpLen; n < frameLength; n++ {
		w := (*[3]int16)(x1[n : n+3])
		sum := (int32(w[0]) + int32(w[2]) + int32(w[1])<<1) << 9 // Q11
		sum = smlawb(int32(x2[n+1])<<8, sum, pred0Q13)           // Q8
		sum = smlawb(sum, int32(w[1])<<11, pred1Q13)             // Q8
		x2[n+1] = sat16(rshiftRound(sum, 8))
	}
	state.predPrevQ13[0] = int16(predQ13[0])
	state.predPrevQ13[1] = int16(predQ13[1])

	// Convert to left/right signals.
	msToLR(x1[1:frameLength+1], x2[1:frameLength+1])
}

// msToLRScalar is the conversion to left/right signals of
// silk_stereo_MS_to_LR: mid becomes mid+side and side mid-side, saturated.
// msToLR (simd_*.go) runs a vector kernel where the CPU has one, otherwise
// this.
func msToLRScalar(mid, side []int16) {
	side = side[:len(mid)]
	for n, m := range mid {
		sum := int32(m) + int32(side[n])
		diff := int32(m) - int32(side[n])
		mid[n] = sat16(sum)
		side[n] = sat16(diff)
	}
}
