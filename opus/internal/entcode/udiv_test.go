package entcode

import (
	"math"
	"math/bits"
	"math/rand/v2"
	"testing"
)

// TestUdivRecip checks the reciprocals against division for every divisor,
// at the edges of each quotient and on random dividends.
func TestUdivRecip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for n := uint32(2); n <= 256; n++ {
		check := func(x uint32) {
			if q, _ := bits.Mul64(uint64(x), udivRecip[n]); uint32(q) != x/n {
				t.Fatalf("%d/%d: %d, want %d", x, n, q, x/n)
			}
		}
		for _, x := range []uint32{0, 1, n - 1, n, n + 1, 1 << 31, 1<<31 + 1, math.MaxUint32, math.MaxUint32 - 1} {
			check(x)
		}
		for range 20000 {
			q := r.Uint32() / n
			check(q * n)
			check(q*n + n - 1)
			if q > 0 {
				check(q*n - 1)
			}
			check(r.Uint32())
		}
	}
}
