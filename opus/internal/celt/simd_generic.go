//go:build !(goexperiment.simd && amd64)

package celt

// Builds without GOEXPERIMENT=simd, and other architectures, have no vector
// kernels: the dispatch tests below are constant and the calls behind them
// are compiled out.
const useAVX2 = false

// buildSIMDTables does nothing: this build has no vector kernels.
func buildSIMDTables() {}

// Acceleration reports the vector kernels the decoder selected: always
// "scalar" in this build.
func Acceleration() string { return "scalar" }

func opusFFTAVX2(*simdFFTState, []float32) { panic("unreachable") }

func postfilterDeemphasisStereoAVX2([]float32, []float32, int, []combSpan, []float32, []float32, float32, []float32) {
	panic("unreachable")
}

func scaleAVX2([]float32, []float32, float32) { panic("unreachable") }

func normaliseAVX2([]float32, []int32, float32) { panic("unreachable") }

func haar1AVX2([]float32, int) { panic("unreachable") }

func denormaliseAVX2([]float32, []float32, *[maxBands]float32, []int, int, int) { panic("unreachable") }

func mdctBackwardAVX2(*mdctLookup, []float32, []float32, []float32, int) { panic("unreachable") }

// simdFFTState is the vector FFT's configuration, empty in this build.
type simdFFTState struct{}

var simdFFTs [4]simdFFTState
