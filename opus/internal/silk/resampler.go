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
// Ported from libopus 1.5.2 silk/resampler.c, silk/resampler_structs.h and silk/resampler_private.h (the decoder's paths).

package silk

import "errors"

// Matrix of resampling methods used (libopus resampler.c):
//
//	                                Fs_out (kHz)
//	                       8      12     16     24     48
//
//	              8        C      UF     U      UF     UF
//	             12        AF     C      UF     U      UF
//	Fs_in (kHz)  16        D      AF     C      UF     UF
//	             24        AF     D      AF     C      U
//	             48        AF     AF     AF     D      C
//
//	C   -> Copy (no resampling)
//	D   -> Allpass-based 2x downsampling
//	U   -> Allpass-based 2x upsampling
//	UF  -> Allpass-based 2x upsampling followed by FIR interpolation
//	AF  -> AR2 filter followed by FIR interpolation
//
// The decoder resamples from 8, 12 or 16 kHz; this port has its upsampling
// (U, UF) and copy (C) paths, which cover every output rate from the
// internal rate up to 48 kHz. The downsampling paths (D, AF:
// resampler_private_down_FIR.c, resampler_private_AR2.c) are not ported.

const (
	// resamplerMaxBatchSizeMs is RESAMPLER_MAX_BATCH_SIZE_MS: the number of
	// milliseconds of input processed in the inner loop.
	resamplerMaxBatchSizeMs = 10
	// resamplerMaxIIROrder is SILK_RESAMPLER_MAX_IIR_ORDER.
	resamplerMaxIIROrder = 6

	useResamplerCopy                = 0 // USE_silk_resampler_copy
	useResamplerPrivateUp2HQWrapper = 1 // USE_silk_resampler_private_up2_HQ_wrapper
	useResamplerPrivateIIRFIR       = 2 // USE_silk_resampler_private_IIR_FIR
)

// errResamplerRates reports a rate pair the decoder's resampler does not
// support.
var errResamplerRates = errors.New("silk: unsupported resampler rates")

// delayMatrixDec holds the delay compensation values of the decoder that
// equalize the total delay of the different modes (delay_matrix_dec).
var delayMatrixDec = [3][5]int8{
	// in \ out 8  12  16  24  48
	/*  8 */ {4, 0, 2, 0, 0},
	/* 12 */ {0, 9, 4, 7, 4},
	/* 16 */ {0, 3, 12, 7, 7},
}

// rateID is the rateID macro: it maps 8000, 12000, 16000, 24000 and 48000 to
// 0..4.
func rateID(r int32) int {
	var gt16k, gt24k int32
	if r > 16000 {
		gt16k = 1
	}
	if r > 24000 {
		gt24k = 1
	}
	return int((((r >> 12) - gt16k) >> gt24k) - 1)
}

// resamplerState is silk_resampler_state_struct for the decoder's paths.
// libopus's sFIR is a union of opus_int32[36] and opus_int16[36]; the
// upsampling paths only use its first RESAMPLER_ORDER_FIR_12 int16 values.
type resamplerState struct {
	sIIR              [resamplerMaxIIROrder]int32
	sFIR              [resamplerOrderFIR12]int16
	delayBuf          [48]int16
	resamplerFunction int
	batchSize         int
	invRatioQ16       int32
	fsInKHz           int
	fsOutKHz          int
	inputDelay        int
}

// resamplerInit is silk_resampler_init with forEnc = 0: it initializes or
// resets the resampler state for the given pair of input and output sampling
// rates.
func resamplerInit(s *resamplerState, fsHzIn, fsHzOut int32) error {
	// Clear state.
	*s = resamplerState{}

	// Input checking.
	if (fsHzIn != 8000 && fsHzIn != 12000 && fsHzIn != 16000) ||
		(fsHzOut != 8000 && fsHzOut != 12000 && fsHzOut != 16000 && fsHzOut != 24000 && fsHzOut != 48000) {
		return errResamplerRates
	}
	s.inputDelay = int(delayMatrixDec[rateID(fsHzIn)][rateID(fsHzOut)])

	s.fsInKHz = int(fsHzIn / 1000)
	s.fsOutKHz = int(fsHzOut / 1000)

	// Number of samples processed per batch.
	s.batchSize = s.fsInKHz * resamplerMaxBatchSizeMs

	// Find resampler with the right sampling ratio.
	up2x := 0
	switch {
	case fsHzOut > fsHzIn:
		// Upsample.
		if fsHzOut == fsHzIn*2 { // Fs_out : Fs_in = 2 : 1
			// Special case: directly use 2x upsampler.
			s.resamplerFunction = useResamplerPrivateUp2HQWrapper
		} else {
			// Default resampler.
			s.resamplerFunction = useResamplerPrivateIIRFIR
			up2x = 1
		}
	case fsHzOut < fsHzIn:
		// Downsample: silk_resampler_private_down_FIR is not ported.
		*s = resamplerState{}
		return errResamplerRates
	default:
		// Input and output sampling rates are equal: copy.
		s.resamplerFunction = useResamplerCopy
	}

	// Ratio of input/output samples.
	s.invRatioQ16 = ((fsHzIn << (14 + up2x)) / fsHzOut) << 2
	// Make sure the ratio is rounded up.
	for smulww(s.invRatioQ16, fsHzOut) < fsHzIn<<up2x {
		s.invRatioQ16++
	}
	return nil
}

// resampler is silk_resampler: it converts inLen samples of in, at least
// 1 ms, from the input to the output sampling rate into out.
func resampler(s *resamplerState, out []int16, in []int16, inLen int) {
	nSamples := s.fsInKHz - s.inputDelay

	// Copy to delay buffer.
	copy(s.delayBuf[s.inputDelay:s.inputDelay+nSamples], in[:nSamples])

	switch s.resamplerFunction {
	case useResamplerPrivateUp2HQWrapper:
		resamplerPrivateUp2HQWrapper(s, out, s.delayBuf[:], s.fsInKHz)
		resamplerPrivateUp2HQWrapper(s, out[s.fsOutKHz:], in[nSamples:], inLen-s.fsInKHz)
	case useResamplerPrivateIIRFIR:
		resamplerPrivateIIRFIR(s, out, s.delayBuf[:], s.fsInKHz)
		resamplerPrivateIIRFIR(s, out[s.fsOutKHz:], in[nSamples:], inLen-s.fsInKHz)
	default:
		copy(out[:s.fsInKHz], s.delayBuf[:s.fsInKHz])
		copy(out[s.fsOutKHz:s.fsOutKHz+inLen-s.fsInKHz], in[nSamples:])
	}

	// Copy to delay buffer.
	copy(s.delayBuf[:s.inputDelay], in[inLen-s.inputDelay:inLen])
}
