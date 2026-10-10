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
// Ported from libopus 1.5.2 celt/rate.c and celt/rate.h (the decoder side).

package celt

import "github.com/fgn/puropus/opus/internal/entcode"

// Allocation constants (rate.h).
const (
	logMaxPseudo         = 6  // LOG_MAX_PSEUDO
	maxFineBits          = 8  // MAX_FINE_BITS
	fineOffset           = 21 // FINE_OFFSET
	qthetaOffset         = 4  // QTHETA_OFFSET
	qthetaOffsetTwoPhase = 16 // QTHETA_OFFSET_TWOPHASE
)

// bitRes is BITRES: allocations count in 1/8 bits.
const bitRes = entcode.BitRes

// getPulses converts a pseudo-pulse index to a pulse count (get_pulses).
func getPulses(i int) int {
	if i < 8 {
		return i
	}
	return (8 + (i & 7)) << ((i >> 3) - 1)
}

// bits2pulsesMax is the allocation above which bits2pulses returns the
// same pulse count: the cache's costs are below 256, so for bits-1 >= 256
// the search ends at the line's last entry, as it does for bits-1 = 256.
// At and below 0 it returns 0.
const bits2pulsesMax = 257

// bits2pulses returns the pseudo-pulse count whose cost is closest to bits
// for the band at LM (bits2pulses), from the table of bits2pulsesSearch.
func bits2pulses(m *Mode, band, LM, bits int) int {
	line := m.cache.line[(LM+1)*m.nbEBands+band]
	return int(m.cache.pulsesOf[line][min(max(bits, 0), bits2pulsesMax)])
}

// bits2pulsesSearch is bits2pulses as libopus computes it, a binary search
// of the band's cache line.
func bits2pulsesSearch(m *Mode, band, LM, bits int) int {
	LM++
	cache := m.cache.bits[m.cache.index[LM*m.nbEBands+band]:]

	lo := 0
	hi := int(cache[0])
	bits--
	for i := 0; i < logMaxPseudo; i++ {
		mid := (lo + hi + 1) >> 1
		// OPT: Make sure this is implemented with a conditional move.
		if int(cache[mid]) >= bits {
			hi = mid
		} else {
			lo = mid
		}
	}
	loBits := -1
	if lo != 0 {
		loBits = int(cache[lo])
	}
	if bits-loBits <= int(cache[hi])-bits {
		return lo
	}
	return hi
}

// buildPulseTables fills the static mode's pulse cache line and pulsesOf
// tables (initTables).
func buildPulseTables() {
	m := StaticMode()
	c := &m.cache
	c.line = make([]int8, len(c.index))
	c.maxBits = make([]int16, len(c.index))
	lines := map[int16]int8{}
	for j, off := range c.index {
		c.line[j] = -1
		if off < 0 {
			continue
		}
		cache := c.bits[off:]
		c.maxBits[j] = int16(cache[cache[0]])
		l, ok := lines[off]
		if !ok {
			l = int8(len(c.pulsesOf))
			lines[off] = l
			var row [bits2pulsesMax + 1]uint8
			LM, band := j/m.nbEBands-1, j%m.nbEBands
			for b := range row {
				row[b] = uint8(bits2pulsesSearch(m, band, LM, b))
			}
			c.pulsesOf = append(c.pulsesOf, row)
		}
		c.line[j] = l
	}
}

// pulses2bits returns the cost in 1/8 bits of the pseudo-pulse count pulses
// for the band at LM (pulses2bits).
func pulses2bits(m *Mode, band, LM, pulses int) int {
	LM++
	cache := m.cache.bits[m.cache.index[LM*m.nbEBands+band]:]
	if pulses == 0 {
		return 0
	}
	return int(cache[pulses]) + 1
}

// log2FracTable holds log2 of 1..24 in 1/8 bits, rounded up
// (LOG2_FRAC_TABLE).
var log2FracTable = [24]int{
	0,
	8, 13,
	16, 19, 21, 23,
	24, 26, 27, 28, 29, 30, 31, 32,
	32, 33, 34, 34, 35, 36, 36, 37, 37,
}

// allocSteps is the number of bisection steps of the interpolation
// (ALLOC_STEPS).
const allocSteps = 6

// interpBits2pulses interpolates between the allocation vectors to fill the
// budget, decodes the skipped bands, the intensity and dual stereo
// parameters, and splits each band's bits between PVQ (bits) and fine
// energy (ebits) (interp_bits2pulses, decoder side). It returns the number
// of coded bands.
func interpBits2pulses(m *Mode, start, end, skipStart int, bits1, bits2, thresh, caps []int, total int,
	balanceOut *int, skipRsv int, intensity *int, intensityRsv int, dualStereo *bool, dualStereoRsv int,
	bits, ebits, finePriority []int, C, LM int, ec *entcode.Decoder,
) int {
	var psum int
	var left, percoeff int
	var balance int
	var codedBands int

	allocFloor := C << bitRes
	stereo := 0
	if C > 1 {
		stereo = 1
	}

	logM := LM << bitRes
	lo := 0
	hi := 1 << allocSteps
	// Each pass of libopus's bisection sums from the last band down: bands
	// above the first one (from the top) that reaches its threshold get
	// alloc_floor or nothing, that band and all below it their capped
	// allocation. Each pass here walks down to that band, then sums the
	// rest without branching on the data.
	bits1 = bits1[:end]
	bits2 = bits2[:end]
	thresh = thresh[:end]
	caps = caps[:end]
	for i := 0; i < allocSteps; i++ {
		mid := (lo + hi) >> 1
		psum = 0
		j := end - 1
		for ; j >= start; j-- {
			tmp := bits1[j] + (mid * bits2[j] >> allocSteps)
			if tmp >= thresh[j] {
				break
			}
			if tmp >= allocFloor {
				psum += allocFloor
			}
		}
		for ; j >= start; j-- {
			tmp := bits1[j] + (mid * bits2[j] >> allocSteps)
			// Don't allocate more than we can actually use.
			psum += min(tmp, caps[j])
		}
		if psum > total {
			hi = mid
		} else {
			lo = mid
		}
	}
	psum = 0
	j := end - 1
	for ; j >= start; j-- {
		tmp := bits1[j] + (lo * bits2[j] >> allocSteps)
		if tmp >= thresh[j] {
			break
		}
		if tmp >= allocFloor {
			tmp = allocFloor
		} else {
			tmp = 0
		}
		// Don't allocate more than we can actually use.
		tmp = min(tmp, caps[j])
		bits[j] = tmp
		psum += tmp
	}
	for ; j >= start; j-- {
		tmp := bits1[j] + (lo * bits2[j] >> allocSteps)
		// Don't allocate more than we can actually use.
		tmp = min(tmp, caps[j])
		bits[j] = tmp
		psum += tmp
	}

	// Decide which bands to skip, working backwards from the end.
	for codedBands = end; ; codedBands-- {
		j := codedBands - 1
		// Never skip the first band, nor a band that has been boosted by
		// dynalloc. In the first case, we'd be coding a bit to signal we're
		// going to waste all the other bits. In the second case, we'd be
		// coding a bit to redistribute all the bits we just signaled should
		// be cocentrated in this band.
		if j <= skipStart {
			// Give the bit we reserved to end skipping back.
			total += skipRsv
			break
		}
		// Figure out how many left-over bits we would be adding to this
		// band. This can include bits we've stolen back from higher, skipped
		// bands.
		left = total - psum
		percoeff = int(int32(celtUdiv(uint32(left), uint32(m.eBands[codedBands]-m.eBands[start]))))
		left -= (m.eBands[codedBands] - m.eBands[start]) * percoeff
		rem := max(left-(m.eBands[j]-m.eBands[start]), 0)
		bandWidth := m.eBands[codedBands] - m.eBands[j]
		bandBits := bits[j] + percoeff*bandWidth + rem
		// Only code a skip decision if we're above the threshold for this
		// band. Otherwise it is force-skipped. This ensures that we have
		// enough bits to code the skip flag.
		if bandBits >= max(thresh[j], allocFloor+(1<<bitRes)) {
			if ec.DecBitLogp(1) != 0 {
				break
			}
			// We used a bit to skip this band.
			psum += 1 << bitRes
			bandBits -= 1 << bitRes
		}
		// Reclaim the bits originally allocated to this band.
		psum -= bits[j] + intensityRsv
		if intensityRsv > 0 {
			intensityRsv = log2FracTable[j-start]
		}
		psum += intensityRsv
		if bandBits >= allocFloor {
			// If we have enough for a fine energy bit per channel, use it.
			psum += allocFloor
			bits[j] = allocFloor
		} else {
			// Otherwise this band gets nothing at all.
			bits[j] = 0
		}
	}

	// Decode the intensity and dual stereo parameters.
	if intensityRsv > 0 {
		*intensity = start + int(ec.DecUint(uint32(codedBands+1-start)))
	} else {
		*intensity = 0
	}
	if *intensity <= start {
		total += dualStereoRsv
		dualStereoRsv = 0
	}
	if dualStereoRsv > 0 {
		*dualStereo = ec.DecBitLogp(1) != 0
	} else {
		*dualStereo = false
	}

	// Allocate the remaining bits.
	left = total - psum
	percoeff = int(int32(celtUdiv(uint32(left), uint32(m.eBands[codedBands]-m.eBands[start]))))
	left -= (m.eBands[codedBands] - m.eBands[start]) * percoeff
	for j := start; j < codedBands; j++ {
		bits[j] += percoeff * (m.eBands[j+1] - m.eBands[j])
	}
	for j := start; j < codedBands; j++ {
		tmp := min(left, m.eBands[j+1]-m.eBands[j])
		bits[j] += tmp
		left -= tmp
	}

	balance = 0
	j = start
	for ; j < codedBands; j++ {
		var excess int
		N0 := m.eBands[j+1] - m.eBands[j]
		N := N0 << LM
		bit := bits[j] + balance

		if N > 1 {
			excess = max(bit-caps[j], 0)
			bits[j] = bit - excess

			// Compensate for the extra DoF in stereo.
			den := C * N
			if C == 2 && N > 2 && !*dualStereo && j < *intensity {
				den++
			}

			NClogN := den * (m.logN[j] + logM)

			// Offset for the number of fine bits by log2(N)/2 + FINE_OFFSET
			// compared to their "fair share" of total/N.
			offset := (NClogN >> 1) - den*fineOffset

			// N=2 is the only point that doesn't match the curve.
			if N == 2 {
				offset += den << bitRes >> 2
			}

			// Changing the offset for allocating the second and third fine
			// energy bit.
			if bits[j]+offset < den*2<<bitRes {
				offset += NClogN >> 2
			} else if bits[j]+offset < den*3<<bitRes {
				offset += NClogN >> 3
			}

			// Divide with rounding.
			ebits[j] = max(0, bits[j]+offset+(den<<(bitRes-1)))
			ebits[j] = int(celtUdiv(uint32(ebits[j]), uint32(den))) >> bitRes

			// Make sure not to bust.
			if C*ebits[j] > bits[j]>>bitRes {
				ebits[j] = bits[j] >> stereo >> bitRes
			}

			// More than that is useless because that's about as far as PVQ
			// can go.
			ebits[j] = min(ebits[j], maxFineBits)

			// If we rounded down or capped this band, make it a candidate
			// for the final fine energy pass.
			finePriority[j] = b2i(ebits[j]*(den<<bitRes) >= bits[j]+offset)

			// Remove the allocated fine bits; the rest are assigned to PVQ.
			bits[j] -= C * ebits[j] << bitRes
		} else {
			// For N=1, all bits go to fine energy except for a single sign
			// bit.
			excess = max(0, bit-(C<<bitRes))
			bits[j] = bit - excess
			ebits[j] = 0
			finePriority[j] = 1
		}

		// Fine energy can't take advantage of the re-balancing in
		// quant_all_bands(). Instead, do the re-balancing here.
		if excess > 0 {
			extraFine := min(excess>>(stereo+bitRes), maxFineBits-ebits[j])
			ebits[j] += extraFine
			extraBits := extraFine * C << bitRes
			finePriority[j] = b2i(extraBits >= excess-balance)
			excess -= extraBits
		}
		balance = excess
	}
	// Save any remaining bits over the cap for the rebalancing in
	// quant_all_bands().
	*balanceOut = balance

	// The skipped bands use all their bits for fine energy.
	for ; j < end; j++ {
		ebits[j] = bits[j] >> stereo >> bitRes
		bits[j] = 0
		finePriority[j] = b2i(ebits[j] < 1)
	}
	return codedBands
}

// cltComputeAllocation computes the bit allocation of the bands from start
// to end of a frame of total 1/8 bits: the PVQ bits in pulses, the fine
// energy bits in ebits and their priority in finePriority, decoding the
// skip, intensity and dual stereo symbols on the way. It returns the number
// of coded bands (clt_compute_allocation, decoder side). offsets are the
// dynalloc boosts and caps the band caps of initCaps.
func cltComputeAllocation(m *Mode, start, end int, offsets, caps []int, allocTrim int, intensity *int,
	dualStereo *bool, total int, balance *int, pulses, ebits, finePriority []int, C, LM int,
	ec *entcode.Decoder,
) int {
	var bits1, bits2, thresh, trimOffset [maxBands]int

	total = max(total, 0)
	length := m.nbEBands
	skipStart := start
	// Reserve a bit to signal the end of manually skipped bands.
	skipRsv := 0
	if total >= 1<<bitRes {
		skipRsv = 1 << bitRes
	}
	total -= skipRsv
	// Reserve bits for the intensity and dual stereo parameters.
	intensityRsv := 0
	dualStereoRsv := 0
	if C == 2 {
		intensityRsv = log2FracTable[end-start]
		if intensityRsv > total {
			intensityRsv = 0
		} else {
			total -= intensityRsv
			if total >= 1<<bitRes {
				dualStereoRsv = 1 << bitRes
			}
			total -= dualStereoRsv
		}
	}

	for j := start; j < end; j++ {
		// Below this threshold, we're sure not to allocate any PVQ bits.
		thresh[j] = max(C<<bitRes, (3*(m.eBands[j+1]-m.eBands[j])<<LM<<bitRes)>>4)
		// Tilt of the allocation curve.
		trimOffset[j] = C * (m.eBands[j+1] - m.eBands[j]) * (allocTrim - 5 - LM) * (end - j - 1) *
			(1 << (LM + bitRes)) >> 6
		// Giving less resolution to single-coefficient bands because they
		// get more benefit from having one coarse value per coefficient.
		if (m.eBands[j+1]-m.eBands[j])<<LM == 1 {
			trimOffset[j] -= C << bitRes
		}
	}
	lo := 1
	hi := m.nbAllocVectors - 1
	// As in interpBits2pulses, each pass of the bisection walks down to the
	// first band (from the top) that reaches its threshold, then sums the
	// capped allocations of it and the bands below.
	var scale [maxBands]int
	for j := start; j < end; j++ {
		scale[j] = C * (m.eBands[j+1] - m.eBands[j])
	}
	for {
		psum := 0
		mid := (lo + hi) >> 1
		alloc := m.allocVectors[mid*length : mid*length+end]
		j := end - 1
		for ; j >= start; j-- {
			bitsj := scale[j] * int(alloc[j]) << LM >> 2
			if bitsj > 0 {
				bitsj = max(0, bitsj+trimOffset[j])
			}
			bitsj += offsets[j]
			if bitsj >= thresh[j] {
				break
			}
			if bitsj >= C<<bitRes {
				psum += C << bitRes
			}
		}
		for ; j >= start; j-- {
			bitsj := scale[j] * int(alloc[j]) << LM >> 2
			if t := max(0, bitsj+trimOffset[j]); bitsj > 0 {
				bitsj = t
			}
			bitsj += offsets[j]
			// Don't allocate more than we can actually use.
			psum += min(bitsj, caps[j])
		}
		if psum > total {
			hi = mid - 1
		} else {
			lo = mid + 1
		}
		if lo > hi {
			break
		}
	}
	hi = lo
	lo--
	for j := start; j < end; j++ {
		N := m.eBands[j+1] - m.eBands[j]
		bits1j := C * N * int(m.allocVectors[lo*length+j]) << LM >> 2
		var bits2j int
		if hi >= m.nbAllocVectors {
			bits2j = caps[j]
		} else {
			bits2j = C * N * int(m.allocVectors[hi*length+j]) << LM >> 2
		}
		if bits1j > 0 {
			bits1j = max(0, bits1j+trimOffset[j])
		}
		if bits2j > 0 {
			bits2j = max(0, bits2j+trimOffset[j])
		}
		if lo > 0 {
			bits1j += offsets[j]
		}
		bits2j += offsets[j]
		if offsets[j] > 0 {
			skipStart = j
		}
		bits2j = max(0, bits2j-bits1j)
		bits1[j] = bits1j
		bits2[j] = bits2j
	}
	return interpBits2pulses(m, start, end, skipStart, bits1[:], bits2[:], thresh[:], caps, total, balance,
		skipRsv, intensity, intensityRsv, dualStereo, dualStereoRsv, pulses, ebits, finePriority, C, LM, ec)
}
