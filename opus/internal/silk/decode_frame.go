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
// Ported from libopus 1.5.2 silk/decode_frame.c.

package silk

import "github.com/fgn/puropus/opus/internal/entcode"

// decodeFrame is silk_decode_frame: it decodes or conceals one frame of a
// channel into pOut and returns its length in samples.
func decodeFrame(psDec *decoderState, psRangeDec *entcode.Decoder, pOut []int16, lostFlag, condCoding int) int {
	var psDecCtrl decoderControl

	L := psDec.frameLength
	psDecCtrl.ltpScaleQ14 = 0

	if lostFlag == FlagDecodeNormal ||
		(lostFlag == FlagDecodeLBRR && psDec.lbrrFlags[psDec.nFramesDecoded] == 1) {
		// (L + SHELL_CODEC_FRAME_LENGTH - 1) & ~(SHELL_CODEC_FRAME_LENGTH - 1)
		// pulses, at most MAX_FRAME_LENGTH.
		var pulses [maxFrameLength]int16

		// Decode quantization indices of side info.
		decodeIndices(psDec, psRangeDec, psDec.nFramesDecoded, lostFlag, condCoding)

		// Decode quantization indices of excitation.
		decodePulses(psRangeDec, pulses[:], int(psDec.indices.signalType),
			int(psDec.indices.quantOffsetType), psDec.frameLength)

		// Decode parameters and pulse signal.
		decodeParameters(psDec, &psDecCtrl, condCoding)

		// Run inverse NSQ.
		decodeCore(psDec, &psDecCtrl, pOut, &pulses)

		// Update output buffer.
		mvLen := psDec.ltpMemLength - psDec.frameLength
		copy(psDec.outBuf[:mvLen], psDec.outBuf[psDec.frameLength:psDec.frameLength+mvLen])
		copy(psDec.outBuf[mvLen:mvLen+psDec.frameLength], pOut[:psDec.frameLength])

		// Update PLC state.
		plc(psDec, &psDecCtrl, pOut, false)

		psDec.lossCnt = 0
		psDec.prevSignalType = int(psDec.indices.signalType)

		// A frame has been decoded without errors.
		psDec.firstFrameAfterReset = 0
	} else {
		// Handle packet loss by extrapolation.
		plc(psDec, &psDecCtrl, pOut, true)

		// Update output buffer.
		mvLen := psDec.ltpMemLength - psDec.frameLength
		copy(psDec.outBuf[:mvLen], psDec.outBuf[psDec.frameLength:psDec.frameLength+mvLen])
		copy(psDec.outBuf[mvLen:mvLen+psDec.frameLength], pOut[:psDec.frameLength])
	}

	// Comfort noise generation / estimation.
	cng(psDec, &psDecCtrl, pOut, L)

	// Ensure smooth connection of extrapolated and good frames.
	plcGlueFrames(psDec, pOut, L)

	// Update some decoder state variables.
	psDec.lagPrev = psDecCtrl.pitchL[psDec.nbSubfr-1]

	// Set output frame length.
	return L
}
