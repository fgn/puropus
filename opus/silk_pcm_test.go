package opus

import (
	"math"
	"math/rand/v2"
	"testing"
)

// silkPCMInput returns SILK output samples of every magnitude, the
// extremes and zero included.
func silkPCMInput(r *rand.Rand, n int) []int16 {
	x := make([]int16, n)
	for i := range x {
		switch r.IntN(3) {
		case 0:
			x[i] = int16(r.Uint32())
		case 1:
			x[i] = []int16{-32768, -32767, -1, 0, 1, 32767}[r.IntN(6)]
		default:
			x[i] = int16(r.IntN(201) - 100)
		}
	}
	return x
}

// TestSilkPCMMatchesCLoop checks the SILK output conversion, in whatever
// form the build dispatches to, against opus_decode_frame's loop on zeroed
// and on CELT output, bit for bit.
func TestSilkPCMMatchesCLoop(t *testing.T) {
	r := rand.New(rand.NewPCG(23, 24))
	for n := range 2*960 + 9 {
		x := silkPCMInput(r, n)
		celt := make([]float32, n)
		for i := range celt {
			celt[i] = float32(r.NormFloat64() * 0.3)
		}
		want := make([]float32, n)
		got := make([]float32, n)
		// The zeroed output of a SILK-only frame.
		for i := range want {
			want[i] = 0
			want[i] = want[i] + float32((1.0/32768)*float32(x[i]))
		}
		silkToFloat(got, x)
		for i := range got {
			if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
				t.Fatalf("silkToFloat n %d sample %d: %v, want %v", n, i, got[i], want[i])
			}
		}
		for i := range want {
			want[i] = celt[i] + float32((1.0/32768)*float32(x[i]))
		}
		copy(got, celt)
		addSilk(got, x)
		for i := range got {
			if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
				t.Fatalf("addSilk n %d sample %d: %v, want %v", n, i, got[i], want[i])
			}
		}
	}
}
