package resample

import (
	"math/rand/v2"
	"strconv"
	"testing"
)

// BenchmarkProcess resamples packets of 123, 960 and 5760 samples with each
// kernel the CPU supports and reports the time per output sample. 123 inputs
// make 41 outputs per call, so every call ends with a partial group.
func BenchmarkProcess(b *testing.B) {
	for _, size := range []int{123, 960, 5760} {
		in := randomSignal(rand.New(rand.NewPCG(1, 2)), size)
		out := make([]float32, size/3+256)
		saved := selected
		for _, k := range kernels() {
			selected = k
			b.Run(k.name+"/"+strconv.Itoa(size), func(b *testing.B) {
				var r Resampler48To16
				outputs := 0
				b.ReportAllocs()
				for b.Loop() {
					outputs += r.Process(in, out)
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(outputs), "ns/out")
			})
		}
		selected = saved
	}
}

// BenchmarkKernel times the filter alone on 1920 outputs.
func BenchmarkKernel(b *testing.B) {
	const n = 1920
	src := randomSignal(rand.New(rand.NewPCG(3, 4)), 3*(n-1)+nTaps)
	dst := make([]float32, n)
	for _, k := range kernels() {
		b.Run(k.name, func(b *testing.B) {
			outputs := 0
			for b.Loop() {
				k.fn(dst, src)
				outputs += n
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(outputs), "ns/out")
		})
	}
}
