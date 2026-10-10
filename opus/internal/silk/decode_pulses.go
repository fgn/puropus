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
// Ported from libopus 1.5.2 silk/decode_pulses.c.

package silk

import "github.com/fgn/puropus/opus/internal/entcode"

// decodePulses is silk_decode_pulses: it decodes the quantization indices of
// the excitation of one frame into pulses, which must hold frameLength
// rounded up to a multiple of 16 values.
func decodePulses(psRangeDec *entcode.Decoder, pulses []int16, signalType, quantOffsetType, frameLength int) {
	var sumPulses, nLshifts [maxNbShellBlocks]int

	// Decode rate level.
	rateLevelIndex := psRangeDec.DecICDF(rateLevelsICDF[signalType>>1][:], 8)

	// Calculate number of shell blocks.
	iter := frameLength >> log2ShellCodecFrameLength
	if iter*shellCodecFrameLength < frameLength {
		iter++ // Only happens for 10 ms @ 12 kHz.
	}

	// Sum-Weighted-Pulses Decoding.
	cdfPtr := pulsesPerBlockICDF[rateLevelIndex][:]
	for i := range iter {
		nLshifts[i] = 0
		sumPulses[i] = psRangeDec.DecICDF(cdfPtr, 8)

		// LSB indication.
		for sumPulses[i] == silkMaxPulses+1 {
			nLshifts[i]++
			// When we've already got 10 LSBs, we shift the table to not
			// allow (SILK_MAX_PULSES + 1).
			shift := 0
			if nLshifts[i] == 10 {
				shift = 1
			}
			sumPulses[i] = psRangeDec.DecICDF(pulsesPerBlockICDF[nRateLevels-1][shift:], 8)
		}
	}

	// Shell decoding.
	for i := range iter {
		block := pulses[i*shellCodecFrameLength : (i+1)*shellCodecFrameLength]
		if sumPulses[i] > 0 {
			shellDecoder(block, psRangeDec, sumPulses[i])
		} else {
			clear(block)
		}
	}

	// LSB Decoding.
	for i := range iter {
		if nLshifts[i] > 0 {
			nLS := nLshifts[i]
			pulsesPtr := pulses[i*shellCodecFrameLength : (i+1)*shellCodecFrameLength]
			for k := range pulsesPtr {
				absQ := int(pulsesPtr[k])
				for range nLS {
					absQ <<= 1
					absQ += psRangeDec.DecICDF2(lsbICDF[0], 8)
				}
				pulsesPtr[k] = int16(absQ)
			}
			// Mark the number of pulses non-zero for sign decoding.
			sumPulses[i] |= nLS << 5
		}
	}

	// Decode and add signs to pulse signal.
	decodeSigns(psRangeDec, pulses, frameLength, signalType, quantOffsetType, &sumPulses)
}
