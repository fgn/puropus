//go:build goexperiment.simd && amd64

package celt

import (
	"math"
	"math/rand"
	"testing"
)

// specialFloats are values whose signs and rounding a reordered kernel would
// get wrong (signed zeros, subnormals, large values; none large enough to
// overflow to infinity, whose NaNs the two orders would sign differently).
var specialFloats = []float32{0, float32(math.Copysign(0, -1)), 1, -1, 1e-38, -1e-38, 1e-45, 1e30, -1e30}

// randomSignal fills x with Gaussian values of varied scale and some special
// values.
func randomSignal(rng *rand.Rand, x []float32) {
	scale := float32(math.Pow(10, float64(rng.Intn(9)-4)))
	for i := range x {
		if rng.Intn(16) == 0 {
			x[i] = specialFloats[rng.Intn(len(specialFloats))]
			continue
		}
		x[i] = scale * float32(rng.NormFloat64())
	}
}

func TestFFTAVX2MatchesScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(4))
	for k := range fftState48000x960 {
		st := &fftState48000x960[k]
		n := 2 * st.nfft
		want := make([]float32, n)
		got := make([]float32, n)
		for iter := 0; iter < 2000; iter++ {
			randomSignal(rng, want)
			copy(got, want)
			opusFFTImpl(&fftConfigs[k], want)
			opusFFTAVX2(&simdFFTs[k], got)
			if i := sameBits(got, want); i >= 0 {
				t.Fatalf("nfft %d: out[%d] = %g, want %g", st.nfft, i, got[i], want[i])
			}
		}
	}
}

func TestMDCTBackwardAVX2MatchesScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	m := StaticMode()
	rng := rand.New(rand.NewSource(5))
	for shift := 0; shift <= m.mdct.maxshift; shift++ {
		N := m.mdct.n >> shift
		in := make([]float32, N/2)
		want := make([]float32, N/2+m.overlap)
		got := make([]float32, len(want))
		for iter := 0; iter < 2000; iter++ {
			randomSignal(rng, in)
			randomSignal(rng, want)
			copy(got, want)
			useAVX2 = false
			m.mdct.cltMdctBackward(in, want, m.window, m.overlap, shift, 1)
			useAVX2 = true
			m.mdct.cltMdctBackward(in, got, m.window, m.overlap, shift, 1)
			if i := sameBits(got, want); i >= 0 {
				t.Fatalf("shift %d: out[%d] = %g, want %g", shift, i, got[i], want[i])
			}
		}
	}
}

func TestMDCTBackwardAVX2ShortBlocksMatchScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	m := StaticMode()
	rng := rand.New(rand.NewSource(6))
	for LM := 1; LM <= m.maxLM; LM++ {
		B := 1 << LM
		N := m.shortMdctSize << LM
		in := make([]float32, N)
		want := make([]float32, N+m.overlap)
		got := make([]float32, len(want))
		for iter := 0; iter < 500; iter++ {
			randomSignal(rng, in)
			randomSignal(rng, want)
			copy(got, want)
			for _, simd := range []bool{false, true} {
				out := want
				if simd {
					out = got
				}
				useAVX2 = simd
				for b := 0; b < B; b++ {
					m.mdct.cltMdctBackward(in[b:], out[m.shortMdctSize*b:], m.window, m.overlap, m.maxLM, B)
				}
			}
			if i := sameBits(got, want); i >= 0 {
				t.Fatalf("LM %d: out[%d] = %g, want %g", LM, i, got[i], want[i])
			}
		}
	}
}

func TestDenormaliseAVX2MatchesScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	m := StaticMode()
	rng := rand.New(rand.NewSource(10))
	const N = maxFrameSize
	X := make([]float32, N)
	want := make([]float32, N)
	got := make([]float32, N)
	var logE [maxBands]float32
	for iter := 0; iter < 5000; iter++ {
		randomSignal(rng, X)
		for i := range logE {
			logE[i] = float32(rng.NormFloat64() * 10)
		}
		start := rng.Intn(maxBands)
		end := start + 1 + rng.Intn(maxBands-start)
		downsample := []int{1, 1, 2, 3, 6}[rng.Intn(5)]
		silence := rng.Intn(8) == 0
		randomSignal(rng, want)
		copy(got, want)
		useAVX2 = false
		denormaliseBands(m, X, want, logE[:], start, end, 8, downsample, silence)
		useAVX2 = true
		denormaliseBands(m, X, got, logE[:], start, end, 8, downsample, silence)
		if i := sameBits(got, want); i >= 0 {
			t.Fatalf("bands %d..%d downsample %d silence %v: freq[%d] = %g, want %g", start, end, downsample, silence, i, got[i], want[i])
		}
	}
}

func TestHaar1AVX2MatchesScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(12))
	var got, want [maxBandSize]float32
	for iter := 0; iter < 20000; iter++ {
		stride := 1 + rng.Intn(2)
		N0 := 2 * (1 + rng.Intn(maxBandSize/stride/2))
		N := N0 * stride
		randomSignal(rng, want[:N])
		copy(got[:N], want[:N])
		useAVX2 = false
		haar1(want[:N], N0, stride)
		useAVX2 = true
		haar1(got[:N], N0, stride)
		if i := sameBits(got[:N], want[:N]); i >= 0 {
			t.Fatalf("N0=%d stride=%d: X[%d] = %g, want %g", N0, stride, i, got[i], want[i])
		}
	}
}

func TestScaleAVX2MatchesScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(13))
	src := make([]float32, maxBandSize)
	dst := make([]float32, maxBandSize)
	for iter := 0; iter < 5000; iter++ {
		n := rng.Intn(maxBandSize + 1)
		randomSignal(rng, src[:n])
		g := float32(rng.NormFloat64())
		scaleAVX2(dst[:n], src[:n], g)
		for i := range dst[:n] {
			if want := g * src[i]; math.Float32bits(dst[i]) != math.Float32bits(want) {
				t.Fatalf("n=%d: dst[%d] = %g, want %g", n, i, dst[i], want)
			}
		}
	}
}

func TestNormaliseAVX2MatchesScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("no AVX2")
	}
	rng := rand.New(rand.NewSource(14))
	var iy [maxBandSize]int32
	X := make([]float32, maxBandSize)
	for iter := 0; iter < 5000; iter++ {
		n := rng.Intn(maxBandSize + 1)
		for i := range iy[:n] {
			iy[i] = int32(rng.Intn(2*maxBandSize+1) - maxBandSize)
		}
		g := float32(rng.NormFloat64())
		normaliseAVX2(X[:n], iy[:n], g)
		for i := range X[:n] {
			if want := g * float32(iy[i]); math.Float32bits(X[i]) != math.Float32bits(want) {
				t.Fatalf("n=%d: X[%d] = %g, want %g", n, i, X[i], want)
			}
		}
	}
}
