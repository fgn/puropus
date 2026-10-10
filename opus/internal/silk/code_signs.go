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
// Ported from libopus 1.5.2 silk/code_signs.c (the decoder side).

package silk

import (
	"math/bits"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// decodeSigns is silk_decode_signs: it decodes the signs of the nonzero
// pulses of each shell block whose sumPulses entry is positive.
//
// The pulses of a block are non-negative here and below 17408 (16 pulses
// with ten LSBs each), so a block's positions with a pulse are found
// without a branch and visited by their set bits, instead of a test per
// position that the branch predictor tends to miss at each pulse.
func decodeSigns(psRangeDec *entcode.Decoder, pulses []int16, length, signalType, quantOffsetType int, sumPulses *[maxNbShellBlocks]int) {
	icdfPtr := signICDF[smulbb(7, int32(quantOffsetType+(signalType<<1))):]
	length = (length + shellCodecFrameLength/2) >> log2ShellCodecFrameLength
	for i := range length {
		qPtr := (*[shellCodecFrameLength]int16)(pulses[i*shellCodecFrameLength:])
		p := sumPulses[i]
		if p > 0 {
			// The table is {icdf[0], 0}.
			icdf0 := icdfPtr[min(p&0x1F, 6)]
			var nonzero uint32
			for j, q := range qPtr {
				// 1 when q > 0, for 0 <= q < 32768.
				nonzero |= (uint32(q) + 0x7fff) >> 15 << j
			}
			for ; nonzero != 0; nonzero &= nonzero - 1 {
				j := bits.TrailingZeros32(nonzero)
				// Attach the sign: silk_dec_map(a) = (a << 1) - 1, so a = 0
				// negates; neg is then all ones.
				neg := int16(psRangeDec.DecICDF2(icdf0, 8) - 1)
				qPtr[j] = (qPtr[j] ^ neg) - neg
			}
		}
	}
}
