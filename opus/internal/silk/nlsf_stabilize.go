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
// Ported from libopus 1.5.2 silk/NLSF_stabilize.c and silk/sort.c (silk_insertion_sort_increasing_all_values_int16).

package silk

// NLSF stabilizer:
//
//   - Moves NLSFs further apart if they are too close
//   - Moves NLSFs away from borders if they are too close
//   - High effort to achieve a modification with minimum Euclidean distance
//     to input vector
//   - Output are sorted NLSF coefficients

// nlsfStabilizeMaxLoops is MAX_LOOPS in NLSF_stabilize.c.
const nlsfStabilizeMaxLoops = 20

// nlsfStabilize is silk_NLSF_stabilize: it enforces the minimum distances
// nDeltaMinQ15 (l+1 values, the last one at least 1) between the l (L) NLSFs of
// nlsfQ15 and the borders.
func nlsfStabilize(nlsfQ15 []int16, nDeltaMinQ15 []int16, l int) {
	var (
		I     int
		loops int
	)

	for loops = 0; loops < nlsfStabilizeMaxLoops; loops++ {
		// Find smallest distance.
		// First element.
		minDiffQ15 := int32(nlsfQ15[0]) - int32(nDeltaMinQ15[0])
		I = 0
		// Middle elements.
		for i := 1; i <= l-1; i++ {
			diffQ15 := int32(nlsfQ15[i]) - (int32(nlsfQ15[i-1]) + int32(nDeltaMinQ15[i]))
			if diffQ15 < minDiffQ15 {
				minDiffQ15 = diffQ15
				I = i
			}
		}
		// Last element.
		diffQ15 := 1<<15 - (int32(nlsfQ15[l-1]) + int32(nDeltaMinQ15[l]))
		if diffQ15 < minDiffQ15 {
			minDiffQ15 = diffQ15
			I = l
		}

		// Now check if the smallest distance is non-negative.
		if minDiffQ15 >= 0 {
			return
		}

		switch I {
		case 0:
			// Move away from lower limit.
			nlsfQ15[0] = nDeltaMinQ15[0]
		case l:
			// Move away from higher limit.
			nlsfQ15[l-1] = int16(1<<15 - int32(nDeltaMinQ15[l]))
		default:
			// Find the lower extreme for the location of the current center
			// frequency.
			minCenterQ15 := int32(0)
			for k := range I {
				minCenterQ15 += int32(nDeltaMinQ15[k])
			}
			minCenterQ15 += int32(nDeltaMinQ15[I]) >> 1

			// Find the upper extreme for the location of the current center
			// frequency.
			maxCenterQ15 := int32(1 << 15)
			for k := l; k > I; k-- {
				maxCenterQ15 -= int32(nDeltaMinQ15[k])
			}
			maxCenterQ15 -= int32(nDeltaMinQ15[I]) >> 1

			// Move apart, sorted by value, keeping the same center
			// frequency.
			centerFreqQ15 := int16(limit(rshiftRound(int32(nlsfQ15[I-1])+int32(nlsfQ15[I]), 1),
				minCenterQ15, maxCenterQ15))
			nlsfQ15[I-1] = int16(int32(centerFreqQ15) - int32(nDeltaMinQ15[I])>>1)
			nlsfQ15[I] = int16(int32(nlsfQ15[I-1]) + int32(nDeltaMinQ15[I]))
		}
	}

	// Safe and simple fall back method, which is less ideal than the above.
	if loops == nlsfStabilizeMaxLoops {
		// Insertion sort (fast for already almost sorted arrays).
		insertionSortIncreasingAllValuesInt16(nlsfQ15[:l])

		// First NLSF should be no less than NDeltaMin[0].
		nlsfQ15[0] = max(nlsfQ15[0], nDeltaMinQ15[0])

		// Keep delta_min distance between the NLSFs.
		for i := 1; i < l; i++ {
			nlsfQ15[i] = max(nlsfQ15[i], sat16(int32(nlsfQ15[i-1])+int32(nDeltaMinQ15[i])))
		}

		// Last NLSF should be no higher than 1 - NDeltaMin[l].
		nlsfQ15[l-1] = int16(min(int32(nlsfQ15[l-1]), 1<<15-int32(nDeltaMinQ15[l])))

		// Keep NDeltaMin distance between the NLSFs.
		for i := l - 2; i >= 0; i-- {
			nlsfQ15[i] = int16(min(int32(nlsfQ15[i]), int32(nlsfQ15[i+1])-int32(nDeltaMinQ15[i+1])))
		}
	}
}

// insertionSortIncreasingAllValuesInt16 is
// silk_insertion_sort_increasing_all_values_int16 (sort.c): it sorts a in
// increasing order.
func insertionSortIncreasingAllValuesInt16(a []int16) {
	for i := 1; i < len(a); i++ {
		value := a[i]
		j := i - 1
		for ; j >= 0 && value < a[j]; j-- {
			a[j+1] = a[j] // Shift value.
		}
		a[j+1] = value // Write value.
	}
}
