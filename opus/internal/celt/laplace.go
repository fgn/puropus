// Copyright (c) 2007 CSIRO
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
// Ported from libopus 1.5.2 celt/laplace.c (the decoder side).

package celt

import "github.com/fgn/puropus/opus/internal/entcode"

const (
	// laplaceLogMinp is log2 of the minimum probability of an energy delta,
	// out of 32768 (LAPLACE_LOG_MINP).
	laplaceLogMinp = 0
	// laplaceMinp is the minimum probability of an energy delta
	// (LAPLACE_MINP).
	laplaceMinp = 1 << laplaceLogMinp
	// laplaceNmin is the minimum number of guaranteed representable energy
	// deltas, in one direction (LAPLACE_NMIN).
	laplaceNmin = 16
)

// ecLaplaceGetFreq1 returns the frequency of +1 and -1 given that of 0;
// decay is positive and at most 11456 (ec_laplace_get_freq1).
func ecLaplaceGetFreq1(fs0 uint32, decay int) uint32 {
	ft := 32768 - laplaceMinp*(2*laplaceNmin) - fs0
	return ft * uint32(int32(16384-decay)) >> 15
}

// ecLaplaceDecode decodes a value coded with a Laplace-like distribution
// whose probability of 0 is fs/32768 and decay the ratio between the
// probabilities of consecutive magnitudes in Q14 (ec_laplace_decode).
func ecLaplaceDecode(dec *entcode.Decoder, fs uint32, decay int) int {
	val := 0
	fm := dec.DecodeBin(15)
	fl := uint32(0)
	if fm >= fs {
		val++
		fl = fs
		fs = ecLaplaceGetFreq1(fs, decay) + laplaceMinp
		// Search the decaying part of the PDF.
		for fs > laplaceMinp && fm >= fl+2*fs {
			fs *= 2
			fl += fs
			fs = ((fs - 2*laplaceMinp) * uint32(int32(decay))) >> 15
			fs += laplaceMinp
			val++
		}
		// Everything beyond that has probability LAPLACE_MINP.
		if fs <= laplaceMinp {
			di := int((fm - fl) >> (laplaceLogMinp + 1))
			val += di
			fl += uint32(2 * di * laplaceMinp)
		}
		if fm < fl+fs {
			val = -val
		} else {
			fl += fs
		}
	}
	dec.DecUpdate(fl, min(fl+fs, 32768), 32768)
	return val
}
