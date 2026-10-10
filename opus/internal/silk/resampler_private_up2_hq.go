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
// Ported from libopus 1.5.2 silk/resampler_private_up2_HQ.c.

package silk

// resamplerPrivateUp2HQScalar is silk_resampler_private_up2_HQ: it upsamples by a
// factor 2 with high quality, using 2nd order allpass filters for the 2x
// upsampling, followed by a notch filter just above Nyquist. s is the state
// (6 values); out receives 2*length samples.
//
// resamplerPrivateUp2HQ (simd_*.go) runs a vector kernel where the CPU has
// one, otherwise this.
func resamplerPrivateUp2HQScalar(s *[resamplerMaxIIROrder]int32, out []int16, in []int16, length int) {
	out = out[:2*length]
	in = in[:length]

	// Internal variables and state are in Q10 format.
	for k := range in {
		// Convert to Q10.
		in32 := int32(in[k]) << 10

		// First all-pass section for even output sample.
		Y := in32 - s[0]
		X := smulwb(Y, resamplerUp2HQ00)
		out32a := s[0] + X
		s[0] = in32 + X

		// Second all-pass section for even output sample.
		Y = out32a - s[1]
		X = smulwb(Y, resamplerUp2HQ01)
		out32b := s[1] + X
		s[1] = out32a + X

		// Third all-pass section for even output sample.
		Y = out32b - s[2]
		X = smlawb(Y, Y, resamplerUp2HQ02)
		out32a = s[2] + X
		s[2] = out32b + X

		// Apply gain in Q15, convert back to int16 and store to output.
		out[2*k] = sat16(rshiftRound(out32a, 10))

		// First all-pass section for odd output sample.
		Y = in32 - s[3]
		X = smulwb(Y, resamplerUp2HQ10)
		out32a = s[3] + X
		s[3] = in32 + X

		// Second all-pass section for odd output sample.
		Y = out32a - s[4]
		X = smulwb(Y, resamplerUp2HQ11)
		out32b = s[4] + X
		s[4] = out32a + X

		// Third all-pass section for odd output sample.
		Y = out32b - s[5]
		X = smlawb(Y, Y, resamplerUp2HQ12)
		out32a = s[5] + X
		s[5] = out32b + X

		// Apply gain in Q15, convert back to int16 and store to output.
		out[2*k+1] = sat16(rshiftRound(out32a, 10))
	}
}

// resamplerPrivateUp2HQWrapper is silk_resampler_private_up2_HQ_wrapper.
func resamplerPrivateUp2HQWrapper(s *resamplerState, out []int16, in []int16, length int) {
	resamplerPrivateUp2HQ(&s.sIIR, out, in, length)
}
