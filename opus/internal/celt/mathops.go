// Copyright (c) 2002-2008 Jean-Marc Valin
// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2009 Xiph.Org Foundation
// Copyright (c) 2001-2011 Timothy B. Terriberry
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
// Ported from libopus 1.5.2 celt/mathops.h, celt/mathops.c and celt/arch.h (the
// float build: FIXED_POINT and FLOAT_APPROX undefined), with celt_udiv and
// celt_sudiv from celt/entcode.h.

package celt

import (
	"math"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// Constants of the float build (arch.h with FIXED_POINT undefined).
const (
	// q15One is 1 in the Q15 domain, 1.0 in the float build (Q15ONE).
	q15One float32 = 1.0
	// epsilon is a small value against division by zero (EPSILON).
	epsilon float32 = 1e-15
	// verySmall is added to the de-emphasis input to avoid denormals
	// (VERY_SMALL).
	verySmall float32 = 1e-30
	// celtSigScale scales the float signal to 16-bit PCM (CELT_SIG_SCALE).
	celtSigScale float32 = 32768
)

// pi is PI of mathops.h, a float constant.
const pi float32 = 3.141592653

// halfPi is the float product .5f*PI that celt_cos_norm multiplies by.
var halfPi = float32(0.5) * pi

// fracMul16 multiplies two 16-bit fractional values; its bit-exactness
// matters (FRAC_MUL16 in mathops.h).
func fracMul16(a, b int) int {
	return (16384 + int(int32(int16(a))*int32(int16(b)))) >> 15
}

// isqrt32 computes floor(sqrt(val)) with exact arithmetic; val must be
// greater than 0 (isqrt32 in mathops.c). It truncates the double-precision
// square root, which is exact here: for val < 2^32 a non-square's root is
// at least 2^-17 below the next integer, far more than the rounding error
// of about 2^-37, and a square's root is exact. isqrt32Bits is libopus's
// bit-by-bit method, which a test compares with it.
func isqrt32(val uint32) uint32 {
	return uint32(math.Sqrt(float64(val)))
}

// isqrt32Bits is isqrt32 as libopus computes it.
func isqrt32Bits(val uint32) uint32 {
	// Uses the second method from
	// http://www.azillionmonkeys.com/qed/sqroot.html: search for the largest
	// binary digit b such that (g+b)*(g+b) <= val, and add it to g.
	g := uint32(0)
	bshift := (ilog(val) - 1) >> 1
	b := uint32(1) << uint(bshift)
	for {
		t := ((g << 1) + b) << uint(bshift)
		if t <= val {
			g += b
			val -= t
		}
		b >>= 1
		bshift--
		if bshift < 0 {
			break
		}
	}
	return g
}

// The float build maps these mathops.h functions to libm, computing in
// double precision and rounding the result to float.

// celtSqrt is celt_sqrt: (float)sqrt(x).
func celtSqrt(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}

// celtRsqrt is celt_rsqrt (and celt_rsqrt_norm): 1.f/celt_sqrt(x).
func celtRsqrt(x float32) float32 {
	return 1 / celtSqrt(x)
}

// celtCosNorm is celt_cos_norm: (float)cos((.5f*PI)*x), with the product
// rounded to float before the double cosine.
func celtCosNorm(x float32) float32 {
	return float32(math.Cos(float64(halfPi * x)))
}

// celtExp2 is celt_exp2 without FLOAT_APPROX:
// (float)exp(0.6931471805599453094*x) in double precision.
func celtExp2(x float32) float32 {
	return float32(math.Exp(0.6931471805599453094 * float64(x)))
}

// celtUdiv is celt_udiv (entcode.h): unsigned 32-bit division.
func celtUdiv(n, d uint32) uint32 {
	return n / d
}

// celtSudiv is celt_sudiv (entcode.h): signed division rounding toward
// zero, as C's.
func celtSudiv(n, d int) int {
	return n / d
}

// ilog is EC_ILOG: the number of bits needed to represent v, 0 for 0.
func ilog(v uint32) int {
	return entcode.ILog(v)
}

// The float build's MIN16, MAX16, MIN32 and MAX32 macros: (a) < (b) ? (a) :
// (b) and (a) > (b) ? (a) : (b). Unlike Go's min and max they return b
// when a and b compare equal or one is a NaN.

func min16(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func max16(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func min32(a, b float32) float32 {
	return min16(a, b)
}

func max32(a, b float32) float32 {
	return max16(a, b)
}
