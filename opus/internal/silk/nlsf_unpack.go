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
// Ported from libopus 1.5.2 silk/NLSF_unpack.c.

package silk

// nlsfUnpack is silk_NLSF_unpack: the entropy table indices and predictor
// coefficients of first-stage codebook vector cb1Index.
func nlsfUnpack(ecIx []int16, predQ8 []uint8, psNLSFCB *nlsfCB, cb1Index int) {
	order := int(psNLSFCB.order)
	ecSelPtr := psNLSFCB.ecSel[cb1Index*order/2:]
	for i := 0; i < order; i += 2 {
		entry := ecSelPtr[i/2]
		ecIx[i] = int16(smulbb(int32(entry>>1)&7, 2*nlsfQuantMaxAmplitude+1))
		predQ8[i] = psNLSFCB.predQ8[i+int(entry&1)*(order-1)]
		ecIx[i+1] = int16(smulbb(int32(entry>>5)&7, 2*nlsfQuantMaxAmplitude+1))
		predQ8[i+1] = psNLSFCB.predQ8[i+int((entry>>4)&1)*(order-1)+1]
	}
}
