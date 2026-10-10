// Copyright (c) 2010 Xiph.Org Foundation, Skype Limited
// Written by Jean-Marc Valin and Koen Vos
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
// Ported from libopus 1.5.2 src/opus_decoder.c (a vector form of the SILK output loop in
// opus_decode_frame).

//go:build goexperiment.simd && amd64

package opus

import "simd/archsimd"

// The SILK output conversion with AVX: eight samples per step are
// sign-extended, converted (exactly) and multiplied by 1/32768 (exactly)
// with 128-bit VEX instructions, then stored or added to pcm with one
// rounding, as the scalar loops do. The tail takes the scalar loops.

// pcmUseAVX selects the vector kernels.
var pcmUseAVX = archsimd.X86.AVX()

// pcmAccel names the kernels this CPU runs.
func pcmAccel() string {
	if pcmUseAVX {
		return "simd-amd64-avx"
	}
	return "scalar"
}

// pcmScale is 1/32768 in every lane.
var pcmScale = [4]float32{1.0 / 32768, 1.0 / 32768, 1.0 / 32768, 1.0 / 32768}

func silkToFloat(pcm []float32, x []int16) {
	if pcmUseAVX {
		silkToFloatAVX(pcm, x)
		return
	}
	silkToFloatScalar(pcm, x)
}

func addSilk(pcm []float32, x []int16) {
	if pcmUseAVX {
		addSilkAVX(pcm, x)
		return
	}
	addSilkScalar(pcm, x)
}

// silkPCM8 returns the eight samples of x as floats times 1/32768.
func silkPCM8(x *[8]int16, scale archsimd.Float32x4) (lo, hi archsimd.Float32x4) {
	v := archsimd.LoadInt16x8Array(x)
	lo = v.ExtendLo4ToInt32().ConvertToFloat32().Mul(scale)
	hi = v.ToBits().ReshapeToUint32s().BitsToInt32().PermuteScalars(2, 3, 2, 3).
		ToBits().ReshapeToUint16s().BitsToInt16().ExtendLo4ToInt32().ConvertToFloat32().Mul(scale)
	return lo, hi
}

func silkToFloatAVX(pcm []float32, x []int16) {
	archsimd.ClearAVXUpperBits()
	x = x[:len(pcm)]
	scale := archsimd.LoadFloat32x4Array(&pcmScale)
	i := 0
	for ; i+8 <= len(pcm); i += 8 {
		lo, hi := silkPCM8((*[8]int16)(x[i:i+8]), scale)
		lo.StoreArray((*[4]float32)(pcm[i : i+4]))
		hi.StoreArray((*[4]float32)(pcm[i+4 : i+8]))
	}
	silkToFloatScalar(pcm[i:], x[i:])
}

func addSilkAVX(pcm []float32, x []int16) {
	archsimd.ClearAVXUpperBits()
	x = x[:len(pcm)]
	scale := archsimd.LoadFloat32x4Array(&pcmScale)
	i := 0
	for ; i+8 <= len(pcm); i += 8 {
		lo, hi := silkPCM8((*[8]int16)(x[i:i+8]), scale)
		p := (*[4]float32)(pcm[i : i+4])
		archsimd.LoadFloat32x4Array(p).Add(lo).StoreArray(p)
		p = (*[4]float32)(pcm[i+4 : i+8])
		archsimd.LoadFloat32x4Array(p).Add(hi).StoreArray(p)
	}
	addSilkScalar(pcm[i:], x[i:])
}
