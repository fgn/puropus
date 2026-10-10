package resample

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// reference resamples x by the definition in the package documentation, in
// float64 with explicit mirroring at both ends.
func reference(x []float32) []float64 {
	n := len(x)
	at := func(i int) float64 {
		switch {
		case i < 0:
			i = -i
		case i >= n:
			i = 2*n - 1 - i
		}
		return float64(x[i])
	}
	y := make([]float64, outputCount(int64(n)))
	for m := range y {
		s := 0.0
		for d := -postTaps; d <= preTaps; d++ {
			s += float64(taps[preTaps-d]) * at(3*m-d)
		}
		y[m] = s
	}
	return y
}

func randomSignal(rng *rand.Rand, n int) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(2*rng.Float64() - 1)
	}
	return x
}

// forEachKernel runs f with each kernel the CPU supports selected.
func forEachKernel(t *testing.T, f func(t *testing.T)) {
	saved := selected
	defer func() { selected = saved }()
	for _, k := range kernels() {
		selected = k
		t.Run(k.name, f)
	}
}

func TestOutputCount(t *testing.T) {
	for _, c := range []struct{ n, want int64 }{
		{0, 0},
		{1, 0},
		{50, 0},
		{66, 0},
		{67, 17},
		{68, 18},
		{69, 18},
		{99, 33},
		{100, 34},
		{101, 34},
		{102, 34},
		{103, 34},
		{104, 35},
		{960, 320},
		{961, 320},
		{962, 321},
		{48000, 16000},
		{48001, 16000},
		{48002, 16001},
		{1 << 40, (1<<40 + 1) / 3},
	} {
		if got := outputCount(c.n); got != c.want {
			t.Errorf("outputCount(%d) = %d, want %d", c.n, got, c.want)
		}
	}
	for n := int64(1); n < 5000; n++ {
		if outputCount(n) < outputCount(n-1) {
			t.Fatalf("outputCount decreases at %d", n)
		}
	}
}

// TestMatchesDefinition compares short and medium streams, in several
// chunkings, with the float64 reference.
func TestMatchesDefinition(t *testing.T) {
	forEachKernel(t, func(t *testing.T) {
		rng := rand.New(rand.NewPCG(3, 4))
		lengths := make([]int, 260, 267)
		for n := range lengths {
			lengths[n] = n
		}
		lengths = append(lengths, 959, 960, 961, 5759, 5760, 5761, 20000)
		for _, n := range lengths {
			x := randomSignal(rng, n)
			want := reference(x)
			for _, sizes := range [][]int{{n + 1}, {1}, {2}, {3}, {7}, {120}} {
				got := resampleChunks(x, sizes)
				if len(got) != len(want) {
					t.Fatalf("n=%d chunks %v: %d samples, want %d", n, sizes, len(got), len(want))
				}
				for i := range got {
					if d := math.Abs(float64(got[i]) - want[i]); d > 1e-6 {
						t.Fatalf("n=%d chunks %v: sample %d off by %g", n, sizes, i, d)
					}
				}
			}
		}
	})
}

// TestChunkInvariance checks that the output bits do not depend on how the
// input is split across Process calls or on the Flush buffer size.
func TestChunkInvariance(t *testing.T) {
	forEachKernel(t, func(t *testing.T) {
		rng := rand.New(rand.NewPCG(5, 6))
		for _, n := range []int{67, 100, 101, 137, 1000, 10007, 48000} {
			x := randomSignal(rng, n)
			whole := resampleChunks(x, []int{n})
			splits := make([][]int, 0, 11)
			splits = append(splits, []int{1}, []int{7}, []int{120}, []int{960}, []int{5760}, []int{2, 3, 5, 11, 13})
			for range 5 {
				splits = append(splits, randomChunks(rng, n, 1+rng.IntN(3000)))
			}
			for _, sizes := range splits {
				if got := resampleChunks(x, sizes); !slices.Equal(f32le(got), f32le(whole)) {
					t.Fatalf("n=%d: chunks %v change the output", n, sizes[:min(len(sizes), 8)])
				}
			}
			var r Resampler48To16
			out := make([]float32, n/3+256)
			got := append([]float32(nil), out[:r.Process(x, out)]...)
			got = append(got, flushAll(&r, 1)...)
			if !slices.Equal(f32le(got), f32le(whole)) {
				t.Fatalf("n=%d: flushing one sample at a time changes the output", n)
			}
		}
	})
}

func TestEmptyAndShortStreams(t *testing.T) {
	var r Resampler48To16
	out := make([]float32, 300)
	if r.Process(nil, out) != 0 {
		t.Fatal("output without input")
	}
	for range 2 {
		if r.Flush(out) != 0 {
			t.Fatal("Flush without input gave output")
		}
	}
	// Streams shorter than 67 samples produce nothing, however they arrive.
	for n := 1; n < minStream; n++ {
		for range n {
			if r.Process([]float32{1}, out) != 0 {
				t.Fatalf("output while %d samples are buffered", n)
			}
		}
		for range 2 {
			if r.Flush(out) != 0 {
				t.Fatalf("output for a %d-sample stream", n)
			}
		}
	}
	// Nothing comes out before the stream reaches 67 samples, even when the
	// first windows are complete.
	x := randomSignal(rand.New(rand.NewPCG(1, 1)), 67)
	if r.Process(x[:66], out) != 0 {
		t.Fatal("output before 67 samples")
	}
	if n := r.Process(x[66:], out); n != 6 {
		t.Fatalf("%d outputs at 67 samples, want 6", n)
	}
	if n := r.Flush(out); n != 11 {
		t.Fatalf("flush gave %d outputs, want 11", n)
	}
	for range 2 {
		if r.Flush(out) != 0 {
			t.Fatal("repeated Flush gave output")
		}
	}
}

// TestReuseAfterFlush checks that a drained resampler starts a new stream
// from scratch, and that Process after an unfinished Flush does too.
func TestReuseAfterFlush(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 9))
	a, b := randomSignal(rng, 2000), randomSignal(rng, 1500)
	want := resampleChunks(b, []int{len(b)})
	var r Resampler48To16
	out := make([]float32, 1000)
	r.Process(a, out)
	flushAll(&r, 7)
	got := append([]float32(nil), out[:r.Process(b, out)]...)
	got = append(got, flushAll(&r, 7)...)
	if !slices.Equal(f32le(got), f32le(want)) {
		t.Fatal("second stream differs after a complete flush")
	}
	r.Process(a, out)
	if r.Flush(out[:1]) != 1 {
		t.Fatal("no flush output")
	}
	got = append(got[:0], out[:r.Process(b, out)]...)
	got = append(got, flushAll(&r, 7)...)
	if !slices.Equal(f32le(got), f32le(want)) {
		t.Fatal("second stream differs after an unfinished flush")
	}
}

// TestSmallOutputBuffer checks that outputs that do not fit in out are kept
// for later calls rather than lost.
func TestSmallOutputBuffer(t *testing.T) {
	rng := rand.New(rand.NewPCG(10, 11))
	x := randomSignal(rng, 5000)
	want := resampleChunks(x, []int{len(x)})
	var r Resampler48To16
	var got []float32
	out := make([]float32, 5)
	for i := 0; i < len(x); i += 960 {
		n := r.Process(x[i:min(i+960, len(x))], out)
		if n > len(out) {
			t.Fatal("wrote past out")
		}
		got = append(got, out[:n]...)
	}
	for {
		n := r.Process(nil, out)
		got = append(got, out[:n]...)
		if n == 0 {
			break
		}
	}
	got = append(got, flushAll(&r, 3)...)
	if !slices.Equal(f32le(got), f32le(want)) {
		t.Fatal("a small output buffer changes the output")
	}
}

// TestOutputBound checks the decoder's sizing: an out of len(in)/3 + 256
// samples always takes everything Process has.
func TestOutputBound(t *testing.T) {
	rng := rand.New(rand.NewPCG(12, 13))
	var r Resampler48To16
	for range 2000 {
		in := randomSignal(rng, rng.IntN(6000))
		out := make([]float32, len(in)/3+256)
		r.Process(in, out)
		if r.Process(nil, out) != 0 {
			t.Fatal("Process left output behind")
		}
		if len(r.buf) >= nTaps {
			t.Fatalf("%d samples held back", len(r.buf))
		}
		if rng.IntN(50) == 0 {
			flushAll(&r, 256)
		}
	}
}

func TestNoAllocationsInSteadyState(t *testing.T) {
	rng := rand.New(rand.NewPCG(14, 15))
	in := randomSignal(rng, 5760)
	out := make([]float32, len(in)/3+256)
	var r Resampler48To16
	r.Process(in, out)
	sizes := []int{120, 960, 1920, 2880, 5760}
	i := 0
	if a := testing.AllocsPerRun(200, func() {
		r.Process(in[:sizes[i%len(sizes)]], out)
		i++
	}); a != 0 {
		t.Fatalf("%v allocations per Process", a)
	}
}

// TestLongStreamCounters checks that streams longer than 2^31 samples count
// correctly on 32-bit platforms, by advancing the counters of a stream by a
// whole number of output periods.
func TestLongStreamCounters(t *testing.T) {
	x := randomSignal(rand.New(rand.NewPCG(16, 17)), 3000)
	want := resampleChunks(x, []int{len(x)})
	var r Resampler48To16
	out := make([]float32, 1300)
	got := append([]float32(nil), out[:r.Process(x[:1500], out)]...)
	r.in += 3 << 31
	r.emitted += 1 << 31
	got = append(got, out[:r.Process(x[1500:], out)]...)
	got = append(got, flushAll(&r, 256)...)
	if !slices.Equal(f32le(got), f32le(want)) {
		t.Fatal("large counters change the output")
	}
}
