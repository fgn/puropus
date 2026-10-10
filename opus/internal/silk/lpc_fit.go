// Copyright (c) 2013, Koen Vos. All rights reserved.
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
// Ported from libopus 1.5.2 silk/LPC_fit.c.

package silk

// lpcFit is silk_LPC_fit: it converts the int32 coefficients aQIN (Q qIn) to
// int16 coefficients aQOUT (Q qOut), bandwidth expanding aQIN until they fit
// without wrap-around.
func lpcFit(aQOUT []int16, aQIN []int32, qOut, qIn, d int) {
	var (
		i, idx   int
		maxabs   int32
		chirpQ16 int32
	)

	// Limit the maximum absolute value of the prediction coefficients, so
	// that they'll fit in int16.
	for i = 0; i < 10; i++ {
		// Find maximum absolute value and its index.
		maxabs = 0
		for k := range d {
			absval := abs32(aQIN[k])
			if absval > maxabs {
				maxabs = absval
				idx = k
			}
		}
		maxabs = rshiftRound(maxabs, qIn-qOut)

		if maxabs > int16Max {
			// Reduce magnitude of prediction coefficients.
			maxabs = min(maxabs, 163838) // (silk_int32_MAX >> 14) + silk_int16_MAX = 163838
			chirpQ16 = lpcFitChirpQ16 - ((maxabs-int16Max)<<14)/((maxabs*int32(idx+1))>>2)
			bwexpander32(aQIN, d, chirpQ16)
		} else {
			break
		}
	}

	if i == 10 {
		// Reached the last iteration, clip the coefficients.
		for k := range d {
			aQOUT[k] = sat16(rshiftRound(aQIN[k], qIn-qOut))
			aQIN[k] = int32(aQOUT[k]) << (qIn - qOut)
		}
	} else {
		for k := range d {
			aQOUT[k] = int16(rshiftRound(aQIN[k], qIn-qOut))
		}
	}
}
