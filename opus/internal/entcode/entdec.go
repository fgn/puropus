// Copyright (c) 2001-2011 Timothy B. Terriberry
// Copyright (c) 2008-2009 Xiph.Org Foundation
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
// Ported from libopus 1.5.2 celt/entdec.c and celt/entdec.h.

package entcode

import "math/bits"

// readByteFromEnd returns the next raw-bit byte, counting from the end of the
// frame, or 0 once it meets the start (ec_read_byte_from_end).
func (d *Decoder) readByteFromEnd() uint32 {
	if d.endOffs < len(d.buf) {
		d.endOffs++
		return uint32(d.buf[len(d.buf)-d.endOffs])
	}
	return 0
}

// normalize stores the range and value a symbol leaves, rescaling them so
// that rng lies entirely in the high-order symbol and reading input as
// needed (ec_dec_normalize). The callers keep rng and val in locals and
// pass them here once; the common case, no rescaling, inlines.
func (d *Decoder) normalize(rng, val uint32) {
	if rng > codeBot {
		d.rng, d.val = rng, val
		return
	}
	d.renormalize(rng, val)
}

// renormalize is the loop of ec_dec_normalize, with the state in locals.
func (d *Decoder) renormalize(rng, val uint32) {
	nbitsTotal, rem, offs := d.nbitsTotal, d.rem, d.offs
	for rng <= codeBot {
		nbitsTotal += symBits
		rng <<= symBits
		// Use up the remaining bits of the last byte and take the rest
		// from the next one, or 0 past the end of the frame
		// (ec_read_byte).
		sym := rem
		rem = 0
		if offs < len(d.buf) {
			rem = uint32(d.buf[offs])
			offs++
		}
		sym = (sym<<symBits | rem) >> (symBits - codeExtra)
		// Subtract them from val, capped to stay below codeTop.
		val = ((val << symBits) + (symMax &^ sym)) & (codeTop - 1)
	}
	d.nbitsTotal, d.rem, d.offs = nbitsTotal, rem, offs
	d.rng, d.val = rng, val
}

// Init starts decoding the frame buf; Storage is len(buf) (ec_dec_init). The
// decoder keeps buf without copying it until the next Init.
func (d *Decoder) Init(buf []byte) {
	d.buf = buf
	d.endOffs = 0
	d.endWindow = 0
	d.nendBits = 0
	// The offset from which Tell subtracts partial bits. After normalize it
	// matches the encoder's, compensating for the bits normalize adds.
	d.nbitsTotal = codeBits + 1 - ((codeBits-codeExtra)/symBits)*symBits
	d.offs = 0
	d.rem = 0
	if len(buf) > 0 {
		d.rem = uint32(buf[0])
		d.offs = 1
	}
	d.ext = 0
	d.err = false
	rng := uint32(1) << codeExtra
	d.normalize(rng, rng-1-d.rem>>(symBits-codeExtra))
}

// Decode returns the cumulative frequency of the next symbol of an alphabet
// with total frequency ft (ec_decode). If the symbol occupies [fl, fh), the
// result lies in that interval, and DecUpdate(fl, fh, ft) must follow before
// the next symbol is decoded.
func (d *Decoder) Decode(ft uint32) uint32 {
	d.ext = d.rng / ft
	s := d.val / d.ext
	return ft - min(s+1, ft)
}

// DecodeBin is Decode with ft = 1<<bits (ec_decode_bin).
func (d *Decoder) DecodeBin(bits uint) uint32 {
	ft := uint32(1) << bits
	d.ext = d.rng >> bits
	s := d.val / d.ext
	return ft - min(s+1, ft)
}

// DecUpdate advances past the symbol [fl, fh) of total frequency ft, which
// must be the ft of the preceding Decode or 1<<bits of DecodeBin
// (ec_dec_update).
func (d *Decoder) DecUpdate(fl, fh, ft uint32) {
	s := d.ext * (ft - fh)
	rng := d.rng - s
	if fl > 0 {
		rng = d.ext * (fh - fl)
	}
	d.normalize(rng, d.val-s)
}

// DecBitLogp decodes a binary symbol whose chance of being 1 is
// 1/(1<<logp), returned as 0 or 1 like ec_dec_bit_logp, so ports can use it
// in arithmetic.
func (d *Decoder) DecBitLogp(logp uint) int {
	return d.decBit(d.rng >> logp)
}

// decBit decodes a binary symbol whose 1 occupies the bottom s values of the
// range: ec_dec_bit_logp with s = rng>>logp, and ec_dec_icdf with the table
// {icdf0, 0} with s = (rng>>ftb)*icdf0. It selects the new range and value
// without a branch, as the bit is often unpredictable.
func (d *Decoder) decBit(s uint32) int {
	r, v := d.rng, d.val
	// val < rng <= 1<<31 and s <= rng, so v-s does not overflow as an
	// int32: m is all ones when v < s, the symbol 1, and zero otherwise.
	m := uint32(int32(v-s) >> 31)
	d.normalize(s&m|(r-s)&^m, v-s&^m)
	return int(m & 1)
}

// DecICDF decodes a symbol with an "inverse" CDF of 1<<ftb total frequency:
// symbol s occupies [ft-icdf[s-1], ft-icdf[s]) with icdf[-1] read as ft
// (ec_dec_icdf). The table must be non-increasing and end with 0. No
// DecUpdate follows.
func (d *Decoder) DecICDF(icdf []uint8, ftb uint) int {
	s := d.rng
	v := d.val
	r := s >> ftb
	ret := -1
	var t uint32
	for {
		t = s
		ret++
		s = r * uint32(icdf[ret])
		if v >= s {
			break
		}
	}
	d.normalize(t-s, v-s)
	return ret
}

// DecICDF2 is DecICDF with the two-entry table {icdf0, 0}, without a branch
// on the decoded symbol.
func (d *Decoder) DecICDF2(icdf0 uint8, ftb uint) int {
	return d.decBit((d.rng >> ftb) * uint32(icdf0))
}

// DecICDF16 is DecICDF with a 16-bit table (ec_dec_icdf16).
func (d *Decoder) DecICDF16(icdf []uint16, ftb uint) int {
	s := d.rng
	v := d.val
	r := s >> ftb
	ret := -1
	var t uint32
	for {
		t = s
		ret++
		s = r * uint32(icdf[ret])
		if v >= s {
			break
		}
	}
	d.normalize(t-s, v-s)
	return ret
}

// DecUint decodes an integer in [0, ft) coded with ec_enc_uint: up to the
// top 8 bits with the range coder and the rest as raw bits (ec_dec_uint). ft
// must be at least 2. A value out of range sets the error flag and decodes as
// ft-1.
func (d *Decoder) DecUint(ft uint32) uint32 {
	ft--
	ftb := ILog(ft)
	if ftb > uintBits {
		ftb -= uintBits
		top := ft>>uint(ftb) + 1
		s := d.decodeUniform(top)
		t := s<<uint(ftb) | d.DecBits(uint(ftb))
		if t <= ft {
			return t
		}
		d.err = true
		return ft
	}
	return d.decodeUniform(ft + 1)
}

// decodeUniform is ec_decode(ft) followed by ec_dec_update(s, s+1, ft), the
// uniform symbol of ec_dec_uint, for 2 <= ft <= 256 (as ec_dec_uint's ft
// always is). The division of rng by ft is a multiplication by udivRecip.
func (d *Decoder) decodeUniform(ft uint32) uint32 {
	rng, val := d.rng, d.val
	ext, _ := bits.Mul64(uint64(rng), udivRecip[ft])
	d.ext = uint32(ext)
	s := val / d.ext
	s = ft - min(s+1, ft)
	sub := d.ext * (ft - s - 1)
	if s > 0 {
		rng = d.ext
	} else {
		rng -= sub
	}
	d.normalize(rng, val-sub)
	return s
}

// udivRecip holds ceil(2^64/n) for 2 <= n <= 256: the high half of the
// 128-bit product of a 32-bit x with udivRecip[n] is x/n. With x = q*n+r,
// the product over 2^64 is q + r/n + x*e/(n*2^64) for some 0 <= e < n, and
// the last term is below 2^-24, so it never carries r/n <= 1-1/n over the
// next integer. (libopus's celt_udiv uses a table of 2^32/n for divisors up
// to 256 on ARM.)
var udivRecip = func() (t [257]uint64) {
	for n := uint64(2); n < uint64(len(t)); n++ {
		q, r := bits.Div64(1, 0, n)
		if r != 0 {
			q++
		}
		t[n] = q
	}
	return t
}()

// DecBits reads bits raw bits, between 0 and 25, from the end of the frame
// (ec_dec_bits).
func (d *Decoder) DecBits(bits uint) uint32 {
	window := d.endWindow
	available := d.nendBits
	if uint(available) < bits {
		for {
			window |= d.readByteFromEnd() << uint(available)
			available += symBits
			if available > windowSize-symBits {
				break
			}
		}
	}
	ret := window & (uint32(1)<<bits - 1)
	window >>= bits
	available -= int(bits)
	d.endWindow = window
	d.nendBits = available
	d.nbitsTotal += int(bits)
	return ret
}
