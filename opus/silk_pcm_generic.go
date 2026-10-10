//go:build !(goexperiment.simd && amd64)

package opus

func silkToFloat(pcm []float32, x []int16) { silkToFloatScalar(pcm, x) }

func addSilk(pcm []float32, x []int16) { addSilkScalar(pcm, x) }
