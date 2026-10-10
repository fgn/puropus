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
// Ported from libopus 1.5.2 silk/SigProc_FIX.h, silk/macros.h, silk/Inlines.h and silk/typedef.h.

package silk

import (
	"math"
	"math/bits"
)

// The fixed-point macros of SigProc_FIX.h, macros.h and Inlines.h. libopus's
// SILK decoder is integer code; these keep the C semantics: opus_int32
// arithmetic wraps (Go's int32 does), >> on signed values is arithmetic, and
// the OPUS_FAST_INT64 forms of the 16x32 multiplies are used (identical
// results to the 32-bit forms).

// Limits of the fixed-point types (typedef.h).
const (
	int32Max = math.MaxInt32 // silk_int32_MAX
	int32Min = math.MinInt32 // silk_int32_MIN
	int16Max = math.MaxInt16 // silk_int16_MAX
	int16Min = math.MinInt16 // silk_int16_MIN
)

// smulwb is silk_SMULWB: (a32 * (opus_int32)((opus_int16)(b32))) >> 16.
func smulwb(a32, b32 int32) int32 {
	return int32((int64(a32) * int64(int16(b32))) >> 16)
}

// smlawb is silk_SMLAWB: a32 + ((b32 * (opus_int32)((opus_int16)(c32))) >> 16).
func smlawb(a32, b32, c32 int32) int32 {
	return int32(int64(a32) + (int64(b32)*int64(int16(c32)))>>16)
}

// smulbb is silk_SMULBB: the product of the low 16 bits of a32 and b32.
func smulbb(a32, b32 int32) int32 {
	return int32(int16(a32)) * int32(int16(b32))
}

// smlabb is silk_SMLABB: a32 + the product of the low 16 bits of b32 and c32.
func smlabb(a32, b32, c32 int32) int32 {
	return a32 + int32(int16(b32))*int32(int16(c32))
}

// smulww is silk_SMULWW: (a32 * b32) >> 16.
func smulww(a32, b32 int32) int32 {
	return int32((int64(a32) * int64(b32)) >> 16)
}

// smlaww is silk_SMLAWW: a32 + ((b32 * c32) >> 16).
func smlaww(a32, b32, c32 int32) int32 {
	return int32(int64(a32) + (int64(b32)*int64(c32))>>16)
}

// smultt is silk_SMULTT: (a32 >> 16) * (b32 >> 16).
func smultt(a32, b32 int32) int32 {
	return (a32 >> 16) * (b32 >> 16)
}

// smull is silk_SMULL: the 64-bit product of a32 and b32.
func smull(a32, b32 int32) int64 {
	return int64(a32) * int64(b32)
}

// smmul is silk_SMMUL: the top 32 bits of the 64-bit product.
func smmul(a32, b32 int32) int32 {
	return int32(smull(a32, b32) >> 32)
}

// addSat32 is silk_ADD_SAT32: a + b saturated to the int32 range. The C
// macro tests the sign bits of a, b and their wrapped sum; the exact sum in
// int64, clamped, is the same value and compiles without branches.
func addSat32(a, b int32) int32 {
	return int32(min(max(int64(a)+int64(b), int32Min), int32Max))
}

// subSat32 is silk_SUB_SAT32: a - b saturated to the int32 range, formed like
// addSat32.
func subSat32(a, b int32) int32 {
	return int32(min(max(int64(a)-int64(b), int32Min), int32Max))
}

// sat16 is silk_SAT16 followed by the (opus_int16) cast every caller applies.
func sat16(a int32) int16 {
	if a > int16Max {
		return int16Max
	}
	if a < int16Min {
		return int16Min
	}
	return int16(a)
}

// limit is silk_LIMIT: a clamped to the range spanned by limit1 and limit2,
// in either order.
func limit[T int | int32](a, limit1, limit2 T) T {
	if limit1 > limit2 {
		if a > limit1 {
			return limit1
		}
		if a < limit2 {
			return limit2
		}
		return a
	}
	if a > limit2 {
		return limit2
	}
	if a < limit1 {
		return limit1
	}
	return a
}

// lshiftSat32 is silk_LSHIFT_SAT32: a << shift, saturating before the shift
// (silk_LIMIT_32 with silk_int32_MIN >> shift <= silk_int32_MAX >> shift), so
// a large positive a gives silk_int32_MAX >> shift << shift, not
// silk_int32_MAX.
func lshiftSat32(a int32, shift int) int32 {
	return min(max(a, int32(int32Min)>>shift), int32(int32Max)>>shift) << shift
}

// rshiftRound is silk_RSHIFT_ROUND: a >> shift rounded to nearest, shift > 0.
func rshiftRound(a int32, shift int) int32 {
	if shift == 1 {
		return (a >> 1) + (a & 1)
	}
	return ((a >> (shift - 1)) + 1) >> 1
}

// rshiftRound64 is silk_RSHIFT_ROUND64.
func rshiftRound64(a int64, shift int) int64 {
	if shift == 1 {
		return (a >> 1) + (a & 1)
	}
	return ((a >> (shift - 1)) + 1) >> 1
}

// abs32 is silk_abs for opus_int32 (wrapping at silk_int32_MIN, as in C).
func abs32(a int32) int32 {
	if a > 0 {
		return a
	}
	return -a
}

// The constants of silk_RAND.
const (
	randMultiplier = 196314165 // RAND_MULTIPLIER
	randIncrement  = 907633515 // RAND_INCREMENT
)

// randNext is silk_RAND: the linear congruential generator of SILK.
func randNext(seed int32) int32 {
	return int32(uint32(randIncrement) + uint32(seed)*uint32(randMultiplier))
}

// clz32 is silk_CLZ32: the number of leading zero bits of in32 (32 for 0).
func clz32(in32 int32) int {
	return bits.LeadingZeros32(uint32(in32))
}

// ror32 is silk_ROR32: a32 rotated right by rot bits (left for negative rot).
func ror32(a32 int32, rot int) int32 {
	return int32(bits.RotateLeft32(uint32(a32), -rot))
}

// clzFrac is silk_CLZ_FRAC: the leading zeros of in and the 7 bits right
// after the leading one.
func clzFrac(in int32) (lz, fracQ7 int32) {
	lzeros := clz32(in)
	return int32(lzeros), ror32(in, 24-lzeros) & 0x7f
}

// sqrtApprox is silk_SQRT_APPROX: an approximation of the square root.
func sqrtApprox(x int32) int32 {
	if x <= 0 {
		return 0
	}
	lz, fracQ7 := clzFrac(x)
	var y int32
	if lz&1 != 0 {
		y = 32768
	} else {
		y = 46214 // sqrt(2) * 32768
	}
	// Get the scaling right.
	y >>= lz >> 1
	// Increment using the fractional part of the input.
	return smlawb(y, y, smulbb(213, fracQ7))
}

// div32VarQ is silk_DIV32_varQ: a good approximation of (a32 << Qres) / b32.
func div32VarQ(a32, b32 int32, qres int) int32 {
	// Compute the number of bits of head room and normalize the inputs.
	aHeadrm := clz32(abs32(a32)) - 1
	a32Nrm := a32 << aHeadrm // Q: aHeadrm
	bHeadrm := clz32(abs32(b32)) - 1
	b32Nrm := b32 << bHeadrm // Q: bHeadrm

	// Inverse of b32, with 14 bits of precision.
	b32Inv := int32(int32Max>>2) / (b32Nrm >> 16) // Q: 29 + 16 - bHeadrm

	// First approximation.
	result := smulwb(a32Nrm, b32Inv) // Q: 29 + aHeadrm - bHeadrm

	// Compute the residual by subtracting the product of the denominator and
	// the first approximation; it may wrap, the final value is small.
	a32Nrm -= smmul(b32Nrm, result) << 3 // Q: aHeadrm

	// Refinement.
	result = smlawb(result, a32Nrm, b32Inv) // Q: 29 + aHeadrm - bHeadrm

	// Convert to the Qres domain.
	lshift := 29 + aHeadrm - bHeadrm - qres
	if lshift < 0 {
		return lshiftSat32(result, -lshift)
	}
	if lshift < 32 {
		return result >> lshift
	}
	// Avoid an undefined result.
	return 0
}

// inverse32VarQ is silk_INVERSE32_varQ: a good approximation of
// (1 << Qres) / b32.
func inverse32VarQ(b32 int32, qres int) int32 {
	// Compute the number of bits of head room and normalize the input.
	bHeadrm := clz32(abs32(b32)) - 1
	b32Nrm := b32 << bHeadrm // Q: bHeadrm

	// Inverse of b32, with 14 bits of precision.
	b32Inv := int32(int32Max>>2) / (b32Nrm >> 16) // Q: 29 + 16 - bHeadrm

	// First approximation.
	result := b32Inv << 16 // Q: 61 - bHeadrm

	// Compute the residual by subtracting the product of the denominator and
	// the first approximation from one.
	errQ32 := ((int32(1) << 29) - smulwb(b32Nrm, b32Inv)) << 3 // Q32

	// Refinement.
	result = smlaww(result, errQ32, b32Inv) // Q: 61 - bHeadrm

	// Convert to the Qres domain.
	lshift := 61 - bHeadrm - qres
	if lshift <= 0 {
		return lshiftSat32(result, -lshift)
	}
	if lshift < 32 {
		return result >> lshift
	}
	// Avoid an undefined result.
	return 0
}
