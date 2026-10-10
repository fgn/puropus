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
// Ported from libopus 1.5.2 silk/NLSF2A.c.

package silk

// Conversion between prediction filter coefficients and LSFs. The order
// should be even. A piecewise linear approximation maps LSF <-> cos(LSF), so
// the result is not accurate LSFs, but the two functions are accurate
// inverses of each other.

const (
	// silkMaxOrderLPC is SILK_MAX_ORDER_LPC.
	silkMaxOrderLPC = 24
	// nlsf2AQA is QA in NLSF2A.c.
	nlsf2AQA = 16
)

// nlsf2AFindPoly is silk_NLSF2A_find_poly: the intermediate polynomial (QA,
// dd+1 values) of the interleaved 2*cos(LSF) values cLSF[0], cLSF[2], ....
func nlsf2AFindPoly(out []int32, cLSF []int32, dd int) {
	out[0] = 1 << nlsf2AQA
	out[1] = -cLSF[0]
	for k := 1; k < dd; k++ {
		ftmp := cLSF[2*k] // QA
		out[k+1] = out[k-1]<<1 - int32(rshiftRound64(smull(ftmp, out[k]), nlsf2AQA))
		for n := k; n > 1; n-- {
			out[n] += out[n-2] - int32(rshiftRound64(smull(ftmp, out[n-1]), nlsf2AQA))
		}
		out[1] -= ftmp
	}
}

// These orderings were found to maximize quality. They improve the numerical
// accuracy of nlsf2AFindPoly compared to the "standard" ordering.
var (
	nlsf2AOrdering16 = [16]uint8{0, 15, 8, 7, 4, 11, 12, 3, 2, 13, 10, 5, 6, 9, 14, 1}
	nlsf2AOrdering10 = [10]uint8{0, 9, 6, 3, 4, 5, 8, 1, 2, 7}
)

// nlsf2A is silk_NLSF2A: the monic whitening filter coefficients in Q12 of
// the normalized line spectral frequencies nlsf (Q15) of order d (10 or 16).
func nlsf2A(aQ12 []int16, nlsf []int16, d int) {
	var (
		cosLSFQA [silkMaxOrderLPC]int32
		p, q     [silkMaxOrderLPC/2 + 1]int32
		a32QA1   [silkMaxOrderLPC]int32
	)

	// Convert LSFs to 2*cos(LSF), using a piecewise linear curve from a
	// table.
	ordering := nlsf2AOrdering10[:]
	if d == 16 {
		ordering = nlsf2AOrdering16[:]
	}
	for k := range d {
		// fInt on a scale 0-127 (rounded down).
		fInt := int32(nlsf[k]) >> (15 - 7)

		// fFrac, range: 0..255.
		fFrac := int32(nlsf[k]) - fInt<<(15-7)

		// Read start and end value from table.
		cosVal := int32(lsfCosTabFIXQ12[fInt])           // Q12
		delta := int32(lsfCosTabFIXQ12[fInt+1]) - cosVal // Q12, with a range of 0..200

		// Linear interpolation.
		cosLSFQA[ordering[k]] = rshiftRound(cosVal<<8+delta*fFrac, 20-nlsf2AQA) // QA
	}

	dd := d >> 1

	// Generate even and odd polynomials using convolution.
	nlsf2AFindPoly(p[:], cosLSFQA[0:], dd)
	nlsf2AFindPoly(q[:], cosLSFQA[1:], dd)

	// Convert even and odd polynomials to opus_int32 Q12 filter coefs.
	for k := range dd {
		ptmp := p[k+1] + p[k]
		qtmp := q[k+1] - q[k]

		// The ptmp and qtmp values at this stage need to fit in int32.
		a32QA1[k] = -qtmp - ptmp    // QA+1
		a32QA1[d-k-1] = qtmp - ptmp // QA+1
	}

	// Convert int32 coefficients to Q12 int16 coefs.
	lpcFit(aQ12, a32QA1[:], 12, nlsf2AQA+1, d)

	for i := 0; lpcInversePredGain(aQ12, d) == 0 && i < maxLPCStabilizeIterations; i++ {
		// Prediction coefficients are (too close to) unstable; apply
		// bandwidth expansion on the unscaled coefficients, convert to Q12
		// and measure again.
		bwexpander32(a32QA1[:], d, 65536-int32(2)<<i)
		for k := range d {
			aQ12[k] = int16(rshiftRound(a32QA1[k], nlsf2AQA+1-12)) // QA+1 -> Q12
		}
	}
}
