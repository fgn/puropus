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
// Ported from libopus 1.5.2 silk/shell_coder.c (the decoder side).

package silk

import "github.com/fgn/puropus/opus/internal/entcode"

// splitLeft is decode_split for p > 0: it decodes the pulse count of the
// first half of a subtree with p pulses. The row of p in shellTable has
// p+1 entries; a single pulse is a binary symbol, decoded without a branch
// on it.
func splitLeft(psRangeDec *entcode.Decoder, p int, shellTable []uint8) int {
	if p == 1 {
		return psRangeDec.DecICDF2(shellTable[0], 8)
	}
	off := int(shellCodeTableOffsets[p])
	return psRangeDec.DecICDF(shellTable[off:off+p+1], 8)
}

// shellDecoder is silk_shell_decoder: it decodes the nonnegative pulse
// amplitudes of one shell code frame of 16 pulses, pulses4 in total.
//
// libopus decodes the fifteen splits of the binary tree depth first with
// decode_split, which decodes nothing for a subtree without pulses. The
// functions below walk the same tree in the same order, one per level, but
// skip a subtree without pulses at once, and decode a subtree with one
// pulse as the binary splits on the way down to it, whose symbols give the
// pulse's position without a branch. Most subtrees hold no pulse or one.
func shellDecoder(pulses0 []int16, psRangeDec *entcode.Decoder, pulses4 int) {
	q := (*[shellCodecFrameLength]int16)(pulses0)
	*q = [shellCodecFrameLength]int16{}
	switch pulses4 {
	case 0:
	case 1:
		i := 8 * (1 - psRangeDec.DecICDF2(shellCodeTable3[0], 8))
		i += 4 * (1 - psRangeDec.DecICDF2(shellCodeTable2[0], 8))
		i += 2 * (1 - psRangeDec.DecICDF2(shellCodeTable1[0], 8))
		i += 1 - psRangeDec.DecICDF2(shellCodeTable0[0], 8)
		q[i&15] = 1
	default:
		c := splitLeft(psRangeDec, pulses4, shellCodeTable3[:])
		shell8((*[8]int16)(q[:8]), psRangeDec, c)
		shell8((*[8]int16)(q[8:]), psRangeDec, pulses4-c)
	}
}

// shell8 decodes the p pulses of a subtree of eight positions into the
// zeroed q, as shellDecoder.
func shell8(q *[8]int16, psRangeDec *entcode.Decoder, p int) {
	switch p {
	case 0:
	case 1:
		i := 4 * (1 - psRangeDec.DecICDF2(shellCodeTable2[0], 8))
		i += 2 * (1 - psRangeDec.DecICDF2(shellCodeTable1[0], 8))
		i += 1 - psRangeDec.DecICDF2(shellCodeTable0[0], 8)
		q[i&7] = 1
	default:
		c := splitLeft(psRangeDec, p, shellCodeTable2[:])
		shell4((*[4]int16)(q[:4]), psRangeDec, c)
		shell4((*[4]int16)(q[4:]), psRangeDec, p-c)
	}
}

// shell4 decodes the p pulses of a subtree of four positions into the
// zeroed q, as shellDecoder.
func shell4(q *[4]int16, psRangeDec *entcode.Decoder, p int) {
	switch p {
	case 0:
	case 1:
		i := 2 * (1 - psRangeDec.DecICDF2(shellCodeTable1[0], 8))
		i += 1 - psRangeDec.DecICDF2(shellCodeTable0[0], 8)
		q[i&3] = 1
	default:
		c := splitLeft(psRangeDec, p, shellCodeTable1[:])
		shell2((*[2]int16)(q[:2]), psRangeDec, c)
		shell2((*[2]int16)(q[2:]), psRangeDec, p-c)
	}
}

// shell2 decodes the p pulses of a pair of positions into q, as
// shellDecoder.
func shell2(q *[2]int16, psRangeDec *entcode.Decoder, p int) {
	if p > 0 {
		c := splitLeft(psRangeDec, p, shellCodeTable0[:])
		q[0], q[1] = int16(c), int16(p-c)
	}
}
