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
// Ported from libopus 1.5.2 silk/define.h, silk/control.h, silk/PLC.h and silk/pitch_est_defines.h.

package silk

// Constants of define.h used by the decoder.
const (
	decoderNumChannels = 2 // DECODER_NUM_CHANNELS

	maxFramesPerPacket = 3 // MAX_FRAMES_PER_PACKET

	// Maximum sampling frequencies.
	maxFsKHz    = 16 // MAX_FS_KHZ
	maxAPIFsKHz = 48 // MAX_API_FS_KHZ

	// Signal types.
	typeNoVoiceActivity = 0 // TYPE_NO_VOICE_ACTIVITY
	typeUnvoiced        = 1 // TYPE_UNVOICED
	typeVoiced          = 2 // TYPE_VOICED

	// Conditional coding types.
	codeIndependently             = 0 // CODE_INDEPENDENTLY
	codeIndependentlyNoLTPScaling = 1 // CODE_INDEPENDENTLY_NO_LTP_SCALING
	codeConditionally             = 2 // CODE_CONDITIONALLY

	// Stereo processing.
	stereoQuantSubSteps = 5 // STEREO_QUANT_SUB_STEPS
	stereoInterpLenMs   = 8 // STEREO_INTERP_LEN_MS

	// Number of samples per frame.
	maxNbSubfr          = 4                              // MAX_NB_SUBFR
	ltpMemLengthMs      = 20                             // LTP_MEM_LENGTH_MS
	subFrameLengthMs    = 5                              // SUB_FRAME_LENGTH_MS
	maxSubFrameLength   = subFrameLengthMs * maxFsKHz    // MAX_SUB_FRAME_LENGTH
	maxFrameLengthMs    = subFrameLengthMs * maxNbSubfr  // MAX_FRAME_LENGTH_MS
	maxFrameLength      = maxFrameLengthMs * maxFsKHz    // MAX_FRAME_LENGTH
	maxLTPMemLength     = ltpMemLengthMs * maxFsKHz      // the largest ltp_mem_length
	maxAPIFrameLength   = maxFrameLengthMs * maxAPIFsKHz // one frame at the largest API rate
	nLevelsQGain        = 64                             // N_LEVELS_QGAIN
	maxDeltaGainQuant   = 36                             // MAX_DELTA_GAIN_QUANT
	minDeltaGainQuant   = -4                             // MIN_DELTA_GAIN_QUANT
	minQGainDB          = 2                              // MIN_QGAIN_DB
	maxQGainDB          = 88                             // MAX_QGAIN_DB
	offsetVLQ10         = 32                             // OFFSET_VL_Q10
	offsetVHQ10         = 100                            // OFFSET_VH_Q10
	offsetUVLQ10        = 100                            // OFFSET_UVL_Q10
	offsetUVHQ10        = 240                            // OFFSET_UVH_Q10
	quantLevelAdjustQ10 = 80                             // QUANT_LEVEL_ADJUST_Q10

	maxLPCStabilizeIterations = 16 // MAX_LPC_STABILIZE_ITERATIONS

	maxLPCOrder = 16 // MAX_LPC_ORDER
	minLPCOrder = 10 // MIN_LPC_ORDER

	ltpOrder = 5 // LTP_ORDER

	// Excitation entropy coding.
	shellCodecFrameLength     = 16                                     // SHELL_CODEC_FRAME_LENGTH
	log2ShellCodecFrameLength = 4                                      // LOG2_SHELL_CODEC_FRAME_LENGTH
	maxNbShellBlocks          = maxFrameLength / shellCodecFrameLength // MAX_NB_SHELL_BLOCKS
	nRateLevels               = 10                                     // N_RATE_LEVELS
	silkMaxPulses             = 16                                     // SILK_MAX_PULSES

	// NLSF quantizer.
	nlsfQuantMaxAmplitude = 4 // NLSF_QUANT_MAX_AMPLITUDE

	// BWE factor after packet loss.
	bweAfterLossQ16 = 63570 // BWE_AFTER_LOSS_Q16

	// Comfort noise generation.
	cngBufMaskMax           = 255   // CNG_BUF_MASK_MAX
	cngGainSmthQ16          = 4634  // CNG_GAIN_SMTH_Q16
	cngGainSmthThresholdQ16 = 46396 // CNG_GAIN_SMTH_THRESHOLD_Q16
	cngNLSFSmthQ16          = 16348 // CNG_NLSF_SMTH_Q16
)

// SILK_FIX_CONST values, computed as the C compiler does.
const (
	nlsfQuantLevelAdjQ10    = 102     // SILK_FIX_CONST( NLSF_QUANT_LEVEL_ADJ, 10 )
	stereoQuantStepQ16      = 6554    // SILK_FIX_CONST( 0.5 / STEREO_QUANT_SUB_STEPS, 16 )
	ltpTransitionGainQ14    = 4096    // SILK_FIX_CONST( 0.25, 14 ), the LTP tap after voiced PLC
	invMaxPredictionGainQ30 = 107374  // SILK_FIX_CONST( 1.0f / MAX_PREDICTION_POWER_GAIN, 30 )
	oneQ30                  = 1 << 30 // SILK_FIX_CONST( 1, 30 )
	oneQ16                  = 1 << 16 // SILK_FIX_CONST( 1, 16 )
	lpcFitChirpQ16          = 65470   // SILK_FIX_CONST( 0.999, 16 )
	plcBWECoefQ16           = 64881   // SILK_FIX_CONST( BWE_COEF, 16 )
)

// Constants of PLC.h.
const (
	vPitchGainStartMinQ14   = 11469 // V_PITCH_GAIN_START_MIN_Q14, 0.7 in Q14
	vPitchGainStartMaxQ14   = 15565 // V_PITCH_GAIN_START_MAX_Q14, 0.95 in Q14
	maxPitchLagMs           = 18    // MAX_PITCH_LAG_MS
	randBufSize             = 128   // RAND_BUF_SIZE
	randBufMask             = randBufSize - 1
	log2InvLPCGainHighThres = 3   // LOG2_INV_LPC_GAIN_HIGH_THRES
	log2InvLPCGainLowThres  = 8   // LOG2_INV_LPC_GAIN_LOW_THRES
	pitchDriftFacQ16        = 655 // PITCH_DRIFT_FAC_Q16
)

// Constants of pitch_est_defines.h used by silk_decode_pitch.
const (
	peMaxNbSubfr        = 4  // PE_MAX_NB_SUBFR
	peMaxLagMs          = 18 // PE_MAX_LAG_MS
	peMinLagMs          = 2  // PE_MIN_LAG_MS
	peNbCbksStage2Ext   = 11 // PE_NB_CBKS_STAGE2_EXT
	peNbCbksStage3Max   = 34 // PE_NB_CBKS_STAGE3_MAX
	peNbCbksStage3_10ms = 12 // PE_NB_CBKS_STAGE3_10MS
	peNbCbksStage2_10ms = 3  // PE_NB_CBKS_STAGE2_10MS
)

// Decoder flags of control.h: the lostFlag argument of Decoder.Decode.
const (
	// FlagDecodeNormal decodes a received frame (FLAG_DECODE_NORMAL).
	FlagDecodeNormal = 0
	// FlagPacketLost conceals a lost frame (FLAG_PACKET_LOST).
	FlagPacketLost = 1
	// FlagDecodeLBRR decodes the in-band FEC (LBRR) copy of a frame
	// (FLAG_DECODE_LBRR).
	FlagDecodeLBRR = 2
)
