// Copyright (c) 2010 Xiph.Org Foundation, Skype Limited
// Written by Jean-Marc Valin and Koen Vos
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
//
// - Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//
// - Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER
// OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
// PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
// NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
// Ported from libopus 1.5.2 src/opus_decoder.c (the SILK output in opus_decode_frame).

package opus

// opus_decode_frame adds the SILK output, 16-bit PCM, to the CELT output
// in the float build: pcm[i] = pcm[i] + (1.f/32768.f)*pcm_silk[i]. The
// product of an int16 with 1/32768 is exact and never -0, so on the zeroed
// output of a SILK-only frame the sum is the product itself.

// silkToFloatScalar sets pcm to the SILK output x of a frame without CELT
// output, as opus_decode_frame adds it to the zeroed pcm. silkToFloat
// (silk_pcm_*.go) runs a vector kernel where the CPU has one, otherwise
// this.
func silkToFloatScalar(pcm []float32, x []int16) {
	x = x[:len(pcm)]
	for i, v := range x {
		pcm[i] = (1.0 / 32768) * float32(v)
	}
}

// addSilkScalar adds the SILK output x to the CELT output in pcm; addSilk
// dispatches like silkToFloat.
func addSilkScalar(pcm []float32, x []int16) {
	x = x[:len(pcm)]
	for i, v := range x {
		pcm[i] += float32((1.0 / 32768) * float32(v))
	}
}
