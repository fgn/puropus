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
// Ported from libopus 1.5.2 celt/quant_bands.c (the decoder side).

package celt

import "github.com/fgn/puropus/opus/internal/entcode"

// eMeans is the mean energy in each band, quantized in Q4 and converted
// back to float (eMeans).
var eMeans = [25]float32{
	6.437500, 6.250000, 5.750000, 5.312500, 5.062500,
	4.812500, 4.500000, 4.375000, 4.875000, 4.687500,
	4.562500, 4.437500, 4.875000, 4.625000, 4.312500,
	4.500000, 4.375000, 4.625000, 4.750000, 4.437500,
	3.750000, 3.750000, 3.750000, 3.750000, 3.750000,
}

// Prediction coefficients of the coarse energy: 0.9, 0.8, 0.65, 0.5
// (pred_coef, beta_coef, beta_intra).
var (
	predCoef = [4]float32{29440 / 32768., 26112 / 32768., 21248 / 32768., 16384 / 32768.}
	betaCoef = [4]float32{30147 / 32768., 22282 / 32768., 12124 / 32768., 6554 / 32768.}
)

const betaIntra float32 = 4915 / 32768.

// eProbModel holds the parameters of the Laplace-like probability models of
// the coarse energy: for each frame size, prediction type (inter/intra) and
// band, the probability of 0 and the decay rate, both in Q8 (e_prob_model).
var eProbModel = [4][2][42]uint8{
	// 120 sample frames.
	{
		// Inter
		{
			72, 127, 65, 129, 66, 128, 65, 128, 64, 128, 62, 128, 64, 128,
			64, 128, 92, 78, 92, 79, 92, 78, 90, 79, 116, 41, 115, 40,
			114, 40, 132, 26, 132, 26, 145, 17, 161, 12, 176, 10, 177, 11,
		},
		// Intra
		{
			24, 179, 48, 138, 54, 135, 54, 132, 53, 134, 56, 133, 55, 132,
			55, 132, 61, 114, 70, 96, 74, 88, 75, 88, 87, 74, 89, 66,
			91, 67, 100, 59, 108, 50, 120, 40, 122, 37, 97, 43, 78, 50,
		},
	},
	// 240 sample frames.
	{
		// Inter
		{
			83, 78, 84, 81, 88, 75, 86, 74, 87, 71, 90, 73, 93, 74,
			93, 74, 109, 40, 114, 36, 117, 34, 117, 34, 143, 17, 145, 18,
			146, 19, 162, 12, 165, 10, 178, 7, 189, 6, 190, 8, 177, 9,
		},
		// Intra
		{
			23, 178, 54, 115, 63, 102, 66, 98, 69, 99, 74, 89, 71, 91,
			73, 91, 78, 89, 86, 80, 92, 66, 93, 64, 102, 59, 103, 60,
			104, 60, 117, 52, 123, 44, 138, 35, 133, 31, 97, 38, 77, 45,
		},
	},
	// 480 sample frames.
	{
		// Inter
		{
			61, 90, 93, 60, 105, 42, 107, 41, 110, 45, 116, 38, 113, 38,
			112, 38, 124, 26, 132, 27, 136, 19, 140, 20, 155, 14, 159, 16,
			158, 18, 170, 13, 177, 10, 187, 8, 192, 6, 175, 9, 159, 10,
		},
		// Intra
		{
			21, 178, 59, 110, 71, 86, 75, 85, 84, 83, 91, 66, 88, 73,
			87, 72, 92, 75, 98, 72, 105, 58, 107, 54, 115, 52, 114, 55,
			112, 56, 129, 51, 132, 40, 150, 33, 140, 29, 98, 35, 77, 42,
		},
	},
	// 960 sample frames.
	{
		// Inter
		{
			42, 121, 96, 66, 108, 43, 111, 40, 117, 44, 123, 32, 120, 36,
			119, 33, 127, 33, 134, 34, 139, 21, 147, 23, 152, 20, 158, 25,
			154, 26, 166, 21, 173, 16, 184, 13, 184, 10, 150, 13, 139, 15,
		},
		// Intra
		{
			22, 178, 63, 114, 74, 82, 84, 83, 92, 82, 103, 62, 96, 72,
			96, 67, 101, 73, 107, 72, 113, 55, 118, 52, 125, 52, 118, 52,
			117, 55, 135, 49, 137, 39, 157, 32, 145, 29, 97, 33, 77, 40,
		},
	},
}

// smallEnergyICDF codes the coarse energy when few bits are left
// (small_energy_icdf).
var smallEnergyICDF = [3]uint8{2, 1, 0}

// unquantCoarseEnergy decodes the coarse band energies of the bands from
// start to end into oldEBands (in log2 units), predicting from the previous
// frame unless intra (unquant_coarse_energy, float build).
func unquantCoarseEnergy(m *Mode, start, end int, oldEBands []float32, intra bool, dec *entcode.Decoder, C, LM int) {
	probModel := &eProbModel[LM][0]
	var prev [2]float32
	var coef, beta float32

	if intra {
		probModel = &eProbModel[LM][1]
		coef = 0
		beta = betaIntra
	} else {
		beta = betaCoef[LM]
		coef = predCoef[LM]
	}

	budget := dec.Storage() * 8

	// Decode at a fixed coarse resolution.
	for i := start; i < end; i++ {
		for c := 0; c < C; c++ {
			var qi int
			tell := dec.Tell()
			switch {
			case budget-tell >= 15:
				pi := 2 * min(i, 20)
				qi = ecLaplaceDecode(dec, uint32(probModel[pi])<<7, int(probModel[pi+1])<<6)
			case budget-tell >= 2:
				qi = dec.DecICDF(smallEnergyICDF[:], 2)
				qi = (qi >> 1) ^ -(qi & 1)
			case budget-tell >= 1:
				qi = -dec.DecBitLogp(1)
			default:
				qi = -1
			}
			q := float32(qi)

			idx := i + c*m.nbEBands
			oldEBands[idx] = max16(-9, oldEBands[idx])
			tmp := float32(coef*oldEBands[idx]) + prev[c] + q
			oldEBands[idx] = tmp
			prev[c] = prev[c] + q - float32(beta*q)
		}
	}
}

// unquantFineEnergy decodes the fine energy bits of each band
// (unquant_fine_energy, float build).
func unquantFineEnergy(m *Mode, start, end int, oldEBands []float32, fineQuant []int, dec *entcode.Decoder, C int) {
	// Decode finer resolution.
	for i := start; i < end; i++ {
		if fineQuant[i] <= 0 {
			continue
		}
		for c := 0; c < C; c++ {
			q2 := dec.DecBits(uint(fineQuant[i]))
			offset := float32((float32(q2)+0.5)*float32(int(1)<<(14-fineQuant[i]))*(1.0/16384)) - 0.5
			oldEBands[i+c*m.nbEBands] += offset
		}
	}
}

// unquantEnergyFinalise uses up the bitsLeft remaining bits on one more
// fine energy bit per band and channel, by priority
// (unquant_energy_finalise, float build).
func unquantEnergyFinalise(m *Mode, start, end int, oldEBands []float32, fineQuant, finePriority []int,
	bitsLeft int, dec *entcode.Decoder, C int,
) {
	// Use up the remaining bits.
	for prio := 0; prio < 2; prio++ {
		for i := start; i < end && bitsLeft >= C; i++ {
			if fineQuant[i] >= maxFineBits || finePriority[i] != prio {
				continue
			}
			for c := 0; c < C; c++ {
				q2 := dec.DecBits(1)
				offset := float32((float32(q2) - 0.5) * float32(int(1)<<(14-fineQuant[i]-1)) * (1.0 / 16384))
				oldEBands[i+c*m.nbEBands] += offset
				bitsLeft--
			}
		}
	}
}
