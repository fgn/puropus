// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2009 Xiph.Org Foundation
// Copyright (c) 2008-2009 Gregory Maxwell
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
// Ported from libopus 1.5.2 celt/bands.c and celt/bands.h (the decoder side).

package celt

import (
	"math"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// Spreading decisions (bands.h).
const (
	spreadNone       = 0
	spreadLight      = 1
	spreadNormal     = 2
	spreadAggressive = 3
)

// celtLcgRand is the linear congruential generator of the folding and
// noise fills (celt_lcg_rand).
func celtLcgRand(seed uint32) uint32 {
	return 1664525*seed + 1013904223
}

// bitexactCos is a cos() approximation designed to be bit-exact on any
// platform. Bit exactness with this approximation is important because it
// has an impact on the bit allocation (bitexact_cos).
func bitexactCos(x int16) int16 {
	tmp := (4096 + int32(x)*int32(x)) >> 13
	x2 := int16(tmp)
	x2 = int16((32767 - int(x2)) + fracMul16(int(x2), -7651+fracMul16(int(x2), 8277+fracMul16(-626, int(x2)))))
	return 1 + x2
}

// bitexactLog2tan returns log2(isin/icos) in Q11, bit-exactly
// (bitexact_log2tan).
func bitexactLog2tan(isin, icos int) int {
	lc := ilog(uint32(icos))
	ls := ilog(uint32(isin))
	icos <<= 15 - lc
	isin <<= 15 - ls
	return (ls-lc)*(1<<11) +
		fracMul16(isin, fracMul16(isin, -2597)+7932) -
		fracMul16(icos, fracMul16(icos, -2597)+7932)
}

// log2tanTerm is one argument's term of bitexactLog2tan: the result is
// log2tanTerm(isin) - log2tanTerm(icos), exactly, in integers.
func log2tanTerm(v int) int {
	l := ilog(uint32(v))
	v <<= 15 - l
	return l*(1<<11) + fracMul16(v, fracMul16(v, -2597)+7932)
}

// thetaGain holds, for a split angle itheta, bitexactCos(itheta) and the
// log2tanTerm of that cosine.
type thetaGain struct {
	cos  int16
	term uint16
}

// thetaGains is thetaGain for itheta = 0..16384 (initTables), so that
// compute_theta's imid, iside and bitexact_log2tan(iside, imid) are three
// lookups. Angles so close to 0 that the cosine wraps to a negative int16
// (itheta < 64, which compute_theta's quantization never produces) keep
// a zero term.
var thetaGains []thetaGain

// buildThetaGains fills thetaGains (initTables).
func buildThetaGains() {
	thetaGains = make([]thetaGain, 16385)
	for t := 1; t < 16384; t++ {
		c := bitexactCos(int16(t))
		thetaGains[t].cos = c
		if c > 0 {
			thetaGains[t].term = uint16(log2tanTerm(int(c)))
		}
	}
}

// denormaliseBands de-normalises the energy to produce the synthesis from
// the unit-energy bands X into freq (denormalise_bands, float build).
func denormaliseBands(m *Mode, X []float32, freq []float32, bandLogE []float32, start, end, M, downsample int, silence bool) {
	eBands := m.eBands
	N := M * m.shortMdctSize
	bound := M * eBands[end]
	if downsample != 1 {
		bound = min(bound, N/downsample)
	}
	if silence {
		bound = 0
		start = 0
		end = 0
	}
	f := 0
	x := M * eBands[start]
	for i := 0; i < M*eBands[start]; i++ {
		freq[f] = 0
		f++
	}
	if useAVX2 && M == 8 {
		// Every band holds a multiple of 8 coefficients.
		var gains [maxBands]float32
		for i := start; i < end; i++ {
			lg := bandLogE[i] + eMeans[i]
			gains[i] = celtExp2(min32(32, lg))
		}
		denormaliseAVX2(X, freq, &gains, eBands, start, end)
		clear(freq[bound:N])
		return
	}
	for i := start; i < end; i++ {
		// f and x step together through the band, M*eBands[i] to
		// M*eBands[i+1].
		bandEnd := M * eBands[i+1]
		lg := bandLogE[i] + eMeans[i]
		g := celtExp2(min32(32, lg))
		src := X[x:bandEnd]
		dst := freq[f : f+len(src)]
		for j, v := range src {
			dst[j] = v * g
		}
		f += len(src)
		x += len(src)
	}
	clear(freq[bound:N])
}

// antiCollapse fills the short blocks of transient bands that received no
// pulses with noise, to prevent energy collapse (anti_collapse, float
// build). X_ holds C channels of size coefficients each.
func antiCollapse(m *Mode, X_ []float32, collapseMasks []uint8, LM, C, size, start, end int,
	logE, prev1logE, prev2logE []float32, pulses []int, seed uint32,
) {
	nb := m.nbEBands
	for i := start; i < end; i++ {
		N0 := m.eBands[i+1] - m.eBands[i]
		// depth in 1/8 bits
		depth := int(celtUdiv(uint32(1+pulses[i]), uint32(N0))) >> LM
		thresh := 0.5 * celtExp2(-0.125*float32(depth))
		sqrt1 := celtRsqrt(float32(N0 << LM))

		for c := 0; c < C; c++ {
			prev1 := prev1logE[c*nb+i]
			prev2 := prev2logE[c*nb+i]
			if C == 1 {
				prev1 = max16(prev1, prev1logE[nb+i])
				prev2 = max16(prev2, prev2logE[nb+i])
			}
			Ediff := logE[c*nb+i] - min16(prev1, prev2)
			Ediff = max32(0, Ediff)

			// r needs to be multiplied by 2 or 2*sqrt(2) depending on LM
			// because short blocks don't have the same energy as long.
			r := 2 * celtExp2(-Ediff)
			if LM == 3 {
				r *= 1.41421356
			}
			r = min16(thresh, r)
			r *= sqrt1
			X := X_[c*size+m.eBands[i]<<LM:]
			renormalize := false
			for k := 0; k < 1<<LM; k++ {
				// Detect collapse.
				if collapseMasks[i*C+c]&(1<<k) == 0 {
					// Fill with noise.
					// r, or -r when bit 15 of the seed is clear.
					rBits := math.Float32bits(r)
					for j := 0; j < N0; j++ {
						seed = celtLcgRand(seed)
						X[(j<<LM)+k] = math.Float32frombits(rBits ^ (^seed&0x8000)<<16)
					}
					renormalize = true
				}
			}
			// We just added some energy, so we need to renormalise.
			if renormalize {
				renormaliseVector(X, N0<<LM, q15One)
			}
		}
	}
}

// stereoMerge turns the decoded mid X and side Y back into left and right
// (stereo_merge, float build).
func stereoMerge(X, Y []float32, mid float32, N int) {
	// Compute the norm of X+Y and X-Y as |X|^2 + |Y|^2 +/- sum(xy).
	xp, side := dualInnerProd(Y, X, Y, N)
	// Compensating for the mid normalization.
	xp = float32(mid * xp)
	// mid and side are in Q15, not Q14 like X and Y; SHR16 does nothing in
	// the float build.
	mid2 := mid
	El := float32(mid2*mid2) + side - float32(2*xp)
	Er := float32(mid2*mid2) + side + float32(2*xp)
	if Er < 6e-4 || El < 6e-4 {
		copy(Y[:N], X[:N])
		return
	}

	lgain := celtRsqrt(El)
	rgain := celtRsqrt(Er)

	for j := 0; j < N; j++ {
		// Apply mid scaling (side is already scaled).
		l := float32(mid * X[j])
		r := Y[j]
		X[j] = lgain * (l - r)
		Y[j] = rgain * (l + r)
	}
}

// orderyTable converts from natural Hadamard to ordery Hadamard: a
// bit-reversed Gray code with the order inverted so that DC comes last. The
// lines are for N=2, 4, 8, 16 (ordery_table).
var orderyTable = [30]int{
	1, 0,
	3, 0, 2, 1,
	7, 0, 4, 3, 6, 1, 5, 2,
	15, 0, 8, 7, 12, 3, 11, 4, 14, 1, 9, 6, 13, 2, 10, 5,
}

// hadamardRows returns the row of X that holds block i after
// deinterleaveHadamard: ordery_table's order with hadamard, else i.
func hadamardRows(rows *[16]int, N0, stride int, hadamard bool) {
	for i := 0; i < stride; i++ {
		r := i
		if hadamard {
			r = orderyTable[stride-2+i]
		}
		rows[i] = r * N0
	}
}

// deinterleaveHadamard reorders X from interleaved short blocks to one block
// after another (deinterleave_hadamard). tmp is scratch of N0*stride values.
// Each step j moves the stride values of X[j*stride:] at once.
func deinterleaveHadamard(X []float32, N0, stride int, hadamard bool, tmp []float32) {
	N := N0 * stride
	tmp = tmp[:N]
	X = X[:N]
	var r [16]int
	hadamardRows(&r, N0, stride, hadamard)
	switch stride {
	case 2:
		for j := 0; j < N0; j++ {
			x := X[2*j : 2*j+2 : 2*j+2]
			tmp[r[0]+j] = x[0]
			tmp[r[1]+j] = x[1]
		}
	case 4:
		for j := 0; j < N0; j++ {
			x := X[4*j : 4*j+4 : 4*j+4]
			tmp[r[0]+j] = x[0]
			tmp[r[1]+j] = x[1]
			tmp[r[2]+j] = x[2]
			tmp[r[3]+j] = x[3]
		}
	case 8:
		for j := 0; j < N0; j++ {
			x := X[8*j : 8*j+8 : 8*j+8]
			tmp[r[0]+j] = x[0]
			tmp[r[1]+j] = x[1]
			tmp[r[2]+j] = x[2]
			tmp[r[3]+j] = x[3]
			tmp[r[4]+j] = x[4]
			tmp[r[5]+j] = x[5]
			tmp[r[6]+j] = x[6]
			tmp[r[7]+j] = x[7]
		}
	default:
		for j := 0; j < N0; j++ {
			x := X[j*stride : j*stride+stride]
			for i, v := range x {
				tmp[r[i]+j] = v
			}
		}
	}
	copy(X, tmp)
}

// interleaveHadamard undoes deinterleaveHadamard (interleave_hadamard).
func interleaveHadamard(X []float32, N0, stride int, hadamard bool, tmp []float32) {
	N := N0 * stride
	tmp = tmp[:N]
	X = X[:N]
	var r [16]int
	hadamardRows(&r, N0, stride, hadamard)
	switch stride {
	case 2:
		for j := 0; j < N0; j++ {
			t := tmp[2*j : 2*j+2 : 2*j+2]
			t[0] = X[r[0]+j]
			t[1] = X[r[1]+j]
		}
	case 4:
		for j := 0; j < N0; j++ {
			t := tmp[4*j : 4*j+4 : 4*j+4]
			t[0] = X[r[0]+j]
			t[1] = X[r[1]+j]
			t[2] = X[r[2]+j]
			t[3] = X[r[3]+j]
		}
	case 8:
		for j := 0; j < N0; j++ {
			t := tmp[8*j : 8*j+8 : 8*j+8]
			t[0] = X[r[0]+j]
			t[1] = X[r[1]+j]
			t[2] = X[r[2]+j]
			t[3] = X[r[3]+j]
			t[4] = X[r[4]+j]
			t[5] = X[r[5]+j]
			t[6] = X[r[6]+j]
			t[7] = X[r[7]+j]
		}
	default:
		for j := 0; j < N0; j++ {
			t := tmp[j*stride : j*stride+stride]
			for i := range t {
				t[i] = X[r[i]+j]
			}
		}
	}
	copy(X, tmp)
}

// haar1 replaces each pair of samples stride apart with their scaled sum and
// difference, a one-level Haar step (haar1).
func haar1(X []float32, N0, stride int) {
	if useAVX2 && stride <= 2 && N0*stride >= 16 {
		haar1AVX2(X[:N0*stride], stride)
		return
	}
	N0 >>= 1
	for i := 0; i < stride; i++ {
		for j := 0; j < N0; j++ {
			tmp1 := float32(0.70710678 * X[stride*2*j+i])
			tmp2 := float32(0.70710678 * X[stride*(2*j+1)+i])
			X[stride*2*j+i] = tmp1 + tmp2
			X[stride*(2*j+1)+i] = tmp1 - tmp2
		}
	}
}

// exp2Table8 holds 2^(i/8) in Q14 (exp2_table8 in compute_qn).
var exp2Table8 = [8]int{16384, 17866, 19483, 21247, 23170, 25267, 27554, 30048}

// computeQn returns the number of quantization steps of the split angle
// theta (compute_qn).
func computeQn(N, b, offset, pulseCap int, stereo bool) int {
	N2 := 2*N - 1
	if stereo && N == 2 {
		N2--
	}
	// The upper limit ensures that in a stereo split with itheta==16384,
	// we'll always have enough bits left over to code at least one pulse in
	// the side; otherwise it would collapse, since it doesn't get folded.
	qb := celtSudiv(b+N2*offset, N2)
	qb = min(b-pulseCap-(4<<entcode.BitRes), qb)

	qb = min(8<<entcode.BitRes, qb)

	if qb < (1 << entcode.BitRes >> 1) {
		return 1
	}
	qn := exp2Table8[qb&0x7] >> (14 - (qb >> entcode.BitRes))
	return (qn + 1) >> 1 << 1
}

// maxBandSize is the size of the widest band, band 20 with 20 ms frames.
const maxBandSize = 176

// bandCtx is the state shared by the band decoding functions (band_ctx,
// decoder side). The scratch buffers replace libopus's stack arrays.
type bandCtx struct {
	m             *Mode
	i             int
	intensity     int
	spread        int
	tfChange      int
	ec            *entcode.Decoder
	remainingBits int
	seed          uint32
	disableInv    bool

	// iy holds the pulses of alg_unquant.
	iy [maxBandSize]int32
	// tmp is the scratch of (de)interleave_hadamard.
	tmp [maxBandSize]float32
}

// splitCtx is the outcome of computeTheta (split_ctx).
type splitCtx struct {
	inv    bool
	imid   int
	iside  int
	delta  int
	itheta int
	qalloc int
}

// computeTheta decodes the split angle theta between the two halves of a
// band of N samples, or mid and side in stereo, and derives the gains and
// the bit split (compute_theta, decoder side; the encoder's X and Y
// arguments are left out).
func computeTheta(ctx *bandCtx, sctx *splitCtx, N int, b *int, B, B0, LM int, stereo bool, fill *int) {
	itheta := 0
	inv := false
	m := ctx.m
	i := ctx.i
	intensity := ctx.intensity
	ec := ctx.ec

	// Decide on the resolution to give to the split parameter theta.
	pulseCap := m.logN[i] + LM*(1<<entcode.BitRes)
	offset := pulseCap >> 1
	if stereo && N == 2 {
		offset -= qthetaOffsetTwoPhase
	} else {
		offset -= qthetaOffset
	}
	qn := computeQn(N, *b, offset, pulseCap, stereo)
	if stereo && i >= intensity {
		qn = 1
	}
	tell := ec.TellFrac()
	if qn != 1 {
		// Entropy coding of the angle. We use a uniform pdf for the time
		// split, a step for stereo, and a triangular one for the rest.
		switch {
		case stereo && N > 2:
			const p0 = 3
			x0 := qn / 2
			ft := p0*(x0+1) + x0
			// Use a probability of p0 up to itheta=8192 and then use 1
			// after.
			fs := int(ec.Decode(uint32(ft)))
			var x int
			if fs < (x0+1)*p0 {
				x = fs / p0
			} else {
				x = x0 + 1 + (fs - (x0+1)*p0)
			}
			var fl, fh int
			if x <= x0 {
				fl = p0 * x
				fh = p0 * (x + 1)
			} else {
				fl = (x - 1 - x0) + (x0+1)*p0
				fh = (x - x0) + (x0+1)*p0
			}
			ec.DecUpdate(uint32(fl), uint32(fh), uint32(ft))
			itheta = x
		case B0 > 1 || stereo:
			// Uniform pdf.
			itheta = int(ec.DecUint(uint32(qn + 1)))
		default:
			// Triangular pdf.
			ft := ((qn >> 1) + 1) * ((qn >> 1) + 1)
			fm := int(ec.Decode(uint32(ft)))
			var fs, fl int
			if fm < ((qn >> 1) * ((qn >> 1) + 1) >> 1) {
				itheta = int((isqrt32(8*uint32(fm)+1) - 1) >> 1)
				fs = itheta + 1
				fl = itheta * (itheta + 1) >> 1
			} else {
				itheta = int((uint32(2*(qn+1)) - isqrt32(8*uint32(ft-fm-1)+1)) >> 1)
				fs = qn + 1 - itheta
				fl = ft - ((qn + 1 - itheta) * (qn + 2 - itheta) >> 1)
			}
			ec.DecUpdate(uint32(fl), uint32(fl+fs), uint32(ft))
		}
		itheta = int(celtUdiv(uint32(itheta*16384), uint32(qn)))
	} else if stereo {
		if *b > 2<<entcode.BitRes && ctx.remainingBits > 2<<entcode.BitRes {
			inv = ec.DecBitLogp(2) != 0
		}
		// inv flag override to avoid problems with downmixing.
		if ctx.disableInv {
			inv = false
		}
		itheta = 0
	}
	qalloc := ec.TellFrac() - tell
	*b -= qalloc

	var imid, iside, delta int
	switch itheta {
	case 0:
		imid = 32767
		iside = 0
		*fill &= (1 << B) - 1
		delta = -16384
	case 16384:
		imid = 0
		iside = 32767
		*fill &= ((1 << B) - 1) << B
		delta = 16384
	default:
		// imid = bitexact_cos(itheta), iside = bitexact_cos(16384-itheta).
		gm := thetaGains[itheta]
		gs := thetaGains[16384-itheta]
		imid = int(gm.cos)
		iside = int(gs.cos)
		// This is the mid vs side allocation that minimizes squared error
		// in that band: bitexact_log2tan(iside, imid).
		delta = fracMul16((N-1)<<7, int(gs.term)-int(gm.term))
	}

	sctx.inv = inv
	sctx.imid = imid
	sctx.iside = iside
	sctx.delta = delta
	sctx.itheta = itheta
	sctx.qalloc = qalloc
}

// quantBandN1 decodes the sign of a one-sample band, in X and, for stereo,
// in Y (quant_band_n1, decoder side).
func quantBandN1(ctx *bandCtx, X, Y []float32, lowbandOut []float32) uint {
	x := X
	channels := 1
	if Y != nil {
		channels = 2
	}
	for c := 0; c < channels; c++ {
		sign := uint32(0)
		if ctx.remainingBits >= 1<<entcode.BitRes {
			sign = ctx.ec.DecBits(1)
			ctx.remainingBits -= 1 << entcode.BitRes
		}
		if sign != 0 {
			x[0] = -1
		} else {
			x[0] = 1
		}
		x = Y
	}
	if lowbandOut != nil {
		lowbandOut[0] = X[0]
	}
	return 1
}

// quantPartition decodes a mono partition. It can split the band in two
// and decode the energy difference between the two half-bands. It can be
// called recursively so bands can end up being split in 8 parts
// (quant_partition, decoder side).
func quantPartition(ctx *bandCtx, X []float32, N, b, B int, lowband []float32, LM int, gain float32, fill int) uint {
	B0 := B
	var cm uint
	m := ctx.m
	i := ctx.i
	spread := ctx.spread
	ec := ctx.ec

	// If we need 1.5 more bit than we can produce, split the band in two
	// (cache[cache[0]] is the band's maxBits).
	if LM != -1 && b > int(m.cache.maxBits[(LM+1)*m.nbEBands+i])+12 && N > 2 {
		var sctx splitCtx
		var nextLowband2 []float32

		N >>= 1
		Y := X[N:]
		LM--
		if B == 1 {
			fill = (fill & 1) | (fill << 1)
		}
		B = (B + 1) >> 1

		computeTheta(ctx, &sctx, N, &b, B, B0, LM, false, &fill)
		imid := sctx.imid
		iside := sctx.iside
		delta := sctx.delta
		itheta := sctx.itheta
		qalloc := sctx.qalloc
		mid := (1.0 / 32768) * float32(imid)
		side := (1.0 / 32768) * float32(iside)

		// Give more bits to low-energy MDCTs than they would otherwise
		// deserve.
		if B0 > 1 && itheta&0x3fff != 0 {
			if itheta > 8192 {
				// Rough approximation for pre-echo masking.
				delta -= delta >> (4 - LM)
			} else {
				// Corresponds to a forward-masking slope of 1.5 dB per 10 ms.
				delta = min(0, delta+(N<<entcode.BitRes>>(5-LM)))
			}
		}
		mbits := max(0, min(b, (b-delta)/2))
		sbits := b - mbits
		ctx.remainingBits -= qalloc

		if lowband != nil {
			nextLowband2 = lowband[N:] // >32-bit split case
		}

		rebalance := ctx.remainingBits
		if mbits >= sbits {
			cm = quantPartition(ctx, X, N, mbits, B, lowband, LM, gain*mid, fill)
			rebalance = mbits - (rebalance - ctx.remainingBits)
			if rebalance > 3<<entcode.BitRes && itheta != 0 {
				sbits += rebalance - (3 << entcode.BitRes)
			}
			cm |= quantPartition(ctx, Y, N, sbits, B, nextLowband2, LM, gain*side, fill>>B) << (B0 >> 1)
		} else {
			cm = quantPartition(ctx, Y, N, sbits, B, nextLowband2, LM, gain*side, fill>>B) << (B0 >> 1)
			rebalance = sbits - (rebalance - ctx.remainingBits)
			if rebalance > 3<<entcode.BitRes && itheta != 16384 {
				mbits += rebalance - (3 << entcode.BitRes)
			}
			cm |= quantPartition(ctx, X, N, mbits, B, lowband, LM, gain*mid, fill)
		}
		return cm
	}

	// This is the basic no-split case.
	q := bits2pulses(m, i, LM, b)
	currBits := pulses2bits(m, i, LM, q)
	ctx.remainingBits -= currBits

	// Ensures we can never bust the budget.
	for ctx.remainingBits < 0 && q > 0 {
		ctx.remainingBits += currBits
		q--
		currBits = pulses2bits(m, i, LM, q)
		ctx.remainingBits -= currBits
	}

	if q != 0 {
		K := getPulses(q)
		// Finally do the actual dequantization.
		return algUnquant(X, N, K, spread, B, ec, gain, ctx.iy[:])
	}

	// If there's no pulse, fill the band anyway.
	// B can be as large as 16, so this shift might overflow an int on a
	// 16-bit platform; use a long to get defined behavior.
	cmMask := uint(1)<<B - 1
	fill &= int(cmMask)
	if fill == 0 {
		clear(X[:N])
		return cm
	}
	if lowband == nil {
		// Noise.
		seed := ctx.seed
		for j := range X[:N] {
			seed = celtLcgRand(seed)
			X[j] = float32(int32(seed) >> 20)
		}
		ctx.seed = seed
		cm = cmMask
	} else {
		// Folded spectrum.
		seed := ctx.seed
		lowband = lowband[:N]
		x := X[:len(lowband)]
		for j, l := range lowband {
			seed = celtLcgRand(seed)
			// About 48 dB below the "normal" folding level: 1/256 with
			// the sign set when bit 15 of the seed is clear.
			tmp := math.Float32frombits(0x3b800000 | (^seed&0x8000)<<16)
			x[j] = l + tmp
		}
		ctx.seed = seed
		cm = uint(fill)
	}
	renormaliseVector(X, N, gain)
	return cm
}

// bitInterleaveTable and bitDeinterleaveTable convert collapse masks
// between time-frequency resolutions (in quant_band).
var (
	bitInterleaveTable   = [16]uint8{0, 1, 1, 1, 2, 3, 3, 3, 2, 3, 3, 3, 2, 3, 3, 3}
	bitDeinterleaveTable = [16]uint8{
		0x00, 0x03, 0x0C, 0x0F, 0x30, 0x33, 0x3C, 0x3F,
		0xC0, 0xC3, 0xCC, 0xCF, 0xF0, 0xF3, 0xFC, 0xFF,
	}
)

// quantBand decodes a band for the mono case, applying the TF resolution
// change (quant_band, decoder side).
func quantBand(ctx *bandCtx, X []float32, N, b, B int, lowband []float32, LM int, lowbandOut []float32,
	gain float32, lowbandScratch []float32, fill int,
) uint {
	N0 := N
	NB := N
	B0 := B
	timeDivide := 0
	recombine := 0
	tfChange := ctx.tfChange

	longBlocks := B0 == 1

	NB = int(celtUdiv(uint32(NB), uint32(B)))

	// Special case for one sample.
	if N == 1 {
		return quantBandN1(ctx, X, nil, lowbandOut)
	}

	if tfChange > 0 {
		recombine = tfChange
	}
	// Band recombining to increase frequency resolution.

	if lowbandScratch != nil && lowband != nil && (recombine != 0 || (NB&1 == 0 && tfChange < 0) || B0 > 1) {
		copy(lowbandScratch[:N], lowband[:N])
		lowband = lowbandScratch
	}

	for k := 0; k < recombine; k++ {
		if lowband != nil {
			haar1(lowband, N>>k, 1<<k)
		}
		fill = int(bitInterleaveTable[fill&0xF]) | int(bitInterleaveTable[fill>>4])<<2
	}
	B >>= recombine
	NB <<= recombine

	// Increasing the time resolution.
	for NB&1 == 0 && tfChange < 0 {
		if lowband != nil {
			haar1(lowband, NB, B)
		}
		fill |= fill << B
		B <<= 1
		NB >>= 1
		timeDivide++
		tfChange++
	}
	B0 = B
	NB0 := NB

	// Reorganize the samples in time order instead of frequency order.
	if B0 > 1 && lowband != nil {
		deinterleaveHadamard(lowband, NB>>recombine, B0<<recombine, longBlocks, ctx.tmp[:])
	}

	cm := quantPartition(ctx, X, N, b, B, lowband, LM, gain, fill)

	// Undo the sample reorganization going from time order to frequency
	// order.
	if B0 > 1 {
		interleaveHadamard(X, NB>>recombine, B0<<recombine, longBlocks, ctx.tmp[:])
	}

	// Undo time-freq changes that we did earlier.
	NB = NB0
	B = B0
	for k := 0; k < timeDivide; k++ {
		B >>= 1
		NB <<= 1
		cm |= cm >> B
		haar1(X, NB, B)
	}

	for k := 0; k < recombine; k++ {
		cm = uint(bitDeinterleaveTable[cm])
		haar1(X, N0>>k, 1<<k)
	}
	B <<= recombine

	// Scale output for later folding.
	if lowbandOut != nil {
		n := celtSqrt(float32(N0))
		if useAVX2 && N0 >= 16 {
			scaleAVX2(lowbandOut[:N0], X[:N0], n)
		} else {
			for j := 0; j < N0; j++ {
				lowbandOut[j] = n * X[j]
			}
		}
	}
	cm &= (1 << B) - 1
	return cm
}

// quantBandStereo decodes a band for the stereo case (quant_band_stereo,
// decoder side).
func quantBandStereo(ctx *bandCtx, X, Y []float32, N, b, B int, lowband []float32, LM int, lowbandOut,
	lowbandScratch []float32, fill int,
) uint {
	var cm uint
	var sctx splitCtx
	ec := ctx.ec

	// Special case for one sample.
	if N == 1 {
		return quantBandN1(ctx, X, Y, lowbandOut)
	}

	origFill := fill

	computeTheta(ctx, &sctx, N, &b, B, B, LM, true, &fill)
	inv := sctx.inv
	imid := sctx.imid
	iside := sctx.iside
	delta := sctx.delta
	itheta := sctx.itheta
	qalloc := sctx.qalloc
	mid := (1.0 / 32768) * float32(imid)
	side := (1.0 / 32768) * float32(iside)

	// This is a special case for N=2 that only works for stereo and takes
	// advantage of the fact that mid and side are orthogonal to encode the
	// side with just one bit.
	if N == 2 {
		sign := 0
		mbits := b
		sbits := 0
		// Only need one bit for the side.
		if itheta != 0 && itheta != 16384 {
			sbits = 1 << entcode.BitRes
		}
		mbits -= sbits
		c := itheta > 8192
		ctx.remainingBits -= qalloc + sbits

		x2, y2 := X, Y
		if c {
			x2, y2 = Y, X
		}
		if sbits != 0 {
			sign = int(ec.DecBits(1))
		}
		sign = 1 - 2*sign
		// We use origFill here because we want to fold the side, but if
		// itheta==16384, we'll have cleared the low bits of fill.
		cm = quantBand(ctx, x2, N, mbits, B, lowband, LM, lowbandOut, q15One, lowbandScratch, origFill)
		// We don't split N=2 bands, so cm is either 1 or 0 (for a
		// fold-collapse), and there's no need to worry about mixing with
		// the other channel.
		y2[0] = float32(-sign) * x2[1]
		y2[1] = float32(sign) * x2[0]
		X[0] = float32(mid * X[0])
		X[1] = float32(mid * X[1])
		Y[0] = float32(side * Y[0])
		Y[1] = float32(side * Y[1])
		tmp := X[0]
		X[0] = tmp - Y[0]
		Y[0] = tmp + Y[0]
		tmp = X[1]
		X[1] = tmp - Y[1]
		Y[1] = tmp + Y[1]
	} else {
		// "Normal" split code.
		mbits := max(0, min(b, (b-delta)/2))
		sbits := b - mbits
		ctx.remainingBits -= qalloc

		rebalance := ctx.remainingBits
		if mbits >= sbits {
			// In stereo mode, we do not apply a scaling to the mid because
			// we need the normalized mid for folding later.
			cm = quantBand(ctx, X, N, mbits, B, lowband, LM, lowbandOut, q15One, lowbandScratch, fill)
			rebalance = mbits - (rebalance - ctx.remainingBits)
			if rebalance > 3<<entcode.BitRes && itheta != 0 {
				sbits += rebalance - (3 << entcode.BitRes)
			}
			// For a stereo split, the high bits of fill are always zero,
			// so no folding will be done to the side.
			cm |= quantBand(ctx, Y, N, sbits, B, nil, LM, nil, side, nil, fill>>B)
		} else {
			// For a stereo split, the high bits of fill are always zero,
			// so no folding will be done to the side.
			cm = quantBand(ctx, Y, N, sbits, B, nil, LM, nil, side, nil, fill>>B)
			rebalance = sbits - (rebalance - ctx.remainingBits)
			if rebalance > 3<<entcode.BitRes && itheta != 16384 {
				mbits += rebalance - (3 << entcode.BitRes)
			}
			// In stereo mode, we do not apply a scaling to the mid because
			// we need the normalized mid for folding later.
			cm |= quantBand(ctx, X, N, mbits, B, lowband, LM, lowbandOut, q15One, lowbandScratch, fill)
		}
	}

	if N != 2 {
		stereoMerge(X, Y, mid, N)
	}
	if inv {
		for j := 0; j < N; j++ {
			Y[j] = -Y[j]
		}
	}
	return cm
}

// specialHybridFolding duplicates enough of the first band's folding data
// to be able to fold the second band. It copies no data for CELT-only mode
// (special_hybrid_folding).
func specialHybridFolding(m *Mode, norm, norm2 []float32, start, M int, dualStereo bool) {
	eBands := m.eBands
	n1 := M * (eBands[start+1] - eBands[start])
	n2 := M * (eBands[start+2] - eBands[start+1])
	copy(norm[n1:n2], norm[2*n1-n2:n1])
	if dualStereo {
		copy(norm2[n1:n2], norm2[2*n1-n2:n1])
	}
}

// quantAllBands decodes the shape of all the bands from start to end: X_
// for mono or mid, and Y_ for side when not nil (quant_all_bands, decoder
// side). norm is scratch of 2*M*eBands[nbEBands-1] values.
func quantAllBands(ctx *bandCtx, m *Mode, start, end int, X_, Y_ []float32, collapseMasks []uint8,
	pulses []int, shortBlocks bool, spread int, dualStereo bool, intensity int, tfRes []int,
	totalBits, balance int, ec *entcode.Decoder, LM, codedBands int, seed *uint32, disableInv bool,
	norm []float32,
) {
	eBands := m.eBands
	updateLowband := true
	C := 1
	if Y_ != nil {
		C = 2
	}

	M := 1 << LM
	B := 1
	if shortBlocks {
		B = M
	}
	normOffset := M * eBands[start]
	// No need to allocate norm for the last band because we don't need an
	// output in that band.
	norm = norm[:C*(M*eBands[m.nbEBands-1]-normOffset)]
	norm2 := norm[M*eBands[m.nbEBands-1]-normOffset:]

	// For decoding, we can use the last band as scratch space because we
	// don't need that scratch space for the last band and we don't care
	// about the data there until we're decoding the last band.
	lowbandScratch := X_[M*eBands[m.effEBands-1]:]

	lowbandOffset := 0
	ctx.ec = ec
	ctx.intensity = intensity
	ctx.m = m
	ctx.seed = *seed
	ctx.spread = spread
	ctx.disableInv = disableInv
	for i := start; i < end; i++ {
		var X, Y []float32
		effectiveLowband := -1
		var xCm, yCm uint

		ctx.i = i
		last := i == end-1

		X = X_[M*eBands[i]:]
		if Y_ != nil {
			Y = Y_[M*eBands[i]:]
		}
		N := M*eBands[i+1] - M*eBands[i]
		tell := ec.TellFrac()

		// Compute how many bits we want to allocate to this band.
		if i != start {
			balance -= tell
		}
		remainingBits := totalBits - tell - 1
		ctx.remainingBits = remainingBits
		b := 0
		if i <= codedBands-1 {
			currBalance := celtSudiv(balance, min(3, codedBands-i))
			b = max(0, min(16383, min(remainingBits+1, pulses[i]+currBalance)))
		}

		if (M*eBands[i]-N >= M*eBands[start] || i == start+1) && (updateLowband || lowbandOffset == 0) {
			lowbandOffset = i
		}
		if i == start+1 {
			specialHybridFolding(m, norm, norm2, start, M, dualStereo)
		}

		tfChange := tfRes[i]
		ctx.tfChange = tfChange
		if i >= m.effEBands {
			X = norm
			if Y_ != nil {
				Y = norm
			}
			lowbandScratch = nil
		}
		if last {
			lowbandScratch = nil
		}

		// Get a conservative estimate of the collapse_mask's for the bands
		// we're going to be folding from.
		if lowbandOffset != 0 && (spread != spreadAggressive || B > 1 || tfChange < 0) {
			// This ensures we never repeat spectral content within one band.
			effectiveLowband = max(0, M*eBands[lowbandOffset]-normOffset-N)
			foldStart := lowbandOffset
			for {
				foldStart--
				if M*eBands[foldStart] <= effectiveLowband+normOffset {
					break
				}
			}
			foldEnd := lowbandOffset - 1
			for {
				foldEnd++
				if foldEnd >= i || M*eBands[foldEnd] >= effectiveLowband+normOffset+N {
					break
				}
			}
			xCm, yCm = 0, 0
			for foldI := foldStart; ; {
				xCm |= uint(collapseMasks[foldI*C+0])
				yCm |= uint(collapseMasks[foldI*C+C-1])
				foldI++
				if foldI >= foldEnd {
					break
				}
			}
		} else {
			// Otherwise, we'll be using the LCG to fold, so all blocks will
			// (almost always) be non-zero.
			xCm = uint(1)<<B - 1
			yCm = xCm
		}

		if dualStereo && i == intensity {
			// Switch off dual stereo to do intensity.
			dualStereo = false
			for j := 0; j < M*eBands[i]-normOffset; j++ {
				norm[j] = 0.5 * (norm[j] + norm2[j])
			}
		}
		var lowband, lowbandOut []float32
		if effectiveLowband != -1 {
			lowband = norm[effectiveLowband:]
		}
		if !last {
			lowbandOut = norm[M*eBands[i]-normOffset:]
		}
		if dualStereo {
			var lowband2, lowbandOut2 []float32
			if effectiveLowband != -1 {
				lowband2 = norm2[effectiveLowband:]
			}
			if !last {
				lowbandOut2 = norm2[M*eBands[i]-normOffset:]
			}
			xCm = quantBand(ctx, X, N, b/2, B, lowband, LM, lowbandOut, q15One, lowbandScratch, int(xCm))
			yCm = quantBand(ctx, Y, N, b/2, B, lowband2, LM, lowbandOut2, q15One, lowbandScratch, int(yCm))
		} else {
			if Y != nil {
				xCm = quantBandStereo(ctx, X, Y, N, b, B, lowband, LM, lowbandOut, lowbandScratch, int(xCm|yCm))
			} else {
				xCm = quantBand(ctx, X, N, b, B, lowband, LM, lowbandOut, q15One, lowbandScratch, int(xCm|yCm))
			}
			yCm = xCm
		}
		collapseMasks[i*C+0] = uint8(xCm)
		collapseMasks[i*C+C-1] = uint8(yCm)
		balance += pulses[i] + tell

		// Update the folding position only as long as we have 1 bit/sample
		// depth.
		updateLowband = b > N<<entcode.BitRes
	}
	*seed = ctx.seed
}
