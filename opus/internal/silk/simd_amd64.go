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
// Ported from libopus 1.5.2 silk/resampler_private_IIR_FIR.c, silk/resampler_private_up2_HQ.c,
// silk/stereo_MS_to_LR.c and silk/dec_API.c (vector forms of their loops).

//go:build goexperiment.simd && amd64

package silk

import "simd/archsimd"

// Vector kernels for amd64 with GOEXPERIMENT=simd. They use 128-bit
// integer instructions that AVX provides (VEX-encoded, so they leave the
// upper halves of the vector registers clean and need only what the runtime
// saves on preemption with AVX alone), and compute exactly what the scalar
// code does: kernels_test.go and simd_amd64_test.go compare them. Without
// AVX the scalar code runs.
//
// Go encodes vector spills and its own copies and zeroing with legacy SSE
// instructions, which cost a microcode assist each on Intel cores while the
// upper halves of the vector registers are dirty, and in this build an
// asynchronous preemption returns with them dirty. Every kernel therefore
// starts by clearing them, and so does Decoder.Decode (clearUpperState) for
// the scalar code of a frame.

// useAVX selects the vector kernels.
var useAVX = archsimd.X86.AVX()

// clearUpperState clears the upper halves of the vector registers
// (VZEROUPPER) where the CPU has them.
func clearUpperState() {
	if useAVX {
		archsimd.ClearAVXUpperBits()
	}
}

// accel names the kernels this CPU runs.
func accel() string {
	if useAVX {
		return "simd-amd64-avx"
	}
	return "scalar"
}

func resamplerPrivateUp2HQ(s *[resamplerMaxIIROrder]int32, out []int16, in []int16, length int) {
	if useAVX {
		up2HQAVX(s, out, in, length)
		return
	}
	resamplerPrivateUp2HQScalar(s, out, in, length)
}

func msToLR(mid, side []int16) {
	if useAVX {
		msToLRAVX(mid, side)
		return
	}
	msToLRScalar(mid, side)
}

func interleave(dst, left, right []int16) {
	if useAVX {
		interleaveAVX(dst, left, right)
		return
	}
	interleaveScalar(dst, left, right)
}

func interpolFIR3Over2(out []int16, buf []int16, n int) int {
	if useAVX {
		return interpolFIR3Over2AVX(out, buf, n)
	}
	return interpolFIR3Over2Scalar(out, buf, n)
}

func interpolFIR2(out []int16, buf []int16, n int) int {
	if useAVX {
		return interpolFIR2AVX(out, buf, n)
	}
	return interpolFIR2Scalar(out, buf, n)
}

func interpolFIR3(out []int16, buf []int16, n int) int {
	if useAVX {
		return interpolFIR3AVX(out, buf, n)
	}
	return interpolFIR3Scalar(out, buf, n)
}

// The FIR interpolation kernels compute each output's eight-tap sum with
// VPMADDWD, which multiplies pairs of int16 and adds each pair's products
// into an int32 lane: a lane holding the samples (w[i+2u], w[i+2u+1]) and
// the taps (h[2u], h[2u+1]) gives two of the eight products of the output
// whose window starts at w[i], and four such vectors, u = 0..3, give the
// whole sum. Four outputs with windows two samples apart are one load per
// u. The sums are exact (no tap is -32768) and wrap in int32 like the
// scalar ones. The rounding shift is the scalar one, the saturation to
// int16 is VPACKSSDW, and byte shuffles put the outputs in order.

// firPairs holds, for a phase p, the taps of firTaps(p) as pairs repeated
// over the four lanes: firPairs[p][u] = {h[2u], h[2u+1], h[2u], ...}.
var firPairs = func() (t [12][4][8]int16) {
	for p := range t {
		h := firTaps(p)
		for u := range 4 {
			for k := range 4 {
				t[p][u][2*k], t[p][u][2*k+1] = h[2*u], h[2*u+1]
			}
		}
	}
	return t
}()

// firTaps returns the eight taps of phase p in window order:
// silk_resampler_frac_FIR_12[p][0..3] and silk_resampler_frac_FIR_12[11-p]
// reversed.
func firTaps(p int) [resamplerOrderFIR12]int16 {
	var h [resamplerOrderFIR12]int16
	for t := range 4 {
		h[t] = resamplerFracFIR12[p][t]
		h[4+t] = resamplerFracFIR12[11-p][3-t]
	}
	return h
}

// firShuffle holds the VPSHUFB masks that gather 24 outputs (three
// vectors of eight) from the three packed vectors of eight results: mask
// [v][s] takes the lanes of output vector v that come from source s and
// zeroes the others.
type firShuffle [3][3][16]int8

// newFIRShuffle builds the masks for outputs whose q-th lane comes from lane
// l of source s, as src(q) returns.
func newFIRShuffle(outputs int, src func(q int) (s, l int)) (m firShuffle) {
	for v := range m {
		for s := range m[v] {
			for b := range m[v][s] {
				m[v][s][b] = -128
			}
		}
	}
	for q := range outputs {
		s, l := src(q)
		v, lane := q/8, q%8
		m[v][s][2*lane], m[v][s][2*lane+1] = int8(2*l), int8(2*l+1)
	}
	return m
}

var (
	// 16 to 48 kHz, eight m: outputs 3m, 3m+1, 3m+2 are the results of
	// phases 0, 8 and 4, packed by m.
	firShuffle3Over2 = newFIRShuffle(24, func(q int) (int, int) { return q % 3, q / 3 })
	// 12 to 48 kHz, eight j: outputs 2j and 2j+1 are phases 0 and 6; the
	// sources hold even j then odd j, packed as phase 0 then phase 6 lanes.
	firShuffle2 = newFIRShuffle(16, func(q int) (int, int) {
		j, r := q/2, q%2
		return j % 2, 4*r + j/2
	})
	// 8 to 48 kHz, eight m: outputs 3m, 3m+1, 3m+2 are phases 0, 4 and 8,
	// each packed as even m then odd m.
	firShuffle3 = newFIRShuffle(24, func(q int) (int, int) {
		m, r := q/3, q%3
		return r, 4*(m%2) + m/2
	})
	// firRound is the 1 of the rounding shift.
	firRound = [4]int32{1, 1, 1, 1}
)

// firDot returns, for the four windows starting at w[off], w[off+2],
// w[off+4] and w[off+6], the eight-tap sums with the taps t. Callers pass
// a fixed-size window and constant offsets, so the loads need no checks.
func firDot(w []int16, off int, t *[4][8]int16) archsimd.Int32x4 {
	a := archsimd.LoadInt16x8Array((*[8]int16)(w[off:])).DotProductPairs(archsimd.LoadInt16x8Array(&t[0]))
	b := archsimd.LoadInt16x8Array((*[8]int16)(w[off+2:])).DotProductPairs(archsimd.LoadInt16x8Array(&t[1]))
	c := archsimd.LoadInt16x8Array((*[8]int16)(w[off+4:])).DotProductPairs(archsimd.LoadInt16x8Array(&t[2]))
	d := archsimd.LoadInt16x8Array((*[8]int16)(w[off+6:])).DotProductPairs(archsimd.LoadInt16x8Array(&t[3]))
	return a.Add(b).Add(c.Add(d))
}

// firPack rounds the sums of two groups of four outputs like
// silk_RSHIFT_ROUND(x, 15) and saturates them to int16, lo first; it
// returns their bytes for the shuffles.
func firPack(lo, hi, one archsimd.Int32x4) archsimd.Uint8x16 {
	lo = lo.ShiftAllRight(14).Add(one).ShiftAllRight(1)
	hi = hi.ShiftAllRight(14).Add(one).ShiftAllRight(1)
	return lo.SaturateToInt16Concat(hi).ToBits().ReshapeToUint8s()
}

// firStore gathers output vector v from the three packed sources with the
// masks m and stores it to o.
func firStore(o *[8]int16, m *firShuffle, v int, s0, s1, s2 archsimd.Uint8x16) {
	x := s0.PermuteOrZero(archsimd.LoadInt8x16Array(&m[v][0])).
		Or(s1.PermuteOrZero(archsimd.LoadInt8x16Array(&m[v][1]))).
		Or(s2.PermuteOrZero(archsimd.LoadInt8x16Array(&m[v][2])))
	x.ReshapeToUint16s().BitsToInt16().StoreArray(o)
}

// firStore2 is firStore with two sources.
func firStore2(o *[8]int16, m *firShuffle, v int, s0, s1 archsimd.Uint8x16) {
	x := s0.PermuteOrZero(archsimd.LoadInt8x16Array(&m[v][0])).
		Or(s1.PermuteOrZero(archsimd.LoadInt8x16Array(&m[v][1])))
	x.ReshapeToUint16s().BitsToInt16().StoreArray(o)
}

// interpolFIR3Over2AVX is interpolFIR3Over2Scalar with eight m (24 outputs)
// per step; the remaining m take the scalar core.
func interpolFIR3Over2AVX(out []int16, buf []int16, n int) int {
	archsimd.ClearAVXUpperBits()
	out = out[:3*n]
	buf = buf[:2*n+resamplerOrderFIR12]
	one := archsimd.LoadInt32x4Array(&firRound)
	m := 0
	for ; m+8 <= n; m += 8 {
		w := (*[24]int16)(buf[2*m : 2*m+24])[:]
		o := (*[24]int16)(out[3*m : 3*m+24])
		// Lane k holds m+k for lo, m+4+k for hi: phase 0 and 8 windows start
		// at w[2k], phase 4 windows at w[2k+1].
		a := firPack(firDot(w, 0, &firPairs[0]), firDot(w, 8, &firPairs[0]), one)
		b := firPack(firDot(w, 0, &firPairs[8]), firDot(w, 8, &firPairs[8]), one)
		c := firPack(firDot(w, 1, &firPairs[4]), firDot(w, 9, &firPairs[4]), one)
		firStore((*[8]int16)(o[0:8]), &firShuffle3Over2, 0, a, b, c)
		firStore((*[8]int16)(o[8:16]), &firShuffle3Over2, 1, a, b, c)
		firStore((*[8]int16)(o[16:24]), &firShuffle3Over2, 2, a, b, c)
	}
	if m < n {
		interpolFIR3Over2Scalar(out[3*m:], buf[2*m:], n-m)
	}
	return 3 * n
}

// interpolFIR2AVX is interpolFIR2Scalar with eight j (16 outputs) per step.
func interpolFIR2AVX(out []int16, buf []int16, n int) int {
	archsimd.ClearAVXUpperBits()
	out = out[:4*n]
	buf = buf[:2*n+resamplerOrderFIR12]
	one := archsimd.LoadInt32x4Array(&firRound)
	j := 0
	for ; j+8 <= 2*n; j += 8 {
		w := (*[16]int16)(buf[j : j+16])[:]
		o := (*[16]int16)(out[2*j : 2*j+16])
		// Lane k holds j+2k (even) or j+2k+1 (odd).
		even := firPack(firDot(w, 0, &firPairs[0]), firDot(w, 0, &firPairs[6]), one)
		odd := firPack(firDot(w, 1, &firPairs[0]), firDot(w, 1, &firPairs[6]), one)
		firStore2((*[8]int16)(o[0:8]), &firShuffle2, 0, even, odd)
		firStore2((*[8]int16)(o[8:16]), &firShuffle2, 1, even, odd)
	}
	if j < 2*n {
		// The scalar core takes whole input samples; j is a multiple of 8.
		interpolFIR2Scalar(out[2*j:], buf[j:], n-j/2)
	}
	return 4 * n
}

// interpolFIR3AVX is interpolFIR3Scalar with eight m (24 outputs) per step.
func interpolFIR3AVX(out []int16, buf []int16, n int) int {
	archsimd.ClearAVXUpperBits()
	out = out[:6*n]
	buf = buf[:2*n+resamplerOrderFIR12]
	one := archsimd.LoadInt32x4Array(&firRound)
	m := 0
	for ; m+8 <= 2*n; m += 8 {
		w := (*[16]int16)(buf[m : m+16])[:]
		o := (*[24]int16)(out[3*m : 3*m+24])
		// Lane k holds m+2k (even) or m+2k+1 (odd).
		a := firPack(firDot(w, 0, &firPairs[0]), firDot(w, 1, &firPairs[0]), one)
		b := firPack(firDot(w, 0, &firPairs[4]), firDot(w, 1, &firPairs[4]), one)
		c := firPack(firDot(w, 0, &firPairs[8]), firDot(w, 1, &firPairs[8]), one)
		firStore((*[8]int16)(o[0:8]), &firShuffle3, 0, a, b, c)
		firStore((*[8]int16)(o[8:16]), &firShuffle3, 1, a, b, c)
		firStore((*[8]int16)(o[16:24]), &firShuffle3, 2, a, b, c)
	}
	if m < 2*n {
		interpolFIR3Scalar(out[3*m:], buf[m:], n-m/2)
	}
	return 6 * n
}

// up2HQAVX is silk_resampler_private_up2_HQ with the even and the odd
// output's chains of all-pass sections side by side: lanes 0 and 2 of each
// vector hold a section's state, input or output of the even and the odd
// chain (lanes 1 and 3 are not used). VPMULDQ multiplies lanes 0 and 2 into
// 64-bit products, whose bits 16 to 47 are silk_SMULWB's result whichever
// way the product is shifted, so VPSRLQ by 16 gives it in lanes 0 and 2.
// Eight input samples take one load; the rest go through the scalar code.
func up2HQAVX(s *[resamplerMaxIIROrder]int32, out []int16, in []int16, length int) {
	archsimd.ClearAVXUpperBits()
	out = out[:2*length]
	in = in[:length]
	st := [3][4]int32{{s[0], 0, s[3], 0}, {s[1], 0, s[4], 0}, {s[2], 0, s[5], 0}}
	s0, s1, s2 := archsimd.LoadInt32x4Array(&st[0]), archsimd.LoadInt32x4Array(&st[1]), archsimd.LoadInt32x4Array(&st[2])
	c0 := archsimd.LoadInt32x4Array(&up2HQCoefs[0])
	c1 := archsimd.LoadInt32x4Array(&up2HQCoefs[1])
	c2 := archsimd.LoadInt32x4Array(&up2HQCoefs[2])
	one := archsimd.LoadInt32x4Array(&firRound)
	k := 0
	for ; k+8 <= length; k += 8 {
		x := archsimd.LoadInt16x8Array((*[8]int16)(in[k : k+8]))
		lo := x.ExtendLo4ToInt32().ShiftAllLeft(10)
		hi := x.ToBits().ReshapeToUint32s().BitsToInt32().PermuteScalars(2, 3, 2, 3).
			ToBits().ReshapeToUint16s().BitsToInt16().ExtendLo4ToInt32().ShiftAllLeft(10)
		var r0, r1, r2, r3 archsimd.Int32x4
		r0, s0, s1, s2 = up2HQStep(lo.PermuteScalars(0, 0, 0, 0), s0, s1, s2, c0, c1, c2)
		r1, s0, s1, s2 = up2HQStep(lo.PermuteScalars(1, 1, 1, 1), s0, s1, s2, c0, c1, c2)
		r2, s0, s1, s2 = up2HQStep(lo.PermuteScalars(2, 2, 2, 2), s0, s1, s2, c0, c1, c2)
		r3, s0, s1, s2 = up2HQStep(lo.PermuteScalars(3, 3, 3, 3), s0, s1, s2, c0, c1, c2)
		up2HQStore((*[8]int16)(out[2*k:2*k+8]), r0, r1, r2, r3, one)
		r0, s0, s1, s2 = up2HQStep(hi.PermuteScalars(0, 0, 0, 0), s0, s1, s2, c0, c1, c2)
		r1, s0, s1, s2 = up2HQStep(hi.PermuteScalars(1, 1, 1, 1), s0, s1, s2, c0, c1, c2)
		r2, s0, s1, s2 = up2HQStep(hi.PermuteScalars(2, 2, 2, 2), s0, s1, s2, c0, c1, c2)
		r3, s0, s1, s2 = up2HQStep(hi.PermuteScalars(3, 3, 3, 3), s0, s1, s2, c0, c1, c2)
		up2HQStore((*[8]int16)(out[2*k+8:2*k+16]), r0, r1, r2, r3, one)
	}
	*s = [resamplerMaxIIROrder]int32{
		s0.GetElem(0), s1.GetElem(0), s2.GetElem(0),
		s0.GetElem(2), s1.GetElem(2), s2.GetElem(2),
	}
	if k < length {
		resamplerPrivateUp2HQScalar(s, out[2*k:], in[k:], length-k)
	}
}

// up2HQCoefs holds silk_resampler_up2_hq_0 and _1 in lanes 0 and 2, one
// vector per section.
var up2HQCoefs = [3][4]int32{
	{resamplerUp2HQ00, 0, resamplerUp2HQ10, 0},
	{resamplerUp2HQ01, 0, resamplerUp2HQ11, 0},
	{resamplerUp2HQ02, 0, resamplerUp2HQ12, 0},
}

// up2HQMul is silk_SMULWB(y, c) in lanes 0 and 2.
func up2HQMul(y, c archsimd.Int32x4) archsimd.Int32x4 {
	return y.MulWidenEven(c).ToBits().ShiftAllRight(16).ReshapeToUint32s().BitsToInt32()
}

// up2HQStep runs the three all-pass sections of both chains on the input
// in32 (in lanes 0 and 2) and returns the output and the new states.
func up2HQStep(in32, s0, s1, s2, c0, c1, c2 archsimd.Int32x4) (out, n0, n1, n2 archsimd.Int32x4) {
	// First all-pass section.
	x := up2HQMul(in32.Sub(s0), c0)
	out32a := s0.Add(x)
	n0 = in32.Add(x)
	// Second all-pass section.
	x = up2HQMul(out32a.Sub(s1), c1)
	out32b := s1.Add(x)
	n1 = out32a.Add(x)
	// Third all-pass section: silk_SMLAWB(Y, Y, c).
	y := out32b.Sub(s2)
	x = y.Add(up2HQMul(y, c2))
	out = s2.Add(x)
	n2 = out32b.Add(x)
	return out, n0, n1, n2
}

// up2HQStore rounds the outputs of four input samples like
// silk_RSHIFT_ROUND(x, 10), saturates them to int16 and stores them as even,
// odd, even, ... samples to o.
func up2HQStore(o *[8]int16, r0, r1, r2, r3, one archsimd.Int32x4) {
	a := r0.ConcatPermuteScalars(0, 2, 4, 6, r1)
	b := r2.ConcatPermuteScalars(0, 2, 4, 6, r3)
	a = a.ShiftAllRight(9).Add(one).ShiftAllRight(1)
	b = b.ShiftAllRight(9).Add(one).ShiftAllRight(1)
	a.SaturateToInt16Concat(b).StoreArray(o)
}

// interleaveAVX is interleaveScalar eight samples of each channel per step,
// with VPUNPCKLWD and VPUNPCKHWD.
func interleaveAVX(dst, left, right []int16) {
	archsimd.ClearAVXUpperBits()
	right = right[:len(left)]
	dst = dst[:2*len(left)]
	i := 0
	for ; i+8 <= len(left); i += 8 {
		l := archsimd.LoadInt16x8Array((*[8]int16)(left[i : i+8]))
		r := archsimd.LoadInt16x8Array((*[8]int16)(right[i : i+8]))
		l.InterleaveLo(r).StoreArray((*[8]int16)(dst[2*i : 2*i+8]))
		l.InterleaveHi(r).StoreArray((*[8]int16)(dst[2*i+8 : 2*i+16]))
	}
	interleaveScalar(dst[2*i:], left[i:], right[i:])
}

// msToLRAVX is msToLRScalar eight samples per step: silk_SAT16 of the sum
// and difference of two int16 is VPADDSW and VPSUBSW.
func msToLRAVX(mid, side []int16) {
	archsimd.ClearAVXUpperBits()
	side = side[:len(mid)]
	n := 0
	for ; n+8 <= len(mid); n += 8 {
		pm, ps := (*[8]int16)(mid[n:n+8]), (*[8]int16)(side[n:n+8])
		m, s := archsimd.LoadInt16x8Array(pm), archsimd.LoadInt16x8Array(ps)
		m.AddSaturated(s).StoreArray(pm)
		m.SubSaturated(s).StoreArray(ps)
	}
	msToLRScalar(mid[n:], side[n:])
}
