package resample

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/rand/v2"
	"testing"
)

// Gates against the reference: RMS and peak sample error.
const (
	gateRMS  = 1e-6
	gatePeak = 1e-4
)

func f32le(x []float32) []byte {
	b := make([]byte, 4*len(x))
	for i, v := range x {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}

func floats(t testing.TB, b []byte) []float32 {
	t.Helper()
	if len(b)%4 != 0 {
		t.Fatalf("%d bytes is not whole float32 samples", len(b))
	}
	x := make([]float32, len(b)/4)
	for i := range x {
		x[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return x
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type gateResult struct{ rms, peak float64 }

func (g gateResult) max(o gateResult) gateResult {
	return gateResult{max(g.rms, o.rms), max(g.peak, o.peak)}
}

// gate fails t unless got has want's length and lies within the RMS and peak
// gates of it, and returns the errors.
func gate(t testing.TB, name string, got, want []float32) gateResult {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d samples, want %d", name, len(got), len(want))
		return gateResult{}
	}
	var g gateResult
	sum := 0.0
	for i := range got {
		d := math.Abs(float64(got[i]) - float64(want[i]))
		sum += d * d
		g.peak = max(g.peak, d)
	}
	if len(got) > 0 {
		g.rms = math.Sqrt(sum / float64(len(got)))
	}
	if g.rms > gateRMS || g.peak > gatePeak || math.IsNaN(sum) {
		t.Errorf("%s: RMS error %.3g, peak %.3g (gates %g, %g)", name, g.rms, g.peak, gateRMS, gatePeak)
	}
	return g
}

// resampleChunks runs x through a new resampler in chunks of the given sizes
// (the last one repeating), with output buffers sized as the decoder sizes
// them, and flushes it.
func resampleChunks(x []float32, sizes []int) []float32 {
	var r Resampler48To16
	var y []float32
	for i := 0; len(x) > 0; i++ {
		n := min(sizes[min(i, len(sizes)-1)], len(x))
		out := make([]float32, n/3+256)
		y = append(y, out[:r.Process(x[:n], out)]...)
		x = x[n:]
	}
	return append(y, flushAll(&r, 256)...)
}

// flushAll calls Flush with buffers of size n until it returns 0.
func flushAll(r *Resampler48To16, n int) []float32 {
	var y []float32
	out := make([]float32, n)
	for {
		k := r.Flush(out)
		if k == 0 {
			return y
		}
		y = append(y, out[:k]...)
	}
}

// randomChunks returns chunk sizes in [1, maxSize] that cover n samples.
func randomChunks(rng *rand.Rand, n, maxSize int) []int {
	var sizes []int
	for n > 0 {
		s := min(1+rng.IntN(maxSize), n)
		sizes = append(sizes, s)
		n -= s
	}
	return append(sizes, 1)
}
