// Copyright (c) 2010 Xiph.Org Foundation, Skype Limited (src/opus_decoder.c)
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
// Copyright (c) 2006-2011, Skype Limited. All rights reserved. (silk/dec_API.c)
//
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
// Ported from libopus 1.5.2 src/opus_decoder.c (opus_packet_has_lbrr) and the
// header flag order of silk/dec_API.c (silk_Decode).

package opus

import "github.com/fgn/puropus/opus/internal/entcode"

// HasLBRR reports whether the first frame of a SILK or hybrid packet carries
// low-bitrate redundancy (in-band FEC) data (RFC 6716 §4.2.3). It is false for
// CELT packets and for packets that do not parse.
//
// It decodes the start of the SILK layer as silk_Decode does: for each coded
// channel, one VAD flag per 20 ms SILK frame (one for a 10 ms packet) and
// then the channel's LBRR flag, each a range-coded bit of probability 1/2
// (RFC 6716 §4.2.3-4.2.4). The result is true when any channel's LBRR flag
// is set, as in libopus's opus_packet_has_lbrr. A first frame of 0 or 1
// bytes is false: opus_decode_frame treats it as lost or DTX and decodes no
// SILK data from it.
func HasLBRR(data []byte) bool {
	p, err := Parse(data, false)
	if err != nil || p.Mode == ModeCELT || p.FrameSize[0] <= 1 {
		return false
	}
	framesPerPacket := 1
	if p.FrameDuration > 960 {
		framesPerPacket = p.FrameDuration / 960
	}
	channels := 1
	if p.Stereo {
		channels = 2
	}
	var dec entcode.Decoder
	dec.Init(data[p.FrameOffset[0] : p.FrameOffset[0]+p.FrameSize[0]])
	lbrr := 0
	for range channels {
		for range framesPerPacket {
			dec.DecBitLogp(1) // VAD flag
		}
		lbrr |= dec.DecBitLogp(1)
	}
	return lbrr != 0
}
