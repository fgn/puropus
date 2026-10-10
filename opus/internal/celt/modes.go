// Copyright (c) 2007-2008 CSIRO
// Copyright (c) 2007-2009 Xiph.Org Foundation
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
// Ported from libopus 1.5.2 celt/modes.c and celt/modes.h (the static 48 kHz
// mode only; custom modes are left out).

package celt

import "sync"

// maxPeriod is the longest pitch period of the post-filter and the PLC, and
// the size of the PLC history (MAX_PERIOD).
const maxPeriod = 1024

// pulseCache holds the precomputed bit costs of the PVQ codebooks
// (PulseCache).
type pulseCache struct {
	size  int
	index []int16
	bits  []uint8
	caps  []uint8

	// line and pulsesOf are derived from index and bits at init. line
	// numbers the distinct cache lines that index points to (-1 where index
	// is -1), and pulsesOf[line[j]][b] is bits2pulsesSearch for cache line
	// index[j] and an allocation of b 1/8 bits, b clamped to
	// [0, bits2pulsesMax].
	line     []int8
	pulsesOf [][bits2pulsesMax + 1]uint8
	// maxBits[j] is the cost of the largest pulse count of cache line
	// index[j], cache[cache[0]], which quant_partition compares with the
	// band's bits to decide a split (0 where index is -1).
	maxBits []int16
}

// Mode is a CELT mode (CELTMode, OpusCustomMode). Opus uses one, the static
// 48 kHz mode with 960-sample frames that StaticMode returns; custom modes
// are not supported.
type Mode struct {
	fs            int
	overlap       int
	nbEBands      int
	effEBands     int
	preemph       [4]float32
	eBands        []int // definition of each "pseudo-critical band"
	maxLM         int
	nbShortMdcts  int
	shortMdctSize int
	// nbAllocVectors is the number of lines in allocVectors.
	nbAllocVectors int
	// allocVectors holds the bits in each band for several rates.
	allocVectors []uint8
	logN         []int
	window       []float32
	mdct         mdctLookup
	cache        pulseCache
}

// Window returns the mode's MDCT overlap window, Overlap() values rising
// from 0 to 1 (mode->window, which opus_decode_frame's smooth_fade uses).
// The caller must not modify it.
func (m *Mode) Window() []float32 {
	return m.window
}

// Overlap returns the length of the MDCT overlap in samples (mode->overlap).
func (m *Mode) Overlap() int {
	return m.overlap
}

// eband5ms holds the band edges of the 2.5 ms short blocks, in units of
// shortMdctSize/120 bins (eband5ms):
// 0  200 400 600 800  1k 1.2 1.4 1.6  2k 2.4 2.8 3.2  4k 4.8 5.6 6.8  8k 9.6 12k 15.6 (Hz).
var eband5ms = [22]int{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 10, 12, 14, 16, 20, 24, 28, 34, 40, 48, 60, 78, 100,
}

// bitallocSize is the number of allocation vectors (BITALLOC_SIZE).
const bitallocSize = 11

// bandAllocation is the bit allocation table in units of 1/32 bit/sample
// (0.1875 dB SNR), one line per rate (band_allocation, the alternate tuning
// partially derived from Vorbis).
var bandAllocation = [bitallocSize * 21]uint8{
	// 0  200 400 600 800  1k 1.2 1.4 1.6  2k 2.4 2.8 3.2  4k 4.8 5.6 6.8  8k 9.6 12k 15.6
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	90, 80, 75, 69, 63, 56, 49, 40, 34, 29, 20, 18, 10, 0, 0, 0, 0, 0, 0, 0, 0,
	110, 100, 90, 84, 78, 71, 65, 58, 51, 45, 39, 32, 26, 20, 12, 0, 0, 0, 0, 0, 0,
	118, 110, 103, 93, 86, 80, 75, 70, 65, 59, 53, 47, 40, 31, 23, 15, 4, 0, 0, 0, 0,
	126, 119, 112, 104, 95, 89, 83, 78, 72, 66, 60, 54, 47, 39, 32, 25, 17, 12, 1, 0, 0,
	134, 127, 120, 114, 103, 97, 91, 85, 78, 72, 66, 60, 54, 47, 41, 35, 29, 23, 16, 10, 1,
	144, 137, 130, 124, 113, 107, 101, 95, 88, 82, 76, 70, 64, 57, 51, 45, 39, 33, 26, 15, 1,
	152, 145, 138, 132, 123, 117, 111, 105, 98, 92, 86, 80, 74, 67, 61, 55, 49, 43, 36, 20, 1,
	162, 155, 148, 142, 133, 127, 121, 115, 108, 102, 96, 90, 84, 77, 71, 65, 59, 53, 46, 30, 1,
	172, 165, 158, 152, 143, 137, 131, 125, 118, 112, 106, 100, 94, 87, 81, 75, 69, 63, 56, 45, 20,
	200, 200, 200, 200, 200, 200, 200, 200, 198, 193, 188, 183, 178, 173, 168, 163, 158, 153, 148, 129, 104,
}

var tablesOnce sync.Once

// initTables builds, once, the tables derived from the static mode and
// libopus's tables that the decoder uses in place of computing the same
// values per frame. Init calls it.
func initTables() {
	tablesOnce.Do(func() {
		buildPVQRows()
		buildRotationGains()
		buildPulseTables()
		buildThetaGains()
		buildFFTConfigs()
		buildSIMDTables()
	})
}

// StaticMode returns the static 48 kHz mode with 960-sample frames and a
// 120-sample overlap, the only one Opus uses (opus_custom_mode_create(48000,
// 960, NULL) without custom modes).
func StaticMode() *Mode {
	return &mode48000x960x120
}
