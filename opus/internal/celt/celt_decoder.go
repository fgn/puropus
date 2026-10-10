// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2010 Xiph.Org Foundation
// Copyright (c) 2008 Gregory Maxwell
// Written by Jean-Marc Valin and Gregory Maxwell
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
// Ported from libopus 1.5.2 celt/celt_decoder.c.

// Package celt is the CELT decoder of Opus (RFC 6716 §4.3), a port of
// libopus 1.5.2's float build: celt/celt_decoder.c and the files it reaches,
// for the static 48 kHz mode with 2.5, 5, 10 and 20 ms frames.
//
// Decoder is libopus's CELTDecoder. Package opus drives it as
// src/opus_decoder.c does:
//
//	celt_decoder_init(st, Fs, channels)  Init (or NewDecoder)
//	celt_decode_with_ec(st, data, len, pcm, frame_size, dec, 0)
//	                                     DecodeWithEC(data[:len], pcm, frameSize, dec)
//	celt_decode_with_ec(st, NULL, ...)   DecodeWithEC(nil, pcm, frameSize, nil): loss concealment
//	CELT_SET_START_BAND, CELT_SET_END_BAND  SetStartBand, SetEndBand
//	CELT_SET_CHANNELS                    SetStreamChannels
//	OPUS_SET_PHASE_INVERSION_DISABLED    SetPhaseInversionDisabled
//	OPUS_RESET_STATE                     Reset
//	OPUS_GET_FINAL_RANGE                 FinalRange
//	CELT_GET_MODE                        Mode (Mode.Window for smooth_fade)
//	OPUS_GET_PITCH                       Pitch
//	CELT_GET_AND_CLEAR_ERROR             GetAndClearError
//	(not in libopus)                     SaveState, RestoreState
//
// CELT_SET_SIGNALLING does nothing without custom modes and is left out. The
// float build never accumulates into the output (opus_decode_frame passes
// celt_accum = 0, and deemphasis asserts it), so DecodeWithEC has no accum
// argument: in hybrid mode the caller adds the SILK output to the CELT output
// itself.
//
// A Decoder holds all its state and scratch memory; decoding allocates
// nothing.
package celt

import (
	"errors"

	"github.com/fgn/puropus/opus/internal/entcode"
)

const (
	// plcPitchLagMax is the maximum pitch lag of the pitch-based PLC, a
	// pitch of 66.67 Hz (PLC_PITCH_LAG_MAX). It is possible to save CPU time
	// in the PLC pitch search by making this smaller than MAX_PERIOD.
	plcPitchLagMax = 720
	// plcPitchLagMin is the minimum pitch lag of the pitch-based PLC, a
	// pitch of 480 Hz (PLC_PITCH_LAG_MIN).
	plcPitchLagMin = 100
	// decodeBufferSize is the history kept per channel (DECODE_BUFFER_SIZE).
	decodeBufferSize = 2048
)

// Sizes of the static mode, for the decoder's fixed arrays.
const (
	maxChannels  = 2
	maxBands     = 21  // nbEBands
	maxOverlap   = 120 // overlap
	maxFrameSize = 960 // shortMdctSize << maxLM
	// maxNormSize is the size of quant_all_bands's norm buffer for two
	// channels: 2*M*eBands[nbEBands-1].
	maxNormSize = 2 * 8 * 78
)

// Errors of DecodeWithEC and the CTLs.
var (
	// ErrBadArg is OPUS_BAD_ARG: an unsupported frame size, sample rate,
	// channel count or band, a start band not below the end band, or a
	// frame longer than 1275 bytes.
	ErrBadArg = errors.New("celt: invalid argument")
	// ErrBufferTooSmall is returned when pcm cannot hold the decoded frame.
	ErrBufferTooSmall = errors.New("celt: buffer too small")
	// ErrInternal is OPUS_INTERNAL_ERROR: the frame used more bits than it
	// has. The decoder state and pcm are updated as libopus updates them.
	ErrInternal = errors.New("celt: internal error")
)

// decoderState is the part of the decoder that Reset clears, everything
// from DECODER_RESET_START (rng) on in libopus's CELTDecoder.
type decoderState struct {
	rng                 uint32
	errorFlag           int
	lastPitchIndex      int
	lossDuration        int
	skipPLC             bool
	postfilterPeriod    int
	postfilterPeriodOld int
	postfilterGain      float32
	postfilterGainOld   float32
	postfilterTapset    int
	postfilterTapsetOld int
	prefilterAndFold    bool

	preemphMemD [2]float32

	// decodeMem holds DECODE_BUFFER_SIZE+overlap samples of history per
	// channel (_decode_mem).
	decodeMem      [maxChannels][decodeBufferSize + maxOverlap]float32
	lpc            [maxChannels][lpcOrder]float32
	oldBandE       [2 * maxBands]float32
	oldLogE        [2 * maxBands]float32
	oldLogE2       [2 * maxBands]float32
	backgroundLogE [2 * maxBands]float32
}

// Decoder is a CELT decoder (CELTDecoder, OpusCustomDecoder) for the static
// 48 kHz mode. The zero value is not usable; call Init or NewDecoder.
type Decoder struct {
	mode           *Mode
	overlap        int
	channels       int
	streamChannels int
	downsample     int
	start, end     int
	disableInv     bool

	decoderState

	// Scratch memory for one frame: libopus's stack arrays.
	ec            entcode.Decoder
	x             [maxChannels * maxFrameSize]float32
	freq          [maxFrameSize]float32
	tfRes         [maxBands]int
	caps          [maxBands]int
	offsets       [maxBands]int
	fineQuant     [maxBands]int
	pulses        [maxBands]int
	finePriority  [maxBands]int
	collapseMasks [maxChannels * maxBands]uint8
	norm          [maxNormSize]float32
	bands         bandCtx
	etmp          [maxOverlap]float32
	lpPitchBuf    [decodeBufferSize >> 1]float32
	exc           [maxPeriod + lpcOrder]float32
	firTmp        [maxPeriod]float32
}

// NewDecoder returns a decoder for the given output sample rate (48000,
// 24000, 16000, 12000 or 8000) and channels (1 or 2), as celt_decoder_init
// sets it up.
func NewDecoder(sampleRate, channels int) (*Decoder, error) {
	st := new(Decoder)
	if err := st.Init(sampleRate, channels); err != nil {
		return nil, err
	}
	return st, nil
}

// Init sets st up for the given output sample rate (48000, 24000, 16000,
// 12000 or 8000) and channels (1 or 2) and resets it (celt_decoder_init with
// opus_custom_decoder_init for the static mode). Phase inversion is
// disabled for mono, as libopus does.
func (st *Decoder) Init(sampleRate, channels int) error {
	if channels < 1 || channels > 2 {
		return ErrBadArg
	}
	downsample := resamplingFactor(sampleRate)
	if downsample == 0 {
		return ErrBadArg
	}
	initTables()
	*st = Decoder{}
	st.mode = StaticMode()
	st.overlap = st.mode.overlap
	st.channels = channels
	st.streamChannels = channels
	st.downsample = downsample
	st.start = 0
	st.end = st.mode.effEBands
	st.disableInv = channels == 1
	st.Reset()
	return nil
}

// deemphasisStereoSimple is the special case for stereo with no
// downsampling and no accumulation. This is quite common and we can make it
// faster by processing both channels in the same loop, reducing overhead
// due to the dependency loop in the IIR filter (deemphasis_stereo_simple).
func deemphasisStereoSimple(in [2][]float32, pcm []float32, N int, coef0 float32, mem []float32) {
	x0 := in[0][:N]
	x1 := in[1][:N]
	pcm = pcm[:2*N]
	m0 := mem[0]
	m1 := mem[1]
	for j := range x0 {
		// Add VERY_SMALL to x[] first to reduce dependency chain.
		tmp0 := x0[j] + verySmall + m0
		tmp1 := x1[j] + verySmall + m1
		m0 = float32(coef0 * tmp0)
		m1 = float32(coef0 * tmp1)
		pcm[2*j] = tmp0 * (1 / celtSigScale)
		pcm[2*j+1] = tmp1 * (1 / celtSigScale)
	}
	mem[0] = m0
	mem[1] = m1
}

// deemphasis applies the de-emphasis filter to the C channels of in and
// writes N/downsample interleaved samples to pcm, scaled to [-1, 1]
// (deemphasis, float build, without custom modes; the float build never
// accumulates). scratch holds N values.
func deemphasis(in [2][]float32, pcm []float32, N, C, downsample int, coef []float32, mem []float32, scratch []float32) {
	// Short version for common case.
	if downsample == 1 && C == 2 {
		deemphasisStereoSimple(in, pcm, N, coef[0], mem)
		return
	}
	applyDownsampling := false
	coef0 := coef[0]
	Nd := N / downsample
	for c := 0; c < C; c++ {
		m := mem[c]
		x := in[c][:N]
		y := pcm[c:]
		if downsample > 1 {
			// Shortcut for the standard (non-custom modes) case.
			for j := range x {
				tmp := x[j] + verySmall + m
				m = float32(coef0 * tmp)
				scratch[j] = tmp
			}
			applyDownsampling = true
		} else {
			// Shortcut for the standard (non-custom modes) case.
			for j := range x {
				tmp := x[j] + verySmall + m
				m = float32(coef0 * tmp)
				y[j*C] = tmp * (1 / celtSigScale)
			}
		}
		mem[c] = m

		if applyDownsampling {
			// Perform down-sampling.
			for j := 0; j < Nd; j++ {
				y[j*C] = scratch[j*downsample] * (1 / celtSigScale)
			}
		}
	}
}

// combSpan is a run of samples [start, end) of a frame and what the pitch
// post-filter does to them: comb_filter's cross-fade between two filters,
// its constant filter with period T1, or nothing.
type combSpan struct {
	kind                         int
	start, end                   int
	T0, T1                       int
	g00, g01, g02, g10, g11, g12 float32
}

const (
	combKeep = iota
	combFade
	combConst
)

// combSpans appends to spans the runs of combFilter(y, x, xi, T0, T1, n,
// g0, g1, tapset0, tapset1, window, overlap) for the n samples from start,
// as combFilter decides them.
func combSpans(spans []combSpan, start, T0, T1, n int, g0, g1 float32, tapset0, tapset1, overlap int) []combSpan {
	if g0 == 0 && g1 == 0 {
		return append(spans, combSpan{kind: combKeep, start: start, end: start + n})
	}
	sp := combSpan{
		T0:  max(T0, combFilterMinPeriod),
		T1:  max(T1, combFilterMinPeriod),
		g00: g0 * combFilterGains[tapset0][0],
		g01: g0 * combFilterGains[tapset0][1],
		g02: g0 * combFilterGains[tapset0][2],
		g10: g1 * combFilterGains[tapset1][0],
		g11: g1 * combFilterGains[tapset1][1],
		g12: g1 * combFilterGains[tapset1][2],
	}
	// If the filter didn't change, we don't need the overlap.
	if g0 == g1 && sp.T0 == sp.T1 && tapset0 == tapset1 {
		overlap = 0
	}
	if overlap > 0 {
		sp.kind, sp.start, sp.end = combFade, start, start+overlap
		spans = append(spans, sp)
	}
	sp.kind, sp.start, sp.end = combConst, start+overlap, start+n
	if g1 == 0 {
		sp.kind = combKeep
	}
	return append(spans, sp)
}

// postfilterDeemphasis applies the post-filter spans in place to the
// channel history x, whose frame starts at xi, and the de-emphasis to the
// result, writing the frame to pcm (comb_filter and deemphasis without
// downsampling, mono). It returns the de-emphasis memory. The comb
// filter's arithmetic runs in the shadow of the de-emphasis recurrence;
// each sample's values are those of the separate passes, as every comb
// tap reads samples at least combFilterMinPeriod-2 back, already filtered.
func postfilterDeemphasis(x []float32, xi int, spans []combSpan, window, pcm []float32, coef, m float32) float32 {
	for k := range spans {
		m = postfilterSpan(x, xi, &spans[k], 0, window, pcm, coef, m)
	}
	return m
}

// postfilterSpan is postfilterDeemphasis for the samples of span sp from
// its sample from on.
func postfilterSpan(x []float32, xi int, sp *combSpan, from int, window, pcm []float32, coef, m float32) float32 {
	y := x[xi+sp.start : xi+sp.end]
	out := pcm[sp.start:sp.end]
	out = out[:len(y)]
	switch sp.kind {
	case combKeep:
		for j := from; j < len(y); j++ {
			tmp := y[j] + verySmall + m
			m = float32(coef * tmp)
			out[j] = tmp * (1 / celtSigScale)
		}
	case combConst:
		g10, g11, g12 := sp.g10, sp.g11, sp.g12
		b := xi + sp.start - sp.T1
		for j := from; j < len(y); j++ {
			h := x[b+j-2 : b+j+3 : b+j+3]
			v := y[j] + float32(g10*h[2]) + float32(g11*(h[3]+h[1])) + float32(g12*(h[4]+h[0]))
			y[j] = v
			tmp := v + verySmall + m
			m = float32(coef * tmp)
			out[j] = tmp * (1 / celtSigScale)
		}
	case combFade:
		a := xi + sp.start - sp.T0
		b := xi + sp.start - sp.T1
		w := window[:len(y)]
		for j := from; j < len(y); j++ {
			f := float32(w[j] * w[j])
			g := x[a+j-2 : a+j+3 : a+j+3]
			h := x[b+j-2 : b+j+3 : b+j+3]
			v := y[j] +
				float32(((q15One-f)*sp.g00)*g[2]) +
				float32(((q15One-f)*sp.g01)*(g[3]+g[1])) +
				float32(((q15One-f)*sp.g02)*(g[4]+g[0])) +
				float32((f*sp.g10)*h[2]) +
				float32((f*sp.g11)*(h[3]+h[1])) +
				float32((f*sp.g12)*(h[4]+h[0]))
			y[j] = v
			tmp := v + verySmall + m
			m = float32(coef * tmp)
			out[j] = tmp * (1 / celtSigScale)
		}
	}
	return m
}

// postfilterDeemphasisStereo is postfilterDeemphasis for two channels,
// with deemphasis_stereo_simple's interleaving of the two recurrences.
func postfilterDeemphasisStereo(x0, x1 []float32, xi int, spans []combSpan, window, pcm []float32, coef float32,
	mem []float32,
) {
	if useAVX2 {
		postfilterDeemphasisStereoAVX2(x0, x1, xi, spans, window, pcm, coef, mem)
		return
	}
	for k := range spans {
		postfilterSpanStereo(x0, x1, xi, &spans[k], 0, window, pcm, coef, mem)
	}
}

// postfilterSpanStereo is postfilterDeemphasisStereo for the samples of
// span sp from its sample from on.
func postfilterSpanStereo(x0, x1 []float32, xi int, sp *combSpan, from int, window, pcm []float32, coef float32,
	mem []float32,
) {
	m0, m1 := mem[0], mem[1]
	y0 := x0[xi+sp.start : xi+sp.end]
	y1 := x1[xi+sp.start : xi+sp.end]
	y1 = y1[:len(y0)]
	out := pcm[2*sp.start : 2*sp.end]
	out = out[:2*len(y0)]
	switch sp.kind {
	case combKeep:
		for j := from; j < len(y0); j++ {
			// Add VERY_SMALL to x[] first to reduce dependency chain.
			tmp0 := y0[j] + verySmall + m0
			tmp1 := y1[j] + verySmall + m1
			m0 = float32(coef * tmp0)
			m1 = float32(coef * tmp1)
			out[2*j] = tmp0 * (1 / celtSigScale)
			out[2*j+1] = tmp1 * (1 / celtSigScale)
		}
	case combConst:
		g10, g11, g12 := sp.g10, sp.g11, sp.g12
		b := xi + sp.start - sp.T1
		for j := from; j < len(y0); j++ {
			h := x0[b+j-2 : b+j+3 : b+j+3]
			v0 := y0[j] + float32(g10*h[2]) + float32(g11*(h[3]+h[1])) + float32(g12*(h[4]+h[0]))
			h = x1[b+j-2 : b+j+3 : b+j+3]
			v1 := y1[j] + float32(g10*h[2]) + float32(g11*(h[3]+h[1])) + float32(g12*(h[4]+h[0]))
			y0[j] = v0
			y1[j] = v1
			tmp0 := v0 + verySmall + m0
			tmp1 := v1 + verySmall + m1
			m0 = float32(coef * tmp0)
			m1 = float32(coef * tmp1)
			out[2*j] = tmp0 * (1 / celtSigScale)
			out[2*j+1] = tmp1 * (1 / celtSigScale)
		}
	case combFade:
		a := xi + sp.start - sp.T0
		b := xi + sp.start - sp.T1
		w := window[:len(y0)]
		for j := from; j < len(y0); j++ {
			f := float32(w[j] * w[j])
			omf := q15One - f
			var v [2]float32
			for c, x := range [2][]float32{x0, x1} {
				g := x[a+j-2 : a+j+3 : a+j+3]
				h := x[b+j-2 : b+j+3 : b+j+3]
				v[c] = x[xi+sp.start+j] +
					float32((omf*sp.g00)*g[2]) +
					float32((omf*sp.g01)*(g[3]+g[1])) +
					float32((omf*sp.g02)*(g[4]+g[0])) +
					float32((f*sp.g10)*h[2]) +
					float32((f*sp.g11)*(h[3]+h[1])) +
					float32((f*sp.g12)*(h[4]+h[0]))
			}
			y0[j] = v[0]
			y1[j] = v[1]
			tmp0 := v[0] + verySmall + m0
			tmp1 := v[1] + verySmall + m1
			m0 = float32(coef * tmp0)
			m1 = float32(coef * tmp1)
			out[2*j] = tmp0 * (1 / celtSigScale)
			out[2*j+1] = tmp1 * (1 / celtSigScale)
		}
	}
	mem[0], mem[1] = m0, m1
}

// celtSynthesis de-normalises the bands of X with the energies oldBandE and
// computes the inverse MDCTs of C coded channels into the CC output
// channels outSyn, upmixing or downmixing as needed (celt_synthesis).
// outSyn[c] starts where the frame starts in the channel's history.
func celtSynthesis(st *Decoder, m *Mode, X []float32, outSyn [2][]float32, oldBandE []float32, start, effEnd, C, CC int,
	isTransient bool, LM, downsample int, silence bool,
) {
	var B, NB, shift int
	overlap := m.overlap
	nbEBands := m.nbEBands
	N := m.shortMdctSize << LM
	freq := st.freq[:N] // Interleaved signal MDCTs
	M := 1 << LM

	if isTransient {
		B = M
		NB = m.shortMdctSize
		shift = m.maxLM
	} else {
		B = 1
		NB = m.shortMdctSize << LM
		shift = m.maxLM - LM
	}

	switch {
	case CC == 2 && C == 1:
		// Copying a mono streams to two channels.
		denormaliseBands(m, X, freq, oldBandE, start, effEnd, M, downsample, silence)
		// Store a temporary copy in the output buffer because the IMDCT
		// destroys its input.
		freq2 := outSyn[1][overlap/2 : overlap/2+N]
		copy(freq2, freq)
		for b := 0; b < B; b++ {
			m.mdct.cltMdctBackward(freq2[b:], outSyn[0][NB*b:], m.window, overlap, shift, B)
		}
		for b := 0; b < B; b++ {
			m.mdct.cltMdctBackward(freq[b:], outSyn[1][NB*b:], m.window, overlap, shift, B)
		}
	case CC == 1 && C == 2:
		// Downmixing a stereo stream to mono.
		freq2 := outSyn[0][overlap/2 : overlap/2+N]
		denormaliseBands(m, X, freq, oldBandE, start, effEnd, M, downsample, silence)
		// Use the output buffer as temp array before downmixing.
		denormaliseBands(m, X[N:], freq2, oldBandE[nbEBands:], start, effEnd, M, downsample, silence)
		for i := range freq {
			freq[i] = float32(0.5*freq[i]) + float32(0.5*freq2[i])
		}
		for b := 0; b < B; b++ {
			m.mdct.cltMdctBackward(freq[b:], outSyn[0][NB*b:], m.window, overlap, shift, B)
		}
	default:
		// Normal case (mono or stereo).
		for c := 0; c < CC; c++ {
			denormaliseBands(m, X[c*N:], freq, oldBandE[c*nbEBands:], start, effEnd, M, downsample, silence)
			for b := 0; b < B; b++ {
				m.mdct.cltMdctBackward(freq[b:], outSyn[c][NB*b:], m.window, overlap, shift, B)
			}
		}
	}
	// Saturating the IMDCT output (SATURATE(x, SIG_SAT)) does nothing in the
	// float build.
}

// tfDecode reads, for each band, how far its time-frequency resolution
// moves from the frame default, into tfRes (tf_decode).
func tfDecode(start, end int, isTransient bool, tfRes []int, LM int, dec *entcode.Decoder) {
	budget := dec.Storage() * 8
	tell := dec.Tell()
	transient := b2i(isTransient)
	logp := 4
	if isTransient {
		logp = 2
	}
	tfSelectRsv := 0
	if LM > 0 && tell+logp+1 <= budget {
		tfSelectRsv = 1
	}
	budget -= tfSelectRsv
	tfChanged := 0
	curr := 0
	for i := start; i < end; i++ {
		if tell+logp <= budget {
			curr ^= dec.DecBitLogp(uint(logp))
			tell = dec.Tell()
			tfChanged |= curr
		}
		tfRes[i] = curr
		logp = 5
		if isTransient {
			logp = 4
		}
	}
	tfSelect := 0
	if tfSelectRsv != 0 &&
		tfSelectTable[LM][4*transient+0+tfChanged] != tfSelectTable[LM][4*transient+2+tfChanged] {
		tfSelect = dec.DecBitLogp(1)
	}
	for i := start; i < end; i++ {
		tfRes[i] = tfSelectTable[LM][4*transient+2*tfSelect+tfRes[i]]
	}
}

// celtPlcPitchSearch returns the pitch period of the decoded history for the
// pitch-based PLC (celt_plc_pitch_search).
func celtPlcPitchSearch(st *Decoder, decodeMem [2][]float32, C int) int {
	lpPitchBuf := st.lpPitchBuf[:]
	pitchDownsample(decodeMem, lpPitchBuf, decodeBufferSize, C)
	pitchIndex := pitchSearch(lpPitchBuf[plcPitchLagMax>>1:], lpPitchBuf,
		decodeBufferSize-plcPitchLagMax, plcPitchLagMax-plcPitchLagMin)
	return plcPitchLagMax - pitchIndex
}

// prefilterAndFold applies the pre-filter to the MDCT overlap of the
// concealed audio for the next frame, because the post-filter will be
// re-applied in the decoder after the MDCT overlap, and simulates TDAC on it
// so that it blends with the MDCT of the next frame (prefilter_and_fold).
func prefilterAndFold(st *Decoder, N int) {
	mode := st.mode
	overlap := st.overlap
	CC := st.channels
	etmp := st.etmp[:overlap]
	for c := 0; c < CC; c++ {
		decodeMem := st.decodeMem[c][:]
		// Apply the pre-filter to the MDCT overlap for the next frame
		// because the post-filter will be re-applied in the decoder after
		// the MDCT overlap.
		combFilter(etmp, decodeMem, decodeBufferSize-N, st.postfilterPeriodOld, st.postfilterPeriod, overlap,
			-st.postfilterGainOld, -st.postfilterGain, st.postfilterTapsetOld, st.postfilterTapset, nil, 0)

		// Simulate TDAC on the concealed audio so that it blends with the
		// MDCT of the next frame.
		for i := 0; i < overlap/2; i++ {
			decodeMem[decodeBufferSize-N+i] = float32(mode.window[i]*etmp[overlap-1-i]) +
				float32(mode.window[overlap-i-1]*etmp[i])
		}
	}
}

// plcLagWindow is the float product 0.008f*0.008f of the PLC's lag window.
const plcLagWindow = float32(0.008) * float32(0.008)

// celtDecodeLost conceals a lost frame of N samples: noise-based after long
// losses, in hybrid mode and before two good frames, else pitch-based
// (celt_decode_lost, without deep PLC).
func celtDecodeLost(st *Decoder, N, LM int) {
	C := st.channels
	var decodeMem, outSyn [2][]float32
	mode := st.mode
	nbEBands := mode.nbEBands
	overlap := mode.overlap
	eBands := mode.eBands

	for c := 0; c < C; c++ {
		decodeMem[c] = st.decodeMem[c][:]
		outSyn[c] = decodeMem[c][decodeBufferSize-N:]
	}
	oldBandE := st.oldBandE[:]
	backgroundLogE := st.backgroundLogE[:]

	lossDuration := st.lossDuration
	start := st.start
	noiseBased := lossDuration >= 40 || start != 0 || st.skipPLC
	if noiseBased {
		// Noise-based PLC/CNG.
		end := st.end
		effEnd := max(start, min(end, mode.effEBands))

		X := st.x[:C*N] // Interleaved normalised MDCTs
		for c := 0; c < C; c++ {
			copy(decodeMem[c][:decodeBufferSize-N+overlap], decodeMem[c][N:])
		}

		if st.prefilterAndFold {
			prefilterAndFold(st, N)
		}

		// Energy decay.
		decay := float32(1.5)
		if lossDuration != 0 {
			decay = 0.5
		}
		for c := 0; c < C; c++ {
			for i := start; i < end; i++ {
				oldBandE[c*nbEBands+i] = max16(backgroundLogE[c*nbEBands+i], oldBandE[c*nbEBands+i]-decay)
			}
		}
		seed := st.rng
		for c := 0; c < C; c++ {
			for i := start; i < effEnd; i++ {
				boffs := N*c + eBands[i]<<LM
				blen := (eBands[i+1] - eBands[i]) << LM
				for j := 0; j < blen; j++ {
					seed = celtLcgRand(seed)
					X[boffs+j] = float32(int32(seed) >> 20)
				}
				renormaliseVector(X[boffs:], blen, q15One)
			}
		}
		st.rng = seed

		celtSynthesis(st, mode, X, outSyn, oldBandE, start, effEnd, C, C, false, LM, st.downsample, false)
		st.prefilterAndFold = false
		// Skip regular PLC until we get two consecutive packets.
		st.skipPLC = true
	} else {
		// Pitch-based PLC.
		var pitchIndex int
		fade := q15One

		if lossDuration == 0 {
			pitchIndex = celtPlcPitchSearch(st, decodeMem, C)
			st.lastPitchIndex = pitchIndex
		} else {
			pitchIndex = st.lastPitchIndex
			fade = 0.8
		}

		// We want the excitation for 2 pitch periods in order to look for a
		// decaying signal, but we can't get more than MAX_PERIOD.
		excLength := min(2*pitchIndex, maxPeriod)

		// exc[i] is libopus's exc[i-CELT_LPC_ORDER]: the excitation of the
		// last MAX_PERIOD samples after CELT_LPC_ORDER samples of history.
		exc := st.exc[:]
		firTmp := st.firTmp[:excLength]
		window := mode.window
		for c := 0; c < C; c++ {
			var S1 float32
			buf := decodeMem[c]
			lpc := st.lpc[c][:]

			for i := 0; i < maxPeriod+lpcOrder; i++ {
				exc[i] = buf[decodeBufferSize-maxPeriod-lpcOrder+i]
			}

			if lossDuration == 0 {
				var ac [lpcOrder + 1]float32
				// Compute LPC coefficients for the last MAX_PERIOD samples
				// before the first loss so we can work in the
				// excitation-filter domain.
				celtAutocorr(exc[lpcOrder:], ac[:], window, overlap, lpcOrder, maxPeriod)
				// Add a noise floor of -40 dB.
				ac[0] *= 1.0001
				// Use lag windowing to stabilize the Levinson-Durbin
				// recursion.
				for i := 1; i <= lpcOrder; i++ {
					// ac[i] *= exp(-.5*(2*M_PI*.002*i)*(2*M_PI*.002*i));
					ac[i] -= float32(ac[i] * plcLagWindow * float32(i) * float32(i))
				}
				celtLpc(lpc, ac[:], lpcOrder)
			}
			// Initialize the LPC history with the samples just before the
			// start of the region for which we're computing the excitation.
			// Compute the excitation for exc_length samples before the loss.
			// We need the copy because celt_fir() cannot filter in-place.
			celtFir(exc[maxPeriod-excLength:], lpc, firTmp, excLength, lpcOrder)
			copy(exc[lpcOrder+maxPeriod-excLength:], firTmp)

			// Check if the waveform is decaying, and if so how fast. We do
			// this to avoid adding energy when concealing in a segment with
			// decaying energy.
			var decay float32
			{
				E1 := float32(1)
				E2 := float32(1)
				decayLength := excLength >> 1
				for i := 0; i < decayLength; i++ {
					e := exc[lpcOrder+maxPeriod-decayLength+i]
					E1 += float32(e * e)
					e = exc[lpcOrder+maxPeriod-2*decayLength+i]
					E2 += float32(e * e)
				}
				E1 = min32(E1, E2)
				decay = celtSqrt(E1 / E2)
			}

			// Move the decoder memory one frame to the left to give us room
			// to add the data for the new frame. We ignore the overlap that
			// extends past the end of the buffer, because we aren't going to
			// use it.
			copy(buf[:decodeBufferSize-N], buf[N:decodeBufferSize])

			// Extrapolate from the end of the excitation with a period of
			// "pitch_index", scaling down each period by an additional
			// factor of "decay".
			extrapolationOffset := maxPeriod - pitchIndex
			// We need to extrapolate enough samples to cover a complete MDCT
			// window (including overlap/2 samples on both sides).
			extrapolationLen := N + overlap
			// We also apply fading if this is not the first loss.
			attenuation := fade * decay
			for i, j := 0, 0; i < extrapolationLen; i, j = i+1, j+1 {
				if j >= pitchIndex {
					j -= pitchIndex
					attenuation *= decay
				}
				buf[decodeBufferSize-N+i] = attenuation * exc[lpcOrder+extrapolationOffset+j]
				// Compute the energy of the previously decoded signal whose
				// excitation we're copying.
				tmp := buf[decodeBufferSize-maxPeriod-N+extrapolationOffset+j]
				S1 += float32(tmp * tmp)
			}
			{
				var lpcMem [lpcOrder]float32
				// Copy the last decoded samples (prior to the overlap
				// region) to synthesis filter memory so we can have a
				// continuous signal.
				for i := 0; i < lpcOrder; i++ {
					lpcMem[i] = buf[decodeBufferSize-N-1-i]
				}
				// Apply the synthesis filter to convert the excitation back
				// into the signal domain.
				celtIir(buf[decodeBufferSize-N:], lpc, buf[decodeBufferSize-N:], extrapolationLen, lpcOrder, lpcMem[:])
			}

			// Check if the synthesis energy is higher than expected, which
			// can happen with the signal changes during our window. If so,
			// attenuate.
			{
				var S2 float32
				for i := 0; i < extrapolationLen; i++ {
					tmp := buf[decodeBufferSize-N+i]
					S2 += float32(tmp * tmp)
				}
				// This checks for an "explosion" in the synthesis. The float
				// test is written this way to catch NaNs in the output of
				// the IIR filter at the same time.
				if !(S1 > float32(0.2*S2)) {
					clear(buf[decodeBufferSize-N : decodeBufferSize-N+extrapolationLen])
				} else if S1 < S2 {
					ratio := celtSqrt((S1 + 1) / (S2 + 1))
					for i := 0; i < overlap; i++ {
						tmpG := q15One - float32(window[i]*(q15One-ratio))
						buf[decodeBufferSize-N+i] = tmpG * buf[decodeBufferSize-N+i]
					}
					for i := overlap; i < extrapolationLen; i++ {
						buf[decodeBufferSize-N+i] = ratio * buf[decodeBufferSize-N+i]
					}
				}
			}
		}
		st.prefilterAndFold = true
	}

	// Saturate to something large to avoid wrap-around.
	st.lossDuration = min(10000, lossDuration+(1<<LM))
}

// DecodeWithEC decodes one CELT frame of frameSize samples per channel at
// the output rate (2.5, 5, 10 or 20 ms: 120, 240, 480 or 960 samples at 48
// kHz) into pcm, interleaved with Channels() channels, and returns
// frameSize (celt_decode_with_ec). data is the frame: when dec is nil the
// decoder reads it with its own range decoder, otherwise dec is positioned
// in data (after the SILK layer of a hybrid frame) and data only gives the
// frame length. A nil data, or one of at most one byte, conceals a lost
// frame.
func (st *Decoder) DecodeWithEC(data []byte, pcm []float32, frameSize int, dec *entcode.Decoder) (int, error) {
	var decodeMem, outSyn [2][]float32
	CC := st.channels
	C := st.streamChannels
	mode := st.mode
	nbEBands := mode.nbEBands
	overlap := mode.overlap
	eBands := mode.eBands
	start := st.start
	end := st.end
	frameSize *= st.downsample

	oldBandE := st.oldBandE[:]
	oldLogE := st.oldLogE[:]
	oldLogE2 := st.oldLogE2[:]
	backgroundLogE := st.backgroundLogE[:]

	LM := 0
	for ; LM <= mode.maxLM; LM++ {
		if mode.shortMdctSize<<LM == frameSize {
			break
		}
	}
	if LM > mode.maxLM {
		return 0, ErrBadArg
	}
	M := 1 << LM

	if len(data) > 1275 || pcm == nil {
		return 0, ErrBadArg
	}
	// validate_celt_decoder, which libopus's hardened builds call, asserts
	// start < end; without it the allocation divides by zero.
	if start >= end {
		return 0, ErrBadArg
	}
	if len(pcm) < frameSize/st.downsample*CC {
		return 0, ErrBufferTooSmall
	}

	N := M * mode.shortMdctSize
	for c := 0; c < CC; c++ {
		decodeMem[c] = st.decodeMem[c][:]
		outSyn[c] = decodeMem[c][decodeBufferSize-N:]
	}

	effEnd := min(end, mode.effEBands)

	if len(data) <= 1 {
		celtDecodeLost(st, N, LM)
		deemphasis(outSyn, pcm, N, CC, st.downsample, mode.preemph[:], st.preemphMemD[:], st.freq[:])
		return frameSize / st.downsample, nil
	}

	// Check if there are at least two packets received consecutively before
	// turning on the pitch-based PLC.
	if st.lossDuration == 0 {
		st.skipPLC = false
	}

	if dec == nil {
		st.ec.Init(data)
		dec = &st.ec
	}

	if C == 1 {
		for i := 0; i < nbEBands; i++ {
			oldBandE[i] = max16(oldBandE[i], oldBandE[nbEBands+i])
		}
	}

	totalBits := len(data) * 8
	tell := dec.Tell()

	silence := false
	if tell >= totalBits {
		silence = true
	} else if tell == 1 {
		silence = dec.DecBitLogp(15) != 0
	}
	if silence {
		// Pretend we've read all the remaining bits.
		tell = len(data) * 8
		dec.SetTell(tell)
	}

	var postfilterGain float32
	postfilterPitch := 0
	postfilterTapset := 0
	if start == 0 && tell+16 <= totalBits {
		if dec.DecBitLogp(1) != 0 {
			octave := int(dec.DecUint(6))
			postfilterPitch = (16 << octave) + int(dec.DecBits(uint(4+octave))) - 1
			qg := int(dec.DecBits(3))
			if dec.Tell()+2 <= totalBits {
				postfilterTapset = dec.DecICDF(tapsetICDF[:], 2)
			}
			postfilterGain = 0.09375 * float32(qg+1)
		}
		tell = dec.Tell()
	}

	isTransient := false
	if LM > 0 && tell+3 <= totalBits {
		isTransient = dec.DecBitLogp(3) != 0
		tell = dec.Tell()
	}

	shortBlocks := isTransient

	// Decode the global flags (first symbols in the stream).
	intraEner := false
	if tell+3 <= totalBits {
		intraEner = dec.DecBitLogp(3) != 0
	}
	// If recovering from packet loss, make sure we make the energy
	// prediction safe to reduce the risk of getting loud artifacts.
	if !intraEner && st.lossDuration != 0 {
		for c := 0; c < 2; c++ {
			var safety float32
			missing := min(10, st.lossDuration>>LM)
			if LM == 0 {
				safety = 1.5
			} else if LM == 1 {
				safety = 0.5
			}
			for i := start; i < end; i++ {
				k := c*nbEBands + i
				if oldBandE[k] < max16(oldLogE[k], oldLogE2[k]) {
					// If energy is going down already, continue the trend.
					E0 := oldBandE[k]
					E1 := oldLogE[k]
					E2 := oldLogE2[k]
					slope := max32(E1-E0, 0.5*(E2-E0))
					E0 -= max32(0, float32(float32(1+missing)*slope))
					oldBandE[k] = max32(-20, E0)
				} else {
					// Otherwise take the min of the last frames.
					oldBandE[k] = min16(min16(oldBandE[k], oldLogE[k]), oldLogE2[k])
				}
				// Shorter frames have more natural fluctuations -- play it
				// safe.
				oldBandE[k] -= safety
			}
		}
	}
	// Get band energies.
	unquantCoarseEnergy(mode, start, end, oldBandE, intraEner, dec, C, LM)

	tfRes := st.tfRes[:nbEBands]
	tfDecode(start, end, isTransient, tfRes, LM, dec)

	tell = dec.Tell()
	spreadDecision := spreadNormal
	if tell+4 <= totalBits {
		spreadDecision = dec.DecICDF(spreadICDF[:], 5)
	}

	caps := st.caps[:nbEBands]
	initCaps(mode, caps, LM, C)

	offsets := st.offsets[:nbEBands]
	dynallocLogp := 6
	totalBits <<= bitRes
	tell = dec.TellFrac()
	for i := start; i < end; i++ {
		width := C * (eBands[i+1] - eBands[i]) << LM
		// quanta is 6 bits, but no more than 1 bit/sample and no less than
		// 1/8 bit/sample.
		quanta := min(width<<bitRes, max(6<<bitRes, width))
		dynallocLoopLogp := dynallocLogp
		boost := 0
		for tell+(dynallocLoopLogp<<bitRes) < totalBits && boost < caps[i] {
			flag := dec.DecBitLogp(uint(dynallocLoopLogp))
			tell = dec.TellFrac()
			if flag == 0 {
				break
			}
			boost += quanta
			totalBits -= quanta
			dynallocLoopLogp = 1
		}
		offsets[i] = boost
		// Making dynalloc more likely.
		if boost > 0 {
			dynallocLogp = max(2, dynallocLogp-1)
		}
	}

	fineQuant := st.fineQuant[:nbEBands]
	allocTrim := 5
	if tell+(6<<bitRes) <= totalBits {
		allocTrim = dec.DecICDF(trimICDF[:], 7)
	}

	bits := (len(data)*8)<<bitRes - dec.TellFrac() - 1
	antiCollapseRsv := 0
	if isTransient && LM >= 2 && bits >= (LM+2)<<bitRes {
		antiCollapseRsv = 1 << bitRes
	}
	bits -= antiCollapseRsv

	pulses := st.pulses[:nbEBands]
	finePriority := st.finePriority[:nbEBands]

	var intensity, balance int
	var dualStereo bool
	codedBands := cltComputeAllocation(mode, start, end, offsets, caps, allocTrim, &intensity, &dualStereo,
		bits, &balance, pulses, fineQuant, finePriority, C, LM, dec)

	unquantFineEnergy(mode, start, end, oldBandE, fineQuant, dec, C)

	for c := 0; c < CC; c++ {
		copy(decodeMem[c][:decodeBufferSize-N+overlap], decodeMem[c][N:])
	}

	// Decode fixed codebook.
	collapseMasks := st.collapseMasks[:C*nbEBands]

	X := st.x[:C*N] // Interleaved normalised MDCTs
	var Y []float32
	if C == 2 {
		Y = X[N:]
	}

	quantAllBands(&st.bands, mode, start, end, X[:N], Y, collapseMasks, pulses, shortBlocks, spreadDecision,
		dualStereo, intensity, tfRes, len(data)*(8<<bitRes)-antiCollapseRsv, balance, dec, LM, codedBands,
		&st.rng, st.disableInv, st.norm[:])

	antiCollapseOn := false
	if antiCollapseRsv > 0 {
		antiCollapseOn = dec.DecBits(1) != 0
	}

	unquantEnergyFinalise(mode, start, end, oldBandE, fineQuant, finePriority, len(data)*8-dec.Tell(), dec, C)

	if antiCollapseOn {
		antiCollapse(mode, X, collapseMasks, LM, C, N, start, end, oldBandE, oldLogE, oldLogE2, pulses, st.rng)
	}

	if silence {
		for i := 0; i < C*nbEBands; i++ {
			oldBandE[i] = -28
		}
	}
	if st.prefilterAndFold {
		prefilterAndFold(st, N)
	}
	celtSynthesis(st, mode, X, outSyn, oldBandE, start, effEnd, C, CC, isTransient, LM, st.downsample, silence)

	st.postfilterPeriod = max(st.postfilterPeriod, combFilterMinPeriod)
	st.postfilterPeriodOld = max(st.postfilterPeriodOld, combFilterMinPeriod)
	// Without downsampling, the post-filter runs in the de-emphasis loop;
	// nothing between the two reads or writes the signal or the
	// de-emphasis memory.
	fused := st.downsample == 1
	if fused {
		var spanBuf [4]combSpan
		spans := combSpans(spanBuf[:0], 0, st.postfilterPeriodOld, st.postfilterPeriod, mode.shortMdctSize,
			st.postfilterGainOld, st.postfilterGain, st.postfilterTapsetOld, st.postfilterTapset, overlap)
		if LM != 0 {
			spans = combSpans(spans, mode.shortMdctSize, st.postfilterPeriod, postfilterPitch, N-mode.shortMdctSize,
				st.postfilterGain, postfilterGain, st.postfilterTapset, postfilterTapset, overlap)
		}
		coef := mode.preemph[0]
		if CC == 2 {
			postfilterDeemphasisStereo(decodeMem[0], decodeMem[1], decodeBufferSize-N, spans, mode.window,
				pcm[:2*N], coef, st.preemphMemD[:])
		} else {
			st.preemphMemD[0] = postfilterDeemphasis(decodeMem[0], decodeBufferSize-N, spans, mode.window,
				pcm[:N], coef, st.preemphMemD[0])
		}
	} else {
		for c := 0; c < CC; c++ {
			combFilter(outSyn[c], decodeMem[c], decodeBufferSize-N, st.postfilterPeriodOld, st.postfilterPeriod,
				mode.shortMdctSize, st.postfilterGainOld, st.postfilterGain, st.postfilterTapsetOld, st.postfilterTapset,
				mode.window, overlap)
			if LM != 0 {
				combFilter(outSyn[c][mode.shortMdctSize:], decodeMem[c], decodeBufferSize-N+mode.shortMdctSize,
					st.postfilterPeriod, postfilterPitch, N-mode.shortMdctSize, st.postfilterGain, postfilterGain,
					st.postfilterTapset, postfilterTapset, mode.window, overlap)
			}
		}
	}
	st.postfilterPeriodOld = st.postfilterPeriod
	st.postfilterGainOld = st.postfilterGain
	st.postfilterTapsetOld = st.postfilterTapset
	st.postfilterPeriod = postfilterPitch
	st.postfilterGain = postfilterGain
	st.postfilterTapset = postfilterTapset
	if LM != 0 {
		st.postfilterPeriodOld = st.postfilterPeriod
		st.postfilterGainOld = st.postfilterGain
		st.postfilterTapsetOld = st.postfilterTapset
	}

	if C == 1 {
		copy(oldBandE[nbEBands:2*nbEBands], oldBandE[:nbEBands])
	}

	if !isTransient {
		copy(oldLogE2[:2*nbEBands], oldLogE[:2*nbEBands])
		copy(oldLogE[:2*nbEBands], oldBandE[:2*nbEBands])
	} else {
		for i := 0; i < 2*nbEBands; i++ {
			oldLogE[i] = min16(oldLogE[i], oldBandE[i])
		}
	}
	// In normal circumstances, we only allow the noise floor to increase by
	// up to 2.4 dB/second, but when we're in DTX we give the weight of all
	// missing packets to the update packet.
	maxBackgroundIncrease := float32(float32(min(160, st.lossDuration+M)) * 0.001)
	for i := 0; i < 2*nbEBands; i++ {
		backgroundLogE[i] = min16(backgroundLogE[i]+maxBackgroundIncrease, oldBandE[i])
	}
	// In case start or end were to change.
	for c := 0; c < 2; c++ {
		for i := 0; i < start; i++ {
			oldBandE[c*nbEBands+i] = 0
			oldLogE[c*nbEBands+i] = -28
			oldLogE2[c*nbEBands+i] = -28
		}
		for i := end; i < nbEBands; i++ {
			oldBandE[c*nbEBands+i] = 0
			oldLogE[c*nbEBands+i] = -28
			oldLogE2[c*nbEBands+i] = -28
		}
	}
	st.rng = dec.Rng()

	if !fused {
		deemphasis(outSyn, pcm, N, CC, st.downsample, mode.preemph[:], st.preemphMemD[:], st.freq[:])
	}
	st.lossDuration = 0
	st.prefilterAndFold = false
	if dec.Tell() > 8*len(data) {
		return 0, ErrInternal
	}
	if dec.HasError() {
		st.errorFlag = 1
	}
	return frameSize / st.downsample, nil
}

// SetStartBand sets the first coded band, 0 for CELT and 17 for hybrid
// frames (CELT_SET_START_BAND).
func (st *Decoder) SetStartBand(start int) error {
	if start < 0 || start >= st.mode.nbEBands {
		return ErrBadArg
	}
	st.start = start
	return nil
}

// SetEndBand sets the end of the coded bands: 13 for narrowband, 17 for
// medium and wideband, 19 for super-wideband and 21 for fullband
// (CELT_SET_END_BAND).
func (st *Decoder) SetEndBand(end int) error {
	if end < 1 || end > st.mode.nbEBands {
		return ErrBadArg
	}
	st.end = end
	return nil
}

// SetStreamChannels sets the number of coded channels of the frames to
// decode, 1 or 2; the output keeps the decoder's channel count
// (CELT_SET_CHANNELS).
func (st *Decoder) SetStreamChannels(channels int) error {
	if channels < 1 || channels > 2 {
		return ErrBadArg
	}
	st.streamChannels = channels
	return nil
}

// GetAndClearError returns 1 if a frame since the last call had an invalid
// value in a range-coded integer, and clears the flag
// (CELT_GET_AND_CLEAR_ERROR).
func (st *Decoder) GetAndClearError() int {
	e := st.errorFlag
	st.errorFlag = 0
	return e
}

// Reset clears the decoder state as at the start of a stream
// (OPUS_RESET_STATE). The configuration (bands, stream channels, phase
// inversion) is kept.
func (st *Decoder) Reset() {
	st.decoderState = decoderState{}
	for i := 0; i < 2*st.mode.nbEBands; i++ {
		st.oldLogE[i] = -28
		st.oldLogE2[i] = -28
	}
	st.skipPLC = true
}

// State is a saved copy of a Decoder's state (SaveState, RestoreState).
type State struct {
	s                          decoderState
	start, end, streamChannels int
}

// SaveState copies into s everything a later frame depends on: the state
// Reset clears and the band and stream channel settings. It is not in
// libopus: package opus saves the state before a packet it may reject after
// CELT has decoded some of its frames.
func (st *Decoder) SaveState(s *State) {
	s.s = st.decoderState
	s.start, s.end, s.streamChannels = st.start, st.end, st.streamChannels
}

// RestoreState returns the decoder to the state SaveState saved in s.
func (st *Decoder) RestoreState(s *State) {
	st.decoderState = s.s
	st.start, st.end, st.streamChannels = s.start, s.end, s.streamChannels
}

// Pitch returns the post-filter period of the last frame (OPUS_GET_PITCH).
func (st *Decoder) Pitch() int {
	return st.postfilterPeriod
}

// Mode returns the decoder's mode (CELT_GET_MODE).
func (st *Decoder) Mode() *Mode {
	return st.mode
}

// FinalRange returns the range coder state after the last decoded frame
// (OPUS_GET_FINAL_RANGE).
func (st *Decoder) FinalRange() uint32 {
	return st.rng
}

// SetPhaseInversionDisabled disables or enables the decoding of stereo phase
// inversion (OPUS_SET_PHASE_INVERSION_DISABLED).
func (st *Decoder) SetPhaseInversionDisabled(disabled bool) {
	st.disableInv = disabled
}

// PhaseInversionDisabled reports whether stereo phase inversion is disabled
// (OPUS_GET_PHASE_INVERSION_DISABLED).
func (st *Decoder) PhaseInversionDisabled() bool {
	return st.disableInv
}

// Channels returns the number of output channels.
func (st *Decoder) Channels() int {
	return st.channels
}
