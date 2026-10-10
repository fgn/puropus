package resample

import "math"

// The anti-aliasing filter is a windowed-sinc low-pass FIR. With the
// distance d = 3m - p (in input samples) between the centre of output m and
// input sample p, its taps are
//
//	h(d) = c · sinc(fc·d) · I0(β·sqrt(1 - (d/halfWidth)²)),   -50 <= d <= 49,
//
// where sinc(x) = sin(πx)/(πx), I0 is the zeroth-order modified Bessel
// function of the first kind (the Kaiser window, β = 9), fc = 0.97/3 puts the
// half-amplitude point at 97% of the 8 kHz output Nyquist frequency, and c
// scales the 100 taps to sum to 1 (unit gain at DC). The window spans
// halfWidth = 50 input samples on either side; its end at d = +50 falls
// outside the 100 taps, so h is symmetric apart from the window-edge tap
// h(-50). The taps are designed in float64 and rounded to float32. Every tap
// lies more than 10^6 float64 ulps from a float32 rounding boundary, so
// last-bit differences between platforms in the float64 design (fused
// multiply-adds, math.Sin implementations) cannot change the float32 taps.
const (
	nTaps        = preTaps + postTaps + 1
	vecTaps      = nTaps &^ 7 // taps in whole groups of eight; four remain
	halfWidth    = 50.0
	cutoff       = 0.97
	decimation   = 3
	kaiserBeta   = 9.0
	besselMaxErr = 1e-17
)

// taps[i] weights input sample 3m - preTaps + i of output m, so it is
// h(preTaps - i).
var taps = designTaps()

func designTaps() [nTaps]float32 {
	var h [nTaps]float64
	sum := 0.0
	fc := cutoff / decimation
	for i := range h {
		d := float64(preTaps - i)
		x := d / halfWidth
		h[i] = sinc(fc*d) * besselI0(kaiserBeta*math.Sqrt(1-x*x))
		sum += h[i]
	}
	var t [nTaps]float32
	for i := range h {
		t[i] = float32(h[i] / sum)
	}
	return t
}

// sinc returns sin(πx)/(πx).
func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// besselI0 evaluates the modified Bessel function of the first kind of order
// zero by its power series, I0(x) = Σ ((x/2)^k / k!)², which converges for
// every x; the terms decay once k exceeds x/2.
func besselI0(x float64) float64 {
	q := x * x / 4
	sum, term := 1.0, 1.0
	for k := 1.0; ; k++ {
		term *= q / (k * k)
		sum += term
		if term <= sum*besselMaxErr {
			return sum
		}
	}
}

// filterScalar sets dst[k] to the dot product of taps with
// src[3k : 3k+nTaps]. It keeps eight partial sums, one per tap index modulo
// 8, and adds them pairwise as ((s0+s4)+(s2+s6)) + ((s1+s5)+(s3+s7)); the
// vector kernels that do not fuse multiply and add follow the same order and
// produce identical results. Each product is rounded before it is added.
//
// The taps are written out, eight to a group, each sum taking its taps in
// ascending order as the loop in filterLoop (in the tests) does. Go does not
// unroll loops; written out, the filter takes 18% less time.
func filterScalar(dst, src []float32) {
	for k := range dst {
		w := (*[nTaps]float32)(src[3*k:])
		var s0, s1, s2, s3, s4, s5, s6, s7 float32
		s0 += float32(w[0] * taps[0])
		s1 += float32(w[1] * taps[1])
		s2 += float32(w[2] * taps[2])
		s3 += float32(w[3] * taps[3])
		s4 += float32(w[4] * taps[4])
		s5 += float32(w[5] * taps[5])
		s6 += float32(w[6] * taps[6])
		s7 += float32(w[7] * taps[7])

		s0 += float32(w[8] * taps[8])
		s1 += float32(w[9] * taps[9])
		s2 += float32(w[10] * taps[10])
		s3 += float32(w[11] * taps[11])
		s4 += float32(w[12] * taps[12])
		s5 += float32(w[13] * taps[13])
		s6 += float32(w[14] * taps[14])
		s7 += float32(w[15] * taps[15])

		s0 += float32(w[16] * taps[16])
		s1 += float32(w[17] * taps[17])
		s2 += float32(w[18] * taps[18])
		s3 += float32(w[19] * taps[19])
		s4 += float32(w[20] * taps[20])
		s5 += float32(w[21] * taps[21])
		s6 += float32(w[22] * taps[22])
		s7 += float32(w[23] * taps[23])

		s0 += float32(w[24] * taps[24])
		s1 += float32(w[25] * taps[25])
		s2 += float32(w[26] * taps[26])
		s3 += float32(w[27] * taps[27])
		s4 += float32(w[28] * taps[28])
		s5 += float32(w[29] * taps[29])
		s6 += float32(w[30] * taps[30])
		s7 += float32(w[31] * taps[31])

		s0 += float32(w[32] * taps[32])
		s1 += float32(w[33] * taps[33])
		s2 += float32(w[34] * taps[34])
		s3 += float32(w[35] * taps[35])
		s4 += float32(w[36] * taps[36])
		s5 += float32(w[37] * taps[37])
		s6 += float32(w[38] * taps[38])
		s7 += float32(w[39] * taps[39])

		s0 += float32(w[40] * taps[40])
		s1 += float32(w[41] * taps[41])
		s2 += float32(w[42] * taps[42])
		s3 += float32(w[43] * taps[43])
		s4 += float32(w[44] * taps[44])
		s5 += float32(w[45] * taps[45])
		s6 += float32(w[46] * taps[46])
		s7 += float32(w[47] * taps[47])

		s0 += float32(w[48] * taps[48])
		s1 += float32(w[49] * taps[49])
		s2 += float32(w[50] * taps[50])
		s3 += float32(w[51] * taps[51])
		s4 += float32(w[52] * taps[52])
		s5 += float32(w[53] * taps[53])
		s6 += float32(w[54] * taps[54])
		s7 += float32(w[55] * taps[55])

		s0 += float32(w[56] * taps[56])
		s1 += float32(w[57] * taps[57])
		s2 += float32(w[58] * taps[58])
		s3 += float32(w[59] * taps[59])
		s4 += float32(w[60] * taps[60])
		s5 += float32(w[61] * taps[61])
		s6 += float32(w[62] * taps[62])
		s7 += float32(w[63] * taps[63])

		s0 += float32(w[64] * taps[64])
		s1 += float32(w[65] * taps[65])
		s2 += float32(w[66] * taps[66])
		s3 += float32(w[67] * taps[67])
		s4 += float32(w[68] * taps[68])
		s5 += float32(w[69] * taps[69])
		s6 += float32(w[70] * taps[70])
		s7 += float32(w[71] * taps[71])

		s0 += float32(w[72] * taps[72])
		s1 += float32(w[73] * taps[73])
		s2 += float32(w[74] * taps[74])
		s3 += float32(w[75] * taps[75])
		s4 += float32(w[76] * taps[76])
		s5 += float32(w[77] * taps[77])
		s6 += float32(w[78] * taps[78])
		s7 += float32(w[79] * taps[79])

		s0 += float32(w[80] * taps[80])
		s1 += float32(w[81] * taps[81])
		s2 += float32(w[82] * taps[82])
		s3 += float32(w[83] * taps[83])
		s4 += float32(w[84] * taps[84])
		s5 += float32(w[85] * taps[85])
		s6 += float32(w[86] * taps[86])
		s7 += float32(w[87] * taps[87])

		s0 += float32(w[88] * taps[88])
		s1 += float32(w[89] * taps[89])
		s2 += float32(w[90] * taps[90])
		s3 += float32(w[91] * taps[91])
		s4 += float32(w[92] * taps[92])
		s5 += float32(w[93] * taps[93])
		s6 += float32(w[94] * taps[94])
		s7 += float32(w[95] * taps[95])

		s0 += float32(w[96] * taps[96])
		s1 += float32(w[97] * taps[97])
		s2 += float32(w[98] * taps[98])
		s3 += float32(w[99] * taps[99])
		dst[k] = ((s0 + s4) + (s2 + s6)) + ((s1 + s5) + (s3 + s7))
	}
}
