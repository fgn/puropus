package resample

// kernel is one implementation of the filter: it sets dst[k] to the dot
// product of taps with src[3k : 3k+nTaps].
type kernel struct {
	name string
	fn   func(dst, src []float32)
	// exact reports that fn returns the same bits as filterScalar. Kernels
	// that fuse multiply and add round differently.
	exact bool
}

// selected is the fastest kernel the CPU supports, chosen once at start-up.
var selected = kernels()[0]

// filterBlock runs the selected kernel.
func filterBlock(dst, src []float32) { selected.fn(dst, src) }

// Acceleration reports the filter kernel selected for this CPU: "scalar",
// "simd-amd64-avx512", "simd-amd64-avx2-fma", "simd-amd64-avx2" or
// "simd-amd64-avx". Builds without GOEXPERIMENT=simd, and other
// architectures, always use "scalar".
func Acceleration() string { return selected.name }
