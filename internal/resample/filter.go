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
func filterScalar(dst, src []float32) {
	for k := range dst {
		w := (*[nTaps]float32)(src[3*k:])
		var s0, s1, s2, s3, s4, s5, s6, s7 float32
		for i := 0; i < vecTaps; i += 8 {
			x := (*[8]float32)(w[i:])
			t := (*[8]float32)(taps[i:])
			s0 += float32(x[0] * t[0])
			s1 += float32(x[1] * t[1])
			s2 += float32(x[2] * t[2])
			s3 += float32(x[3] * t[3])
			s4 += float32(x[4] * t[4])
			s5 += float32(x[5] * t[5])
			s6 += float32(x[6] * t[6])
			s7 += float32(x[7] * t[7])
		}
		s0 += float32(w[vecTaps] * taps[vecTaps])
		s1 += float32(w[vecTaps+1] * taps[vecTaps+1])
		s2 += float32(w[vecTaps+2] * taps[vecTaps+2])
		s3 += float32(w[vecTaps+3] * taps[vecTaps+3])
		dst[k] = ((s0 + s4) + (s2 + s6)) + ((s1 + s5) + (s3 + s7))
	}
}
