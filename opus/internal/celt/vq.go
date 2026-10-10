// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2009 Xiph.Org Foundation
// Written by Jean-Marc Valin
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
// Ported from libopus 1.5.2 celt/vq.c and celt/vq.h (the decoder side).

package celt

import "github.com/fgn/puropus/opus/internal/entcode"

// expRotation1 applies one pass of Givens rotations by (c, s) to pairs of
// samples stride apart, forward then backward (exp_rotation1). Stride 1 is
// expRotation1Stride1.
func expRotation1(X []float32, n, stride int, c, s float32) {
	ms := -s
	if n <= stride {
		return
	}
	// a[i] is X[i] and b[i] is X[i+stride]: the slices overlap as the C
	// pointers do.
	a := X[:n-stride]
	b := X[stride:n]
	b = b[:len(a)]
	for i := range a {
		x1 := a[i]
		x2 := b[i]
		b[i] = float32(c*x2) + float32(s*x1)
		a[i] = float32(c*x1) + float32(ms*x2)
	}
	if n <= 2*stride {
		return
	}
	a = X[:n-2*stride]
	b = X[stride : n-stride]
	b = b[:len(a)]
	for i := len(a) - 1; i >= 0; i-- {
		x1 := a[i]
		x2 := b[i]
		b[i] = float32(c*x2) + float32(s*x1)
		a[i] = float32(c*x1) + float32(ms*x2)
	}
}

// expRotation1Stride1 is expRotation1 with stride 1. Each step of either
// pass reads the value the step before it wrote, which this version carries
// in a register instead of reloading it; the arithmetic is the same.
func expRotation1Stride1(X []float32, n int, c, s float32) {
	ms := -s
	if n < 2 {
		return
	}
	X = X[:n]
	// Forward: step i-1 of exp_rotation1 rotates (X[i-1], X[i]); x1 is the
	// X[i-1] the step before wrote.
	x1 := X[0]
	for i := 1; i < len(X); i++ {
		x2 := X[i]
		X[i-1] = float32(c*x1) + float32(ms*x2)
		x1 = float32(c*x2) + float32(s*x1)
	}
	X[n-1] = x1
	if n < 3 {
		return
	}
	// Backward: step i rotates (X[i], X[i+1]); x2 is the X[i+1] the step
	// before wrote.
	x2 := X[n-2]
	for i := n - 3; i >= 0; i-- {
		x1 := X[i]
		X[i+1] = float32(c*x2) + float32(s*x1)
		x2 = float32(c*x1) + float32(ms*x2)
	}
	X[0] = x2
}

// spreadFactor is the rotation strength of each spreading decision
// (SPREAD_FACTOR in exp_rotation).
var spreadFactor = [3]int{15, 10, 5}

// rotationGain is the cosine and sine of exp_rotation's angle.
type rotationGain struct{ c, s float32 }

// rotationGainOf computes exp_rotation's c and s for a band of n samples
// with K pulses and the given spreading decision.
func rotationGainOf(n, K, spread int) rotationGain {
	factor := spreadFactor[spread-1]
	gain := float32(n) / float32(n+factor*K)
	theta := float32(0.5 * (gain * gain))
	return rotationGain{
		c: celtCosNorm(theta),
		s: celtCosNorm(q15One - theta), // sin(theta)
	}
}

// rotationGains holds rotationGainOf for the band sizes quant_partition can
// reach in the static mode and every K that exp_rotation rotates with
// (2K < n), computed once with the same arithmetic (initTables): the gains
// of size n start at rotationGainOffset[n], or rotationGainOffset[n] is -1.
var (
	rotationGainOffset [maxBandSize + 1]int32
	rotationGains      [3][]rotationGain
)

// buildRotationGains fills rotationGains (initTables).
func buildRotationGains() {
	m := StaticMode()
	var reachable [maxBandSize + 1]bool
	for LM := 0; LM <= m.maxLM; LM++ {
		for i := 0; i < m.nbEBands; i++ {
			// quant_partition halves N while N > 2, at most LM+1 times.
			N := (m.eBands[i+1] - m.eBands[i]) << LM
			reachable[N] = true
			for l := LM; l >= 0 && N > 2; l-- {
				N >>= 1
				reachable[N] = true
			}
		}
	}
	size := int32(0)
	for n := range rotationGainOffset {
		rotationGainOffset[n] = -1
		if reachable[n] {
			rotationGainOffset[n] = size
			size += int32((n + 1) / 2)
		}
	}
	for spread := 1; spread <= 3; spread++ {
		g := make([]rotationGain, size)
		for n, off := range rotationGainOffset {
			for K := 0; off >= 0 && 2*K < n; K++ {
				g[int(off)+K] = rotationGainOf(n, K, spread)
			}
		}
		rotationGains[spread-1] = g
	}
}

// expRotation spreads (dir > 0) or unspreads (dir < 0) the energy of the K
// pulses in X[0:n] over the stride interleaved blocks (exp_rotation, float
// build).
func expRotation(X []float32, n, dir, stride, K, spread int) {
	stride2 := 0
	if 2*K >= n || spread == spreadNone {
		return
	}
	var g rotationGain
	if off := rotationGainOffset[n]; off >= 0 {
		g = rotationGains[spread-1][int(off)+K]
	} else {
		g = rotationGainOf(n, K, spread)
	}
	c, s := g.c, g.s

	if n >= 8*stride {
		stride2 = 1
		// This is just a simple (equivalent) way of computing
		// sqrt(len/stride) with rounding. It's basically incrementing long
		// as (stride2+0.5)^2 < len/stride.
		for (stride2*stride2+stride2)*stride+(stride>>2) < n {
			stride2++
		}
	}
	n = int(celtUdiv(uint32(n), uint32(stride)))
	for i := 0; i < stride; i++ {
		if dir < 0 {
			if stride2 != 0 {
				expRotation1(X[i*n:], n, stride2, s, c)
			}
			expRotation1Stride1(X[i*n:], n, c, s)
		} else {
			expRotation1Stride1(X[i*n:], n, c, -s)
			if stride2 != 0 {
				expRotation1(X[i*n:], n, stride2, s, -c)
			}
		}
	}
}

// normaliseResidual scales the decoded pulses iy[0:n] by gain/sqrt(Ryy)
// into X (normalise_residual, float build).
func normaliseResidual(iy []int32, X []float32, n int, Ryy, gain float32) {
	g := celtRsqrt(Ryy) * gain
	iy = iy[:n]
	X = X[:n]
	if useAVX2 && n >= 16 {
		normaliseAVX2(X, iy, g)
		return
	}
	for i := range X {
		X[i] = g * float32(iy[i])
	}
}

// extractCollapseMask returns a bit per block of B that has pulses
// (extract_collapse_mask).
func extractCollapseMask(iy []int32, n, B int) uint {
	if B <= 1 {
		return 1
	}
	N0 := int(celtUdiv(uint32(n), uint32(B)))
	var collapseMask uint
	for i := 0; i < B; i++ {
		tmp := int32(0)
		for j := 0; j < N0; j++ {
			tmp |= iy[i*N0+j]
		}
		if tmp != 0 {
			collapseMask |= 1 << i
		}
	}
	return collapseMask
}

// algUnquant decodes the pulse vector of a band with K pulses and returns
// the normalised, unspread shape in X[0:n] with its collapse mask
// (alg_unquant). iy is scratch of n values.
func algUnquant(X []float32, n, K, spread, B int, dec *entcode.Decoder, gain float32, iy []int32) uint {
	iy = iy[:n]
	Ryy := decodePulses(iy, n, K, dec)
	normaliseResidual(iy, X, n, Ryy, gain)
	expRotation(X, n, -1, B, K, spread)
	return extractCollapseMask(iy, n, B)
}

// renormaliseVector scales X[0:n] to the norm gain (renormalise_vector,
// float build).
func renormaliseVector(X []float32, n int, gain float32) {
	E := epsilon + celtInnerProd(X, X, n)
	g := celtRsqrt(E) * gain
	X = X[:n]
	for i := range X {
		X[i] = g * X[i]
	}
}
