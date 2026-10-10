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
// Ported from libopus 1.5.2 silk/decoder_set_fs.c.

package silk

// decoderSetFs is silk_decoder_set_fs: it sets the internal sampling
// frequency fsKHz (8, 12 or 16) and the API sampling frequency of a channel,
// for the frame length given by psDec.nbSubfr.
func decoderSetFs(psDec *decoderState, fsKHz int, fsAPIHz int32) error {
	var err error

	// New (sub)frame length.
	psDec.subfrLength = subFrameLengthMs * fsKHz
	frameLength := psDec.nbSubfr * psDec.subfrLength

	// Initialize the resampler when switching internal or external sampling
	// frequency.
	if psDec.fsKHz != fsKHz || psDec.fsAPIHz != fsAPIHz {
		// Initialize the resampler for dec_API.c preparing resampling from
		// fsKHz to fsAPIHz.
		err = resamplerInit(&psDec.resamplerState, int32(fsKHz*1000), fsAPIHz)

		psDec.fsAPIHz = fsAPIHz
	}

	if psDec.fsKHz != fsKHz || frameLength != psDec.frameLength {
		if fsKHz == 8 {
			if psDec.nbSubfr == maxNbSubfr {
				psDec.pitchContourICDF = pitchContourNBICDF[:]
			} else {
				psDec.pitchContourICDF = pitchContour10MsNBICDF[:]
			}
		} else {
			if psDec.nbSubfr == maxNbSubfr {
				psDec.pitchContourICDF = pitchContourICDF[:]
			} else {
				psDec.pitchContourICDF = pitchContour10MsICDF[:]
			}
		}
		if psDec.fsKHz != fsKHz {
			psDec.ltpMemLength = ltpMemLengthMs * fsKHz
			if fsKHz == 8 || fsKHz == 12 {
				psDec.lpcOrder = minLPCOrder
				psDec.psNLSFCB = &nlsfCBNBMB
			} else {
				psDec.lpcOrder = maxLPCOrder
				psDec.psNLSFCB = &nlsfCBWB
			}
			switch fsKHz {
			case 16:
				psDec.pitchLagLowBitsICDF = uniform8ICDF[:]
			case 12:
				psDec.pitchLagLowBitsICDF = uniform6ICDF[:]
			case 8:
				psDec.pitchLagLowBitsICDF = uniform4ICDF[:]
			}
			psDec.firstFrameAfterReset = 1
			psDec.lagPrev = 100
			psDec.lastGainIndex = 10
			psDec.prevSignalType = typeNoVoiceActivity
			psDec.outBuf = [len(psDec.outBuf)]int16{}
			psDec.sLPCQ14Buf = [maxLPCOrder]int32{}
		}

		psDec.fsKHz = fsKHz
		psDec.frameLength = frameLength
	}
	return err
}
