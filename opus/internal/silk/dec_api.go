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
// Ported from libopus 1.5.2 silk/dec_API.c, silk/API.h, silk/control.h and silk/errors.h.

// Package silk is the SILK decoder of Opus (RFC 6716 §4.2), a port of the
// decoder side of libopus 1.5.2's silk/ directory: silk_Decode and the
// functions it reaches, the fixed-point signal processing they use, and the
// SILK resampler for the decoder's output rates. libopus's SILK decoder is
// integer code in every build; this port keeps its opus_int16/opus_int32
// arithmetic, so its output is bit-exact.
//
// The API mirrors libopus's dec_API.c:
//
//	libopus                                    Go
//	silk_Get_Decoder_Size + memset             var d silk.Decoder (or new)
//	silk_InitDecoder(st)                       d.Init()
//	silk_ResetDecoder(st)                      d.Reset()
//	silk_Decode(st, &ctl, lostFlag,            n, err := d.Decode(&ctl, lostFlag,
//	    newPacketFlag, dec, pcm, &n)               newPacketFlag, dec, pcm)
//	silk_DecControlStruct                      DecControl
//	FLAG_DECODE_NORMAL, FLAG_PACKET_LOST,      FlagDecodeNormal, FlagPacketLost,
//	FLAG_DECODE_LBRR                           FlagDecodeLBRR
//	(not in libopus)                           d.SaveState(&s), d.RestoreState(&s)
//
// Decode writes one SILK frame (10 or 20 ms) of 16-bit PCM at
// DecControl.APISampleRate, interleaved when DecControl.NChannelsAPI is 2, and
// returns the number of samples per channel; a 40 or 60 ms packet takes two or
// three calls, the first with newPacketFlag set, as opus_decode_frame makes
// them. Concealment (FlagPacketLost) takes the duration to conceal in
// PayloadSizeMs (opus_decode_frame passes max(10, ms) of at most 20 ms).
// Package opus converts the PCM to float as libopus's float build does
// (x/32768). In-band FEC (LBRR) frames are
// parsed and skipped with FlagDecodeNormal; FlagDecodeLBRR decodes them, as
// opus_decode_frame does with decode_fec.
//
// Differences from libopus: the resampler has the upsampling paths and the
// copy, which cover every output rate the decoder uses with Opus (8, 12 and
// 16 kHz to 12, 16, 24 or 48 kHz); an output rate below the internal rate,
// which needs libopus's downsampling paths, fails with
// ErrInvalidSamplingFrequency where libopus would continue with a failed
// resampler. Invalid control values return an error, as silk_Decode returns
// an error code (libopus's celt_assert checks are not ported). The decoder's
// scratch memory is zero-initialized where libopus's stack arrays are not;
// valid streams never read it before writing.
//
// A Decoder holds all its state and scratch memory: Decode does not allocate.
//
// Several stages are restructured for speed without changing a result: they
// form the same truncated products and wrapping int32 sums as libopus in
// another order, or decode the same symbols with fewer branches, and
// kernels_test.go checks each against libopus's loop. With
// GOEXPERIMENT=simd on amd64 with AVX, the resampler and the stereo output
// run 128-bit vector kernels (simd_amd64.go; package opus has its own for
// the conversion to float), chosen at run time and checked against the
// scalar code (simd_amd64_test.go, also under each GODEBUG tier of the
// test:simd:noavx task).
package silk

import (
	"errors"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// Errors returned by Decode (errors.h).
var (
	// ErrInvalidSamplingFrequency is SILK_DEC_INVALID_SAMPLING_FREQUENCY:
	// an internal or API sample rate the decoder does not support.
	ErrInvalidSamplingFrequency = errors.New("silk: invalid sampling frequency")
	// ErrInvalidFrameSize is SILK_DEC_INVALID_FRAME_SIZE: a payload size
	// other than 0, 10, 20, 40 or 60 ms.
	ErrInvalidFrameSize = errors.New("silk: invalid frame size")
)

// DecControl is silk_DecControlStruct: the decoder's control input and status
// output. Package opus keeps one per decoder and updates it before each
// Decode, as opus_decode_frame does with OpusDecoder.DecControl.
type DecControl struct {
	// NChannelsAPI is the number of output channels, 1 or 2 (input).
	NChannelsAPI int
	// NChannelsInternal is the number of coded channels, 1 or 2 (input).
	NChannelsInternal int
	// APISampleRate is the output sample rate in Hz, 48000 for Opus
	// (input).
	APISampleRate int
	// InternalSampleRate is the coded sample rate in Hz: 8000, 12000 or
	// 16000 (input).
	InternalSampleRate int
	// PayloadSizeMs is the packet duration in ms: 10, 20, 40 or 60, or 0
	// for a lost packet (input).
	PayloadSizeMs int
	// PrevPitchLag is the pitch lag of the previous frame measured in
	// samples at 48 kHz, 0 if it was not voiced (output; OPUS_GET_PITCH).
	PrevPitchLag int
}

// state is the part of silk_decoder that one Decode call leaves for the
// next: the channel states, the stereo state and the channel counts of the
// last call.
type state struct {
	channelState         [decoderNumChannels]decoderState
	sStereo              stereoDecState
	nChannelsAPI         int
	nChannelsInternal    int
	prevDecodeOnlyMiddle int
}

// State is a saved copy of a Decoder's state (SaveState, RestoreState).
type State struct {
	s state
}

// Decoder is silk_decoder: the SILK decoder state of both channels. The zero
// value must be initialized with Init.
type Decoder struct {
	state

	// samplesOut1Tmp holds the decoded frame of each channel at the
	// internal rate, after two samples of stereo history
	// (samplesOut1_tmp in silk_Decode).
	samplesOut1Tmp [decoderNumChannels][maxFrameLength + 2]int16
	// samplesOut2Tmp holds the two resampled channels before interleaving
	// (samplesOut2_tmp in silk_Decode, which holds one channel at a time).
	samplesOut2Tmp [2][maxAPIFrameLength]int16
}

// Reset is silk_ResetDecoder: it resets the state of both channels and the
// stereo state, keeping the channel counts of the last call.
func (d *Decoder) Reset() {
	for n := range d.channelState {
		resetDecoder(&d.channelState[n])
	}
	d.sStereo = stereoDecState{}
	// Not strictly needed, but it's cleaner that way.
	d.prevDecodeOnlyMiddle = 0
}

// SaveState copies the decoder's state, everything a later Decode depends
// on, into s. It is not in libopus: package opus saves the state before a
// packet it may reject after SILK has decoded part of it.
func (d *Decoder) SaveState(s *State) {
	s.s = d.state
}

// RestoreState returns the decoder to the state SaveState saved in s.
func (d *Decoder) RestoreState(s *State) {
	d.state = s.s
}

// Init is silk_InitDecoder on cleared memory, as opus_decoder_init calls it:
// the decoder becomes the state of a new stream.
func (d *Decoder) Init() {
	*d = Decoder{}
	for n := range d.channelState {
		initDecoder(&d.channelState[n])
	}
}

// Decode is silk_Decode: it decodes one SILK frame from psRangeDec, or
// conceals it when lostFlag is FlagPacketLost, into samplesOut and returns the
// number of samples per channel written at decControl.APISampleRate.
// newPacketFlag is set for the first call of a packet. samplesOut must hold
// the frame for decControl.NChannelsAPI channels: up to 960 samples per
// channel at 48 kHz.
func (d *Decoder) Decode(decControl *DecControl, lostFlag int, newPacketFlag bool, psRangeDec *entcode.Decoder, samplesOut []int16) (nSamplesOut int, err error) {
	var (
		decodeOnlyMiddle int
		msPredQ13        [2]int32
		nSamplesOutDec   int
	)
	channelState := &d.channelState
	clearUpperState()

	// Test if first frame in payload.
	if newPacketFlag {
		for n := 0; n < decControl.NChannelsInternal; n++ {
			channelState[n].nFramesDecoded = 0 // Used to count frames in packet.
		}
	}

	// If Mono -> Stereo transition in bitstream: init state of second channel.
	if decControl.NChannelsInternal > d.nChannelsInternal {
		initDecoder(&channelState[1])
	}

	stereoToMono := decControl.NChannelsInternal == 1 && d.nChannelsInternal == 2 &&
		decControl.InternalSampleRate == 1000*channelState[0].fsKHz

	if channelState[0].nFramesDecoded == 0 {
		for n := 0; n < decControl.NChannelsInternal; n++ {
			switch decControl.PayloadSizeMs {
			case 0, 10:
				// A payload size of 0 means packet loss: use 10 ms.
				channelState[n].nFramesPerPacket = 1
				channelState[n].nbSubfr = 2
			case 20:
				channelState[n].nFramesPerPacket = 1
				channelState[n].nbSubfr = 4
			case 40:
				channelState[n].nFramesPerPacket = 2
				channelState[n].nbSubfr = 4
			case 60:
				channelState[n].nFramesPerPacket = 3
				channelState[n].nbSubfr = 4
			default:
				return 0, ErrInvalidFrameSize
			}
			fsKHzDec := (decControl.InternalSampleRate >> 10) + 1
			if fsKHzDec != 8 && fsKHzDec != 12 && fsKHzDec != 16 {
				return 0, ErrInvalidSamplingFrequency
			}
			if err := decoderSetFs(&channelState[n], fsKHzDec, int32(decControl.APISampleRate)); err != nil {
				return 0, err
			}
		}
	}

	if decControl.NChannelsAPI == 2 && decControl.NChannelsInternal == 2 && (d.nChannelsAPI == 1 || d.nChannelsInternal == 1) {
		d.sStereo.predPrevQ13 = [2]int16{}
		d.sStereo.sSide = [2]int16{}
		channelState[1].resamplerState = channelState[0].resamplerState
	}
	d.nChannelsAPI = decControl.NChannelsAPI
	d.nChannelsInternal = decControl.NChannelsInternal

	if decControl.APISampleRate > maxAPIFsKHz*1000 || decControl.APISampleRate < 8000 {
		return 0, ErrInvalidSamplingFrequency
	}

	if lostFlag != FlagPacketLost && channelState[0].nFramesDecoded == 0 {
		// First decoder call for this payload: decode the VAD flags and the
		// LBRR flag.
		for n := 0; n < decControl.NChannelsInternal; n++ {
			for i := 0; i < channelState[n].nFramesPerPacket; i++ {
				channelState[n].vadFlags[i] = psRangeDec.DecBitLogp(1)
			}
			channelState[n].lbrrFlag = psRangeDec.DecBitLogp(1)
		}
		// Decode the LBRR flags.
		for n := 0; n < decControl.NChannelsInternal; n++ {
			channelState[n].lbrrFlags = [maxFramesPerPacket]int{}
			if channelState[n].lbrrFlag != 0 {
				if channelState[n].nFramesPerPacket == 1 {
					channelState[n].lbrrFlags[0] = 1
				} else {
					lbrrSymbol := psRangeDec.DecICDF(lbrrFlagsICDFPtr[channelState[n].nFramesPerPacket-2], 8) + 1
					for i := 0; i < channelState[n].nFramesPerPacket; i++ {
						channelState[n].lbrrFlags[i] = (lbrrSymbol >> i) & 1
					}
				}
			}
		}

		if lostFlag == FlagDecodeNormal {
			// Regular decoding: skip all LBRR data.
			for i := 0; i < channelState[0].nFramesPerPacket; i++ {
				for n := 0; n < decControl.NChannelsInternal; n++ {
					if channelState[n].lbrrFlags[i] != 0 {
						var pulses [maxFrameLength]int16
						if decControl.NChannelsInternal == 2 && n == 0 {
							stereoDecodePred(psRangeDec, &msPredQ13)
							if channelState[1].lbrrFlags[i] == 0 {
								decodeOnlyMiddle = stereoDecodeMidOnly(psRangeDec)
							}
						}
						// Use conditional coding if the previous frame is
						// available.
						condCoding := codeIndependently
						if i > 0 && channelState[n].lbrrFlags[i-1] != 0 {
							condCoding = codeConditionally
						}
						decodeIndices(&channelState[n], psRangeDec, i, 1, condCoding)
						decodePulses(psRangeDec, pulses[:], int(channelState[n].indices.signalType),
							int(channelState[n].indices.quantOffsetType), channelState[n].frameLength)
					}
				}
			}
		}
	}

	// Get the MS predictor index.
	if decControl.NChannelsInternal == 2 {
		frame := channelState[0].nFramesDecoded
		if lostFlag == FlagDecodeNormal ||
			(lostFlag == FlagDecodeLBRR && channelState[0].lbrrFlags[frame] == 1) {
			stereoDecodePred(psRangeDec, &msPredQ13)
			// For LBRR data, decode the mid-only flag only if the side
			// channel's LBRR flag is false.
			if (lostFlag == FlagDecodeNormal && channelState[1].vadFlags[frame] == 0) ||
				(lostFlag == FlagDecodeLBRR && channelState[1].lbrrFlags[frame] == 0) {
				decodeOnlyMiddle = stereoDecodeMidOnly(psRangeDec)
			} else {
				decodeOnlyMiddle = 0
			}
		} else {
			for n := range 2 {
				msPredQ13[n] = int32(d.sStereo.predPrevQ13[n])
			}
		}
	}

	// Reset the side channel decoder prediction memory for the first frame
	// with side coding.
	if decControl.NChannelsInternal == 2 && decodeOnlyMiddle == 0 && d.prevDecodeOnlyMiddle == 1 {
		side := &channelState[1]
		side.outBuf = [len(side.outBuf)]int16{}
		side.sLPCQ14Buf = [maxLPCOrder]int32{}
		side.lagPrev = 100
		side.lastGainIndex = 10
		side.prevSignalType = typeNoVoiceActivity
		side.firstFrameAfterReset = 1
	}

	// libopus decodes into the output buffer when it is large enough
	// (delay_stack_alloc); the decoder's own buffers give the same result.
	samplesOut1Tmp := &d.samplesOut1Tmp

	var hasSide bool
	if lostFlag == FlagDecodeNormal {
		hasSide = decodeOnlyMiddle == 0
	} else {
		hasSide = d.prevDecodeOnlyMiddle == 0 ||
			(decControl.NChannelsInternal == 2 && lostFlag == FlagDecodeLBRR &&
				channelState[1].lbrrFlags[channelState[1].nFramesDecoded] == 1)
	}
	// Call the decoder for one frame.
	for n := 0; n < decControl.NChannelsInternal; n++ {
		if n == 0 || hasSide {
			frameIndex := channelState[0].nFramesDecoded - n
			var condCoding int
			switch {
			case frameIndex <= 0:
				// Use independent coding if no previous frame is available.
				condCoding = codeIndependently
			case lostFlag == FlagDecodeLBRR:
				condCoding = codeIndependently
				if channelState[n].lbrrFlags[frameIndex-1] != 0 {
					condCoding = codeConditionally
				}
			case n > 0 && d.prevDecodeOnlyMiddle != 0:
				// If we skipped a side frame in this packet, we don't need
				// LTP scaling; the LTP state is well-defined.
				condCoding = codeIndependentlyNoLTPScaling
			default:
				condCoding = codeConditionally
			}
			nSamplesOutDec = decodeFrame(&channelState[n], psRangeDec, samplesOut1Tmp[n][2:], lostFlag, condCoding)
		} else {
			clear(samplesOut1Tmp[n][2 : 2+nSamplesOutDec])
		}
		channelState[n].nFramesDecoded++
	}

	if decControl.NChannelsAPI == 2 && decControl.NChannelsInternal == 2 {
		// Convert Mid/Side to Left/Right.
		stereoMSToLR(&d.sStereo, samplesOut1Tmp[0][:], samplesOut1Tmp[1][:], &msPredQ13, channelState[0].fsKHz, nSamplesOutDec)
	} else {
		// Buffering.
		copy(samplesOut1Tmp[0][:2], d.sStereo.sMid[:])
		copy(d.sStereo.sMid[:], samplesOut1Tmp[0][nSamplesOutDec:nSamplesOutDec+2])
	}

	// Number of output samples.
	nSamplesOut = nSamplesOutDec * decControl.APISampleRate / (channelState[0].fsKHz * 1000)

	if decControl.NChannelsAPI == 2 {
		// Resample the decoded signal to the API sample rate and
		// interleave. libopus interleaves each channel as it resamples it;
		// the two are independent, so both channels are resampled first and
		// interleaved in one pass.
		left := d.samplesOut2Tmp[0][:nSamplesOut]
		right := d.samplesOut2Tmp[1][:nSamplesOut]
		resampler(&channelState[0].resamplerState, left, samplesOut1Tmp[0][1:], nSamplesOutDec)
		switch {
		case decControl.NChannelsInternal == 2:
			resampler(&channelState[1].resamplerState, right, samplesOut1Tmp[1][1:], nSamplesOutDec)
		case stereoToMono:
			// Create two channel output from mono stream: resample the
			// right channel for newly collapsed stereo just in case we
			// weren't doing collapsing when switching to mono.
			resampler(&channelState[1].resamplerState, right, samplesOut1Tmp[0][1:], nSamplesOutDec)
		default:
			// Create two channel output from mono stream.
			right = left
		}
		interleave(samplesOut[:2*nSamplesOut], left, right)
	} else {
		// Resample the decoded signal to the API sample rate.
		resampler(&channelState[0].resamplerState, samplesOut, samplesOut1Tmp[0][1:], nSamplesOutDec)
	}

	// Export the pitch lag, measured at 48 kHz sampling rate.
	if channelState[0].prevSignalType == typeVoiced {
		multTab := [3]int{6, 4, 3}
		decControl.PrevPitchLag = channelState[0].lagPrev * multTab[(channelState[0].fsKHz-8)>>2]
	} else {
		decControl.PrevPitchLag = 0
	}

	if lostFlag == FlagPacketLost {
		// On packet loss, remove the gain clamping to prevent having the
		// energy "bounce back" if we lose packets when the energy is going
		// down.
		for i := 0; i < d.nChannelsInternal; i++ {
			channelState[i].lastGainIndex = 10
		}
	} else {
		d.prevDecodeOnlyMiddle = decodeOnlyMiddle
	}
	return nSamplesOut, nil
}

// interleaveScalar sets dst to the samples of left and right alternately,
// as silk_Decode interleaves the resampled channels; dst holds 2*len(left)
// samples and right as many as left. interleave (simd_*.go) runs a vector
// kernel where the CPU has one, otherwise this.
func interleaveScalar(dst, left, right []int16) {
	right = right[:len(left)]
	dst = dst[:2*len(left)]
	for i, l := range left {
		dst[2*i] = l
		dst[2*i+1] = right[i]
	}
}
