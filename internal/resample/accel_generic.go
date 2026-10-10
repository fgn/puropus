//go:build !(goexperiment.simd && amd64)

package resample

// kernels lists the filter kernels this CPU can run, preferred first. This
// build has only the scalar one.
func kernels() []kernel {
	return []kernel{{name: "scalar", fn: filterScalar, exact: true}}
}
