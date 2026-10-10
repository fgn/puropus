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
// Ported from libopus 1.5.2 celt/celt.c and celt/celt.h.

package celt

// Post-filter period limits (COMBFILTER_MAXPERIOD, COMBFILTER_MINPERIOD).
const (
	combFilterMaxPeriod = 1024
	combFilterMinPeriod = 15
)

// Inverse CDFs of the CELT frame header symbols (celt.h).
var (
	// trimICDF codes the allocation trim (trim_icdf).
	trimICDF = [11]uint8{126, 124, 119, 109, 87, 41, 19, 9, 4, 2, 0}
	// spreadICDF codes the spreading decision; probabilities NONE 21.875%,
	// LIGHT 6.25%, NORMAL 65.625%, AGGRESSIVE 6.25% (spread_icdf).
	spreadICDF = [4]uint8{25, 23, 2, 0}
	// tapsetICDF codes the post-filter tapset (tapset_icdf).
	tapsetICDF = [3]uint8{2, 1, 0}
)

// resamplingFactor returns the decimation from 48 kHz to rate, or 0 for an
// unsupported rate (resampling_factor).
func resamplingFactor(rate int) int {
	switch rate {
	case 48000:
		return 1
	case 24000:
		return 2
	case 16000:
		return 3
	case 12000:
		return 4
	case 8000:
		return 6
	default:
		return 0
	}
}

// combFilterConst applies the post-filter with constant taps to n samples:
// y[i] = x[xi+i] + g10*x[xi+i-T] + g11*(x[xi+i-T+1]+x[xi+i-T-1]) +
// g12*(x[xi+i-T+2]+x[xi+i-T-2]) (comb_filter_const_c, the generic
// version). y may alias x[xi:].
func combFilterConst(y []float32, x []float32, xi, T, n int, g10, g11, g12 float32) {
	x4 := x[xi-T-2]
	x3 := x[xi-T-1]
	x2 := x[xi-T]
	x1 := x[xi-T+1]
	y = y[:n]
	for i := range y {
		x0 := x[xi+i-T+2]
		y[i] = x[xi+i] + float32(g10*x2) + float32(g11*(x1+x3)) + float32(g12*(x0+x4))
		// SATURATE(y[i], SIG_SAT) does nothing in the float build.
		x4 = x3
		x3 = x2
		x2 = x1
		x1 = x0
	}
}

// combFilterGains are the post-filter taps of the three tapsets (gains in
// comb_filter).
var combFilterGains = [3][3]float32{
	{0.3066406250, 0.2170410156, 0.1296386719},
	{0.4638671875, 0.2680664062, 0},
	{0.7998046875, 0.1000976562, 0},
}

// combFilter applies the pitch post-filter to n samples, cross-fading over
// overlap samples from the filter (T0, g0, tapset0) to (T1, g1, tapset1)
// with the squared window (comb_filter). x[xi+i] is the input sample i, and
// the filter reads up to T+2 samples of history before x[xi]; y receives the
// n output samples and may alias x[xi:], in which case the filter reads its
// own output as libopus's in-place filtering does.
func combFilter(y []float32, x []float32, xi, T0, T1, n int, g0, g1 float32, tapset0, tapset1 int,
	window []float32, overlap int,
) {
	if g0 == 0 && g1 == 0 {
		copy(y[:n], x[xi:xi+n])
		return
	}
	// When the gain is zero, T0 and/or T1 is set to zero. We need to have
	// them be at least 2 to avoid processing garbage data.
	T0 = max(T0, combFilterMinPeriod)
	T1 = max(T1, combFilterMinPeriod)
	g00 := g0 * combFilterGains[tapset0][0]
	g01 := g0 * combFilterGains[tapset0][1]
	g02 := g0 * combFilterGains[tapset0][2]
	g10 := g1 * combFilterGains[tapset1][0]
	g11 := g1 * combFilterGains[tapset1][1]
	g12 := g1 * combFilterGains[tapset1][2]
	x1 := x[xi-T1+1]
	x2 := x[xi-T1]
	x3 := x[xi-T1-1]
	x4 := x[xi-T1-2]
	// If the filter didn't change, we don't need the overlap.
	if g0 == g1 && T0 == T1 && tapset0 == tapset1 {
		overlap = 0
	}
	i := 0
	for ; i < overlap; i++ {
		x0 := x[xi+i-T1+2]
		f := float32(window[i] * window[i])
		y[i] = x[xi+i] +
			float32(((q15One-f)*g00)*x[xi+i-T0]) +
			float32(((q15One-f)*g01)*(x[xi+i-T0+1]+x[xi+i-T0-1])) +
			float32(((q15One-f)*g02)*(x[xi+i-T0+2]+x[xi+i-T0-2])) +
			float32((f*g10)*x2) +
			float32((f*g11)*(x1+x3)) +
			float32((f*g12)*(x0+x4))
		x4 = x3
		x3 = x2
		x2 = x1
		x1 = x0
	}
	if g1 == 0 {
		copy(y[overlap:n], x[xi+overlap:xi+n])
		return
	}
	// Compute the part with the constant filter.
	combFilterConst(y[i:], x, xi+i, T1, n-i, g10, g11, g12)
}

// tfSelectTable is the TF change table, indexed by LM and
// 4*isTransient + 2*tf_select + per_band_flag. Positive values mean better
// frequency resolution (longer effective window), negative values better
// time resolution (shorter effective window) (tf_select_table).
var tfSelectTable = [4][8]int{
	// isTransient=0   isTransient=1
	{0, -1, 0, -1, 0, -1, 0, -1}, // 2.5 ms
	{0, -1, 0, -2, 1, 0, 1, -1},  // 5 ms
	{0, -2, 0, -3, 2, 0, 1, -1},  // 10 ms
	{0, -2, 0, -3, 3, 0, 1, -1},  // 20 ms
}

// initCaps computes the maximum allocation of each band for C channels and
// LM (init_caps).
func initCaps(m *Mode, caps []int, LM, C int) {
	for i := 0; i < m.nbEBands; i++ {
		N := (m.eBands[i+1] - m.eBands[i]) << LM
		caps[i] = (int(m.cache.caps[m.nbEBands*(2*LM+C-1)+i]) + 64) * C * N >> 2
	}
}
