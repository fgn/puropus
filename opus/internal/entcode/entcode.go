// Copyright (c) 2001-2011 Timothy B. Terriberry
// Copyright (c) 2001-2008 Timothy B. Terriberry
// Copyright (c) 2008-2009 Xiph.Org Foundation
// Copyright (c) 2003-2008 Timothy B. Terriberry
// Copyright (c) 2008 Xiph.Org Foundation
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
// Ported from libopus 1.5.2 celt/entcode.h, celt/entcode.c, celt/mfrngcod.h
// and celt/ecintrin.h.

// Package entcode is the Opus range decoder (RFC 6716 §4.1), a port of the
// decoder side of libopus's entropy coder: celt/entcode.c, celt/entdec.c and
// the constants of celt/mfrngcod.h. CELT, SILK and the Opus framing layer
// share one Decoder per frame.
//
// The methods keep libopus's names without the ec_ prefix and Go-cased
// (ec_dec_uint is DecUint, ec_tell_frac is TellFrac), its unsigned argument
// types and its results, so ports of the codec layers read like the C. The
// fields libopus's decoders touch directly are reached through small
// accessors: Rng (the final range), Storage and SetStorage (the redundancy
// split in opus_decode_frame), SetTell (the silence case of
// celt_decode_with_ec) and HasError (ec_get_error).
//
// A Decoder is a plain value with no heap state of its own: embed it in the
// codec state or keep it on the stack, and call Init for every frame.
package entcode

import "math/bits"

// BitRes is the resolution of fractional bit counts: TellFrac counts in
// 1/(1<<BitRes) bits (BITRES in entcode.h).
const BitRes = 3

// Constants of the range coder (entcode.h and mfrngcod.h).
const (
	// uintBits is the number of bits of a DecUint value coded with the
	// range coder; the rest are raw bits (EC_UINT_BITS).
	uintBits = 8
	// windowSize is the size of the raw-bit window (EC_WINDOW_SIZE).
	windowSize = 32
	// symBits is the number of bits read at a time (EC_SYM_BITS).
	symBits = 8
	// codeBits is the size of the state registers (EC_CODE_BITS).
	codeBits = 32
	// symMax is the largest symbol (EC_SYM_MAX).
	symMax = 1<<symBits - 1
	// codeTop is the carry bit of the high-order symbol (EC_CODE_TOP).
	codeTop = uint32(1) << (codeBits - 1)
	// codeBot is the low-order bit of the high-order symbol (EC_CODE_BOT).
	codeBot = codeTop >> symBits
	// codeExtra is the number of bits of the last, partial symbol in the
	// code register (EC_CODE_EXTRA).
	codeExtra = (codeBits-2)%symBits + 1
)

// Decoder is the range decoder state (ec_ctx, used as ec_dec). The zero value
// is not ready for decoding; call Init.
type Decoder struct {
	// buf is the frame being decoded, cut to its storage size so that range
	// bytes are read from the front and raw bits from the back of buf.
	buf []byte
	// endOffs counts the bytes read from the end for raw bits.
	endOffs int
	// endWindow holds raw bits read from the end and not yet used.
	endWindow uint32
	// nendBits is the number of valid bits in endWindow.
	nendBits int
	// nbitsTotal is the number of whole bits read, not counting the partial
	// bits still in the range coder.
	nbitsTotal int
	// offs is the offset of the next range coder byte.
	offs int
	// rng is the number of values in the current range.
	rng uint32
	// val is the difference between the top of the current range and the
	// input value, minus one.
	val uint32
	// ext is the normalization factor saved by Decode for DecUpdate.
	ext uint32
	// rem is the last byte read, whose low bits are not used yet.
	rem uint32
	// err is set when DecUint reads a value out of range.
	err bool
}

// ILog returns the number of bits needed to represent v: 0 for 0, otherwise
// floor(log2(v))+1 (EC_ILOG; libopus leaves the value for 0 undefined).
func ILog(v uint32) int {
	return bits.Len32(v)
}

// Tell returns the number of bits read so far, rounded up: the whole bits
// read plus the bits still undecided in the range (ec_tell). Encoder and
// decoder agree on it, so it drives coding decisions.
func (d *Decoder) Tell() int {
	return d.nbitsTotal - ILog(d.rng)
}

// tellFracCorrection holds the thresholds where the next 1/8 bit begins
// (the table in ec_tell_frac).
var tellFracCorrection = [8]uint32{35733, 38967, 42495, 46340, 50535, 55109, 60097, 65535}

// TellFrac returns the number of bits read so far in 1/8 bits
// (1<<BitRes), rounded up (ec_tell_frac, the table-driven version libopus
// compiles). libopus returns opus_uint32; the value is never negative.
func (d *Decoder) TellFrac() int {
	nbits := d.nbitsTotal << BitRes
	l := ILog(d.rng)
	r := d.rng >> uint(l-16)
	b := (r >> 12) - 8
	if r > tellFracCorrection[b&7] {
		b++
	}
	return nbits - (l<<3 + int(b))
}

// Rng returns the current range. After the last symbol of a frame it is the
// final range that OPUS_GET_FINAL_RANGE reports.
func (d *Decoder) Rng() uint32 {
	return d.rng
}

// Storage returns the size of the frame in bytes, as passed to Init or
// reduced by SetStorage (the storage field).
func (d *Decoder) Storage() int {
	return len(d.buf)
}

// SetStorage reduces the frame to its first storage bytes, so that raw bits
// are read from the new end, as opus_decode_frame does with
// dec.storage -= redundancy_bytes before CELT decodes a frame that carries
// redundancy. storage must be between 0 and Storage().
func (d *Decoder) SetStorage(storage int) {
	d.buf = d.buf[:storage]
}

// SetTell advances the bit count so that Tell returns tell, as
// celt_decode_with_ec does with dec->nbits_total += tell - ec_tell(dec) to
// treat the rest of a silent frame as read.
func (d *Decoder) SetTell(tell int) {
	d.nbitsTotal += tell - d.Tell()
}

// HasError reports whether DecUint decoded a value out of range
// (ec_get_error).
func (d *Decoder) HasError() bool {
	return d.err
}
