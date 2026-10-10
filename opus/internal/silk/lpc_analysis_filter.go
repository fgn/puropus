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
// Ported from libopus 1.5.2 silk/LPC_analysis_filter.c (the non-celt_fir path).

package silk

// lpcAnalysisFilter is silk_LPC_analysis_filter: the MA prediction error
// filter of order d (even, at least 6) with the Q12 coefficients b (B), over
// length samples of in. The filter starts with zero state: the first d output
// samples are set to zero. Wrap-around in the accumulator is allowed, as in
// libopus (only invalid streams trigger it, and two wraps can cancel).
//
// The decoder's orders, 16 and 10, take specialized loops: the products are
// formed independently and the sum wraps in opus_int32, so any order of the
// additions gives libopus's result.
func lpcAnalysisFilter(out []int16, in []int16, b []int16, length, d int) {
	switch {
	case d == 16 && length > d:
		lpcAnalysisFilter16(out[:length], in[:length], (*[16]int16)(b))
	case d == 10 && length > d:
		lpcAnalysisFilter10(out[:length], in[:length], (*[10]int16)(b))
	default:
		lpcAnalysisFilterGeneric(out, in, b, length, d)
	}
}

// lpcAnalysisFilterGeneric is the loop of silk_LPC_analysis_filter for any
// order.
func lpcAnalysisFilterGeneric(out []int16, in []int16, b []int16, length, d int) {
	_ = b[d-1]
	for ix := d; ix < length; ix++ {
		inPtr := in[ix-d : ix+1] // in_ptr[-j] in libopus is inPtr[d-1-j]
		out32Q12 := smulbb(int32(inPtr[d-1]), int32(b[0]))
		out32Q12 = smlabb(out32Q12, int32(inPtr[d-2]), int32(b[1]))
		out32Q12 = smlabb(out32Q12, int32(inPtr[d-3]), int32(b[2]))
		out32Q12 = smlabb(out32Q12, int32(inPtr[d-4]), int32(b[3]))
		out32Q12 = smlabb(out32Q12, int32(inPtr[d-5]), int32(b[4]))
		out32Q12 = smlabb(out32Q12, int32(inPtr[d-6]), int32(b[5]))
		for j := 6; j < d; j += 2 {
			out32Q12 = smlabb(out32Q12, int32(inPtr[d-1-j]), int32(b[j]))
			out32Q12 = smlabb(out32Q12, int32(inPtr[d-2-j]), int32(b[j+1]))
		}

		// Subtract prediction.
		out32Q12 = int32(inPtr[d])<<12 - out32Q12

		// Scale to Q0.
		out32 := rshiftRound(out32Q12, 12)

		// Saturate output.
		out[ix] = sat16(out32)
	}

	// Set first d output samples to zero.
	clear(out[:d])
}

// lpcAnalysisFilter16 is silk_LPC_analysis_filter of order 16 over
// len(in) > 16 samples. The window slides along in and out so that its
// base stays in a register, and the products are written out in order (see
// lpcSynthesis16).
func lpcAnalysisFilter16(out []int16, in []int16, b *[16]int16) {
	w, o := in, out[16:len(in)]
	for i := range o {
		s := (*[17]int16)(w)
		p0 := int32(s[15])*int32(b[0]) + int32(s[14])*int32(b[1])
		p1 := int32(s[13])*int32(b[2]) + int32(s[12])*int32(b[3])
		p2 := int32(s[11])*int32(b[4]) + int32(s[10])*int32(b[5])
		p3 := int32(s[9])*int32(b[6]) + int32(s[8])*int32(b[7])
		q0 := (p0 + p1) + (p2 + p3)
		p4 := int32(s[7])*int32(b[8]) + int32(s[6])*int32(b[9])
		p5 := int32(s[5])*int32(b[10]) + int32(s[4])*int32(b[11])
		p6 := int32(s[3])*int32(b[12]) + int32(s[2])*int32(b[13])
		p7 := int32(s[1])*int32(b[14]) + int32(s[0])*int32(b[15])
		q1 := (p4 + p5) + (p6 + p7)
		// Subtract prediction, scale to Q0 and saturate.
		o[i] = sat16(rshiftRound(int32(s[16])<<12-(q0+q1), 12))
		w = w[1:]
	}
	clear(out[:16])
}

// lpcAnalysisFilter10 is lpcAnalysisFilter16 for order 10.
func lpcAnalysisFilter10(out []int16, in []int16, b *[10]int16) {
	w, o := in, out[10:len(in)]
	for i := range o {
		s := (*[11]int16)(w)
		p0 := int32(s[9])*int32(b[0]) + int32(s[8])*int32(b[1])
		p1 := int32(s[7])*int32(b[2]) + int32(s[6])*int32(b[3])
		p2 := int32(s[5])*int32(b[4]) + int32(s[4])*int32(b[5])
		p3 := int32(s[3])*int32(b[6]) + int32(s[2])*int32(b[7])
		p4 := int32(s[1])*int32(b[8]) + int32(s[0])*int32(b[9])
		o[i] = sat16(rshiftRound(int32(s[10])<<12-((p0+p1)+(p2+p3)+p4), 12))
		w = w[1:]
	}
	clear(out[:10])
}
