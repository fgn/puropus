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
// Ported from libopus 1.5.2 silk/structs.h.

package silk

// nlsfCB is silk_NLSF_CB_struct: an NLSF codebook (the decoder's fields).
type nlsfCB struct {
	nVectors           int16
	order              int16
	quantStepSizeQ16   int16
	invQuantStepSizeQ6 int16
	cb1NLSFQ8          []uint8
	cb1WghtQ9          []int16
	cb1ICDF            []uint8
	predQ8             []uint8
	ecSel              []uint8
	ecICDF             []uint8
	deltaMinQ15        []int16
}

// stereoDecState is stereo_dec_state.
type stereoDecState struct {
	predPrevQ13 [2]int16
	sMid        [2]int16
	sSide       [2]int16
}

// sideInfoIndices is SideInfoIndices: the quantization indices of a frame.
type sideInfoIndices struct {
	gainsIndices     [maxNbSubfr]int8
	ltpIndex         [maxNbSubfr]int8
	nlsfIndices      [maxLPCOrder + 1]int8
	lagIndex         int16
	contourIndex     int8
	signalType       int8
	quantOffsetType  int8
	nlsfInterpCoefQ2 int8
	perIndex         int8
	ltpScaleIndex    int8
	seed             int8
}

// plcStruct is silk_PLC_struct: the packet loss concealment state.
type plcStruct struct {
	pitchLQ8        int32              // pitch lag to use for voiced concealment
	ltpCoefQ14      [ltpOrder]int16    // LTP coefficients to use for voiced concealment
	prevLPCQ12      [maxLPCOrder]int16 // LPC coefficients of the last decoded frame
	lastFrameLost   int                // was the previous frame lost
	randSeed        int32              // seed for unvoiced signal generation
	randScaleQ14    int16              // scaling of the unvoiced random signal
	concEnergy      int32
	concEnergyShift int
	prevLTPScaleQ14 int16
	prevGainQ16     [2]int32
	fsKHz           int
	nbSubfr         int
	subfrLength     int
}

// cngStruct is silk_CNG_struct: the comfort noise generation state.
type cngStruct struct {
	cngExcBufQ14   [maxFrameLength]int32
	cngSmthNLSFQ15 [maxLPCOrder]int16
	cngSynthState  [maxLPCOrder]int32
	cngSmthGainQ16 int32
	randSeed       int32
	fsKHz          int
}

// decoderState is silk_decoder_state: the state of one channel. libopus's
// silk_reset_decoder clears all of it (the part from prev_gain_Q16 on, which
// is everything without OSCE).
type decoderState struct {
	prevGainQ16          int32
	excQ14               [maxFrameLength]int32
	sLPCQ14Buf           [maxLPCOrder]int32
	outBuf               [maxFrameLength + 2*maxSubFrameLength]int16 // buffer for the output signal
	lagPrev              int                                         // previous lag
	lastGainIndex        int8                                        // previous gain index
	fsKHz                int                                         // sampling frequency in kHz
	fsAPIHz              int32                                       // API sample frequency (Hz)
	nbSubfr              int                                         // number of 5 ms subframes in a frame
	frameLength          int                                         // frame length (samples)
	subfrLength          int                                         // subframe length (samples)
	ltpMemLength         int                                         // length of the LTP memory
	lpcOrder             int                                         // LPC order
	prevNLSFQ15          [maxLPCOrder]int16                          // used to interpolate LSFs
	firstFrameAfterReset int                                         // flag for deactivating NLSF interpolation
	pitchLagLowBitsICDF  []uint8                                     // iCDF table for the low bits of the pitch lag index
	pitchContourICDF     []uint8                                     // iCDF table for the pitch contour index

	// For buffering payload in case of more frames per packet.
	nFramesDecoded   int
	nFramesPerPacket int

	// Specifically for entropy coding.
	ecPrevSignalType int
	ecPrevLagIndex   int16

	vadFlags  [maxFramesPerPacket]int
	lbrrFlag  int
	lbrrFlags [maxFramesPerPacket]int

	resamplerState resamplerState

	psNLSFCB *nlsfCB // NLSF codebook

	// Quantization indices.
	indices sideInfoIndices

	// CNG state.
	sCNG cngStruct

	// Stuff used for PLC.
	lossCnt        int
	prevSignalType int

	sPLC plcStruct
}

// decoderControl is silk_decoder_control: the parameters of one frame.
type decoderControl struct {
	// Prediction and coding parameters.
	pitchL   [maxNbSubfr]int
	gainsQ16 [maxNbSubfr]int32
	// Holds interpolated and final coefficients.
	predCoefQ12 [2][maxLPCOrder]int16
	ltpCoefQ14  [ltpOrder * maxNbSubfr]int16
	ltpScaleQ14 int32
}
