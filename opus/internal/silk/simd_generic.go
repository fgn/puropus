//go:build !(goexperiment.simd && amd64)

package silk

// The stages with vector kernels (simd_amd64.go) run their scalar code in
// this build.

// clearUpperState has nothing to clear in this build.
func clearUpperState() {}

func interpolFIR3Over2(out []int16, buf []int16, n int) int {
	return interpolFIR3Over2Scalar(out, buf, n)
}

func interpolFIR2(out []int16, buf []int16, n int) int {
	return interpolFIR2Scalar(out, buf, n)
}

func interpolFIR3(out []int16, buf []int16, n int) int {
	return interpolFIR3Scalar(out, buf, n)
}

func resamplerPrivateUp2HQ(s *[resamplerMaxIIROrder]int32, out []int16, in []int16, length int) {
	resamplerPrivateUp2HQScalar(s, out, in, length)
}

func interleave(dst, left, right []int16) { interleaveScalar(dst, left, right) }

func msToLR(mid, side []int16) { msToLRScalar(mid, side) }
