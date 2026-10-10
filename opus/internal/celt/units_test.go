package celt

import (
	"bufio"
	"compress/gzip"
	"encoding/hex"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// The stage tests replay testdata/units.txt.gz, written by
// tools/libopus-ref/celt_ref.c from libopus's generic C build, whose
// arithmetic is the C source's: the decoder stages must reproduce it bit for
// bit. The format is documented at the top of celt_ref.c.

// unitCase is one line of units.txt: its kind, inputs and outputs.
type unitCase struct {
	line    int
	kind    string
	in, out []string
}

var (
	unitsOnce sync.Once
	units     map[string][]unitCase
	unitsErr  error
)

// readUnits parses testdata/units.txt.gz once.
func readUnits(t *testing.T) map[string][]unitCase {
	t.Helper()
	unitsOnce.Do(func() {
		f, err := os.Open("testdata/units.txt.gz")
		if err != nil {
			unitsErr = err
			return
		}
		defer func() { _ = f.Close() }()
		zr, err := gzip.NewReader(f)
		if err != nil {
			unitsErr = err
			return
		}
		units = map[string][]unitCase{}
		sc := bufio.NewScanner(zr)
		sc.Buffer(nil, 1<<22)
		for line := 1; sc.Scan(); line++ {
			text := sc.Text()
			if strings.HasPrefix(text, "#") {
				continue
			}
			in, out, ok := strings.Cut(text, " = ")
			fields := strings.Fields(in)
			if !ok || len(fields) == 0 {
				unitsErr = &os.PathError{Op: "parse", Path: "units.txt", Err: os.ErrInvalid}
				return
			}
			units[fields[0]] = append(units[fields[0]], unitCase{line, fields[0], fields[1:], strings.Fields(out)})
		}
		unitsErr = sc.Err()
	})
	if unitsErr != nil {
		t.Fatal(unitsErr)
	}
	return units
}

// fieldReader reads the fields of one side of a case in order.
type fieldReader struct {
	t    *testing.T
	line int
	f    []string
}

func (r *fieldReader) next() string {
	r.t.Helper()
	if len(r.f) == 0 {
		r.t.Fatalf("units.txt:%d: too few fields", r.line)
	}
	s := r.f[0]
	r.f = r.f[1:]
	return s
}

func (r *fieldReader) int() int {
	r.t.Helper()
	v, err := strconv.Atoi(r.next())
	if err != nil {
		r.t.Fatalf("units.txt:%d: %v", r.line, err)
	}
	return v
}

func (r *fieldReader) uint32() uint32 {
	r.t.Helper()
	v, err := strconv.ParseUint(r.next(), 10, 32)
	if err != nil {
		r.t.Fatalf("units.txt:%d: %v", r.line, err)
	}
	return uint32(v)
}

func (r *fieldReader) float() float32 {
	r.t.Helper()
	v, err := strconv.ParseUint(r.next(), 16, 32)
	if err != nil {
		r.t.Fatalf("units.txt:%d: %v", r.line, err)
	}
	return math.Float32frombits(uint32(v))
}

func (r *fieldReader) ints(n int) []int {
	r.t.Helper()
	v := make([]int, n)
	for i := range v {
		v[i] = r.int()
	}
	return v
}

func (r *fieldReader) floats(n int) []float32 {
	r.t.Helper()
	v := make([]float32, n)
	for i := range v {
		v[i] = r.float()
	}
	return v
}

func (r *fieldReader) bytes() []byte {
	r.t.Helper()
	b, err := hex.DecodeString(r.next())
	if err != nil {
		r.t.Fatalf("units.txt:%d: %v", r.line, err)
	}
	return b
}

func (r *fieldReader) done() {
	r.t.Helper()
	if len(r.f) != 0 {
		r.t.Fatalf("units.txt:%d: %d fields left", r.line, len(r.f))
	}
}

// sides returns readers for the inputs and outputs of c.
func (c unitCase) sides(t *testing.T) (in, out *fieldReader) {
	return &fieldReader{t, c.line, c.in}, &fieldReader{t, c.line, c.out}
}

// casesOf returns the cases of a kind, failing if there are none.
func casesOf(t *testing.T, kind string) []unitCase {
	t.Helper()
	cases := readUnits(t)[kind]
	if len(cases) == 0 {
		t.Fatalf("no %s cases", kind)
	}
	return cases
}

// sameFloats reports the first index where got and want differ in their
// bits, or -1; a length difference counts as a difference.
func sameFloats(got, want []float32) int {
	if len(got) != len(want) {
		return min(len(got), len(want))
	}
	for i := range want {
		if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
			return i
		}
	}
	return -1
}

func TestCwrsiMatchesLibopus(t *testing.T) {
	var y [maxBandSize]int32
	pairs := map[[2]int]bool{}
	for _, c := range casesOf(t, "cwrs") {
		in, out := c.sides(t)
		N, K, data := in.int(), in.int(), in.bytes()
		in.done()
		ryy, rng, want := out.float(), out.uint32(), out.ints(N)
		out.done()
		var dec entcode.Decoder
		dec.Init(data)
		got := decodePulses(y[:N], N, K, &dec)
		if math.Float32bits(got) != math.Float32bits(ryy) || dec.Rng() != rng {
			t.Fatalf("line %d: N=%d K=%d: Ryy %g rng %d, want %g %d", c.line, N, K, got, dec.Rng(), ryy, rng)
		}
		for i := range want {
			if int(y[i]) != want[i] {
				t.Fatalf("line %d: N=%d K=%d: y %v, want %v", c.line, N, K, y[:N], want)
			}
		}
		pairs[[2]int{N, K}] = true
	}
	t.Logf("%d (N, K) pairs", len(pairs))
}

func TestAlgUnquantMatchesLibopus(t *testing.T) {
	var iy [maxBandSize]int32
	var X [maxBandSize]float32
	for _, c := range casesOf(t, "unquant") {
		in, out := c.sides(t)
		N, K, spread, B, gain, data := in.int(), in.int(), in.int(), in.int(), in.float(), in.bytes()
		in.done()
		cm, rng, want := out.uint32(), out.uint32(), out.floats(N)
		out.done()
		var dec entcode.Decoder
		dec.Init(data)
		got := algUnquant(X[:N], N, K, spread, B, &dec, gain, iy[:])
		if got != uint(cm) || dec.Rng() != rng {
			t.Fatalf("line %d: collapse mask %d rng %d, want %d %d", c.line, got, dec.Rng(), cm, rng)
		}
		if i := sameFloats(X[:N], want); i >= 0 {
			t.Fatalf("line %d: N=%d K=%d spread=%d B=%d: X[%d] = %g, want %g", c.line, N, K, spread, B, i, X[i], want[i])
		}
	}
}

func TestInitCapsMatchesLibopus(t *testing.T) {
	m := StaticMode()
	var caps [maxBands]int
	for _, c := range casesOf(t, "caps") {
		in, out := c.sides(t)
		LM, C := in.int(), in.int()
		want := out.ints(maxBands)
		initCaps(m, caps[:], LM, C)
		for i := range want {
			if caps[i] != want[i] {
				t.Fatalf("line %d: caps %v, want %v", c.line, caps, want)
			}
		}
	}
}

func TestComputeAllocationMatchesLibopus(t *testing.T) {
	m := StaticMode()
	for _, c := range casesOf(t, "alloc") {
		in, out := c.sides(t)
		start, end, C, LM, trim, total := in.int(), in.int(), in.int(), in.int(), in.int(), in.int()
		offsets := in.ints(maxBands)
		data := in.bytes()
		in.done()
		coded, intensity, dual, balance := out.int(), out.int(), out.int(), out.int()
		rng, tellFrac := out.uint32(), out.int()
		pulses, ebits, prio := out.ints(maxBands), out.ints(maxBands), out.ints(maxBands)
		out.done()

		var caps, gotPulses, gotEbits, gotPrio [maxBands]int
		var gotIntensity, gotBalance int
		var gotDual bool
		initCaps(m, caps[:], LM, C)
		var dec entcode.Decoder
		dec.Init(data)
		gotCoded := cltComputeAllocation(m, start, end, offsets, caps[:], trim, &gotIntensity, &gotDual, total,
			&gotBalance, gotPulses[:], gotEbits[:], gotPrio[:], C, LM, &dec)
		if gotCoded != coded || gotIntensity != intensity || b2i(gotDual) != dual || gotBalance != balance ||
			dec.Rng() != rng || dec.TellFrac() != tellFrac {
			t.Fatalf("line %d: coded %d intensity %d dual %v balance %d rng %d tell %d, want %d %d %d %d %d %d",
				c.line, gotCoded, gotIntensity, gotDual, gotBalance, dec.Rng(), dec.TellFrac(),
				coded, intensity, dual, balance, rng, tellFrac)
		}
		for i := range maxBands {
			if gotPulses[i] != pulses[i] || gotEbits[i] != ebits[i] || gotPrio[i] != prio[i] {
				t.Fatalf("line %d: band %d: pulses %d ebits %d priority %d, want %d %d %d", c.line, i,
					gotPulses[i], gotEbits[i], gotPrio[i], pulses[i], ebits[i], prio[i])
			}
		}
	}
}

func TestUnquantEnergyMatchesLibopus(t *testing.T) {
	m := StaticMode()
	t.Run("coarse", func(t *testing.T) {
		for _, c := range casesOf(t, "coarse") {
			in, out := c.sides(t)
			start, end, C, LM, intra := in.int(), in.int(), in.int(), in.int(), in.int()
			old := in.floats(2 * maxBands)
			data := in.bytes()
			in.done()
			rng, tell, want := out.uint32(), out.int(), out.floats(2*maxBands)
			out.done()
			var dec entcode.Decoder
			dec.Init(data)
			unquantCoarseEnergy(m, start, end, old, intra != 0, &dec, C, LM)
			if dec.Rng() != rng || dec.Tell() != tell {
				t.Fatalf("line %d: rng %d tell %d, want %d %d", c.line, dec.Rng(), dec.Tell(), rng, tell)
			}
			if i := sameFloats(old, want); i >= 0 {
				t.Fatalf("line %d: energy %d = %g, want %g", c.line, i, old[i], want[i])
			}
		}
	})
	t.Run("fine", func(t *testing.T) {
		for _, c := range casesOf(t, "fine") {
			in, out := c.sides(t)
			start, end, C := in.int(), in.int(), in.int()
			fq, old, data := in.ints(maxBands), in.floats(2*maxBands), in.bytes()
			in.done()
			rng, want := out.uint32(), out.floats(2*maxBands)
			out.done()
			var dec entcode.Decoder
			dec.Init(data)
			unquantFineEnergy(m, start, end, old, fq, &dec, C)
			if dec.Rng() != rng {
				t.Fatalf("line %d: rng %d, want %d", c.line, dec.Rng(), rng)
			}
			if i := sameFloats(old, want); i >= 0 {
				t.Fatalf("line %d: energy %d = %g, want %g", c.line, i, old[i], want[i])
			}
		}
	})
	t.Run("final", func(t *testing.T) {
		for _, c := range casesOf(t, "final") {
			in, out := c.sides(t)
			start, end, C, bitsLeft := in.int(), in.int(), in.int(), in.int()
			fq, prio, old, data := in.ints(maxBands), in.ints(maxBands), in.floats(2*maxBands), in.bytes()
			in.done()
			rng, want := out.uint32(), out.floats(2*maxBands)
			out.done()
			var dec entcode.Decoder
			dec.Init(data)
			unquantEnergyFinalise(m, start, end, old, fq, prio, bitsLeft, &dec, C)
			if dec.Rng() != rng {
				t.Fatalf("line %d: rng %d, want %d", c.line, dec.Rng(), rng)
			}
			if i := sameFloats(old, want); i >= 0 {
				t.Fatalf("line %d: energy %d = %g, want %g", c.line, i, old[i], want[i])
			}
		}
	})
}

func TestQuantAllBandsMatchesLibopus(t *testing.T) {
	m := StaticMode()
	var ctx bandCtx
	var norm [maxNormSize]float32
	kinds := map[string]int{}
	for _, c := range casesOf(t, "bands") {
		in, out := c.sides(t)
		start, end, C, LM, trim, total := in.int(), in.int(), in.int(), in.int(), in.int(), in.int()
		spread, short, seed, disableInv := in.int(), in.int(), in.uint32(), in.int()
		tf := in.ints(maxBands)
		data := in.bytes()
		in.done()
		M := 1 << LM
		N := M * m.shortMdctSize
		coded := M * (m.eBands[end] - m.eBands[start])
		rng, wantSeed := out.uint32(), out.uint32()
		masks := out.ints(C * maxBands)
		want := out.floats(C * coded)
		out.done()

		var caps, offsets, pulses, ebits, prio [maxBands]int
		var intensity, balance int
		var dual bool
		var X [2 * maxFrameSize]float32
		var collapseMasks [2 * maxBands]uint8
		initCaps(m, caps[:], LM, C)
		var dec entcode.Decoder
		dec.Init(data)
		if got := dec.Storage()*8<<bitRes - dec.TellFrac() - 1; got != total {
			t.Fatalf("line %d: total %d, want %d", c.line, got, total)
		}
		codedBands := cltComputeAllocation(m, start, end, offsets[:], caps[:], trim, &intensity, &dual, total,
			&balance, pulses[:], ebits[:], prio[:], C, LM, &dec)
		var Y []float32
		if C == 2 {
			Y = X[N : 2*N]
		}
		quantAllBands(&ctx, m, start, end, X[:N], Y, collapseMasks[:C*maxBands], pulses[:], short != 0, spread, dual,
			intensity, tf, len(data)*(8<<bitRes), balance, &dec, LM, codedBands, &seed, disableInv != 0, norm[:])
		if dec.Rng() != rng || seed != wantSeed {
			t.Fatalf("line %d: rng %d seed %d, want %d %d", c.line, dec.Rng(), seed, rng, wantSeed)
		}
		for i, v := range masks {
			if int(collapseMasks[i]) != v {
				t.Fatalf("line %d: collapse mask %d = %d, want %d", c.line, i, collapseMasks[i], v)
			}
		}
		for ch := 0; ch < C; ch++ {
			got := X[ch*N+M*m.eBands[start] : ch*N+M*m.eBands[end]]
			if i := sameFloats(got, want[ch*coded:(ch+1)*coded]); i >= 0 {
				t.Fatalf("line %d: C=%d LM=%d channel %d: X[%d] = %g, want %g", c.line, C, LM, ch,
					M*m.eBands[start]+i, got[i], want[ch*coded+i])
			}
		}
		key := "mono"
		if C == 2 {
			key = "stereo"
			if dual {
				key += "-dual"
			}
			if intensity < end {
				key += "-intensity"
			}
		}
		if short != 0 {
			key += "-transient"
		}
		kinds[key]++
	}
	t.Logf("cases: %v", kinds)
}

func TestAntiCollapseMatchesLibopus(t *testing.T) {
	m := StaticMode()
	for _, c := range casesOf(t, "anti") {
		in, out := c.sides(t)
		C, LM, start, end, seed := in.int(), in.int(), in.int(), in.int(), in.uint32()
		masks := in.ints(C * maxBands)
		pulses := in.ints(maxBands)
		logE, p1, p2 := in.floats(2*maxBands), in.floats(2*maxBands), in.floats(2*maxBands)
		N := m.shortMdctSize << LM
		X := in.floats(C * N)
		in.done()
		want := out.floats(C * N)
		out.done()
		collapseMasks := make([]uint8, len(masks))
		for i, v := range masks {
			collapseMasks[i] = uint8(v)
		}
		antiCollapse(m, X, collapseMasks, LM, C, N, start, end, logE, p1, p2, pulses, seed)
		if i := sameFloats(X, want); i >= 0 {
			t.Fatalf("line %d: X[%d] = %g, want %g", c.line, i, X[i], want[i])
		}
	}
}

func TestDenormaliseBandsMatchesLibopus(t *testing.T) {
	m := StaticMode()
	for _, c := range casesOf(t, "denorm") {
		in, out := c.sides(t)
		LM, start, end, ds, silence := in.int(), in.int(), in.int(), in.int(), in.int()
		N := m.shortMdctSize << LM
		logE, X := in.floats(maxBands), in.floats(N)
		in.done()
		want := out.floats(N)
		out.done()
		freq := make([]float32, N)
		for i := range freq {
			freq[i] = 123
		}
		denormaliseBands(m, X, freq, logE, start, end, 1<<LM, ds, silence != 0)
		if i := sameFloats(freq, want); i >= 0 {
			t.Fatalf("line %d: freq[%d] = %g, want %g", c.line, i, freq[i], want[i])
		}
	}
}

func TestInverseMDCTMatchesLibopus(t *testing.T) {
	m := StaticMode()
	sizes := map[[2]int]bool{}
	for _, c := range casesOf(t, "imdct") {
		in, out := c.sides(t)
		shift, stride := in.int(), in.int()
		N2 := 960 >> shift
		x, hist := in.floats(stride*N2), in.floats(60)
		in.done()
		want := out.floats(N2 + 60)
		out.done()
		y := make([]float32, N2+m.overlap)
		copy(y, hist)
		m.mdct.cltMdctBackward(x, y, m.window, m.overlap, shift, stride)
		if i := sameFloats(y[:N2+60], want); i >= 0 {
			t.Fatalf("line %d: shift %d stride %d: out[%d] = %g, want %g", c.line, shift, stride, i, y[i], want[i])
		}
		sizes[[2]int{2 * N2, stride}] = true
	}
	if len(sizes) != 7 {
		t.Fatalf("tested %d size and stride pairs, want 7", len(sizes))
	}
}

func TestCombFilterMatchesLibopus(t *testing.T) {
	m := StaticMode()
	for _, c := range casesOf(t, "comb") {
		in, out := c.sides(t)
		T0, T1, N, g0, g1 := in.int(), in.int(), in.int(), in.float(), in.float()
		tap0, tap1, overlap, inplace, H := in.int(), in.int(), in.int(), in.int(), in.int()
		x := in.floats(H + N)
		in.done()
		want := out.floats(N)
		out.done()
		var window []float32
		if overlap > 0 {
			window = m.window
		}
		y := x[H:]
		if inplace == 0 {
			y = make([]float32, N)
		}
		combFilter(y, x, H, T0, T1, N, g0, g1, tap0, tap1, window, overlap)
		if i := sameFloats(y, want); i >= 0 {
			t.Fatalf("line %d: y[%d] = %g, want %g", c.line, i, y[i], want[i])
		}
	}
}

func TestDeemphasisMatchesLibopus(t *testing.T) {
	for _, c := range casesOf(t, "deemph") {
		in, out := c.sides(t)
		N, C, ds, coef := in.int(), in.int(), in.int(), in.float()
		mem := in.floats(C)
		var x [2][]float32
		for ch := 0; ch < C; ch++ {
			x[ch] = in.floats(N)
		}
		in.done()
		want, wantMem := out.floats(C*N/ds), out.floats(C)
		out.done()
		pcm := make([]float32, C*N/ds)
		deemphasis(x, pcm, N, C, ds, []float32{coef}, mem, make([]float32, N))
		if i := sameFloats(pcm, want); i >= 0 {
			t.Fatalf("line %d: pcm[%d] = %g, want %g", c.line, i, pcm[i], want[i])
		}
		if i := sameFloats(mem, wantMem); i >= 0 {
			t.Fatalf("line %d: mem[%d] = %g, want %g", c.line, i, mem[i], wantMem[i])
		}
	}
}

func TestConcealmentHelpersMatchLibopus(t *testing.T) {
	m := StaticMode()
	t.Run("pitch", func(t *testing.T) {
		for _, c := range casesOf(t, "plcpitch") {
			in, out := c.sides(t)
			C := in.int()
			var x [2][]float32
			for ch := 0; ch < C; ch++ {
				x[ch] = in.floats(decodeBufferSize)
			}
			in.done()
			want, wantPitch := out.floats(decodeBufferSize>>1), out.int()
			out.done()
			lp := make([]float32, decodeBufferSize>>1)
			pitchDownsample(x, lp, decodeBufferSize, C)
			if i := sameFloats(lp, want); i >= 0 {
				t.Fatalf("line %d: x_lp[%d] = %g, want %g", c.line, i, lp[i], want[i])
			}
			pitch := pitchSearch(lp[plcPitchLagMax>>1:], lp, decodeBufferSize-plcPitchLagMax, plcPitchLagMax-plcPitchLagMin)
			if pitch != wantPitch {
				t.Fatalf("line %d: pitch %d, want %d", c.line, pitch, wantPitch)
			}
		}
	})
	t.Run("autocorr", func(t *testing.T) {
		for _, c := range casesOf(t, "autocorr") {
			in, out := c.sides(t)
			n, lag, overlap := in.int(), in.int(), in.int()
			x := in.floats(n)
			in.done()
			want := out.floats(lag + 1)
			out.done()
			ac := make([]float32, lag+1)
			celtAutocorr(x, ac, m.window, overlap, lag, n)
			if i := sameFloats(ac, want); i >= 0 {
				t.Fatalf("line %d: ac[%d] = %g, want %g", c.line, i, ac[i], want[i])
			}
		}
	})
	t.Run("lpc", func(t *testing.T) {
		for _, c := range casesOf(t, "lpc") {
			in, out := c.sides(t)
			p := in.int()
			ac := in.floats(p + 1)
			in.done()
			want := out.floats(p)
			out.done()
			lpc := make([]float32, p)
			celtLpc(lpc, ac, p)
			if i := sameFloats(lpc, want); i >= 0 {
				t.Fatalf("line %d: lpc[%d] = %g, want %g", c.line, i, lpc[i], want[i])
			}
		}
	})
	t.Run("fir", func(t *testing.T) {
		for _, c := range casesOf(t, "fir") {
			in, out := c.sides(t)
			N, ord := in.int(), in.int()
			num, x := in.floats(ord), in.floats(ord+N)
			in.done()
			want := out.floats(N)
			out.done()
			y := make([]float32, N)
			celtFir(x, num, y, N, ord)
			if i := sameFloats(y, want); i >= 0 {
				t.Fatalf("line %d: y[%d] = %g, want %g", c.line, i, y[i], want[i])
			}
		}
	})
	t.Run("iir", func(t *testing.T) {
		for _, c := range casesOf(t, "iir") {
			in, out := c.sides(t)
			N, ord := in.int(), in.int()
			den, mem, x := in.floats(ord), in.floats(ord), in.floats(N)
			in.done()
			want, wantMem := out.floats(N), out.floats(ord)
			out.done()
			y := make([]float32, N)
			celtIir(x, den, y, N, ord, mem)
			if i := sameFloats(y, want); i >= 0 {
				t.Fatalf("line %d: y[%d] = %g, want %g", c.line, i, y[i], want[i])
			}
			if i := sameFloats(mem, wantMem); i >= 0 {
				t.Fatalf("line %d: mem[%d] = %g, want %g", c.line, i, mem[i], wantMem[i])
			}
			// The concealment filters in place.
			in, _ = c.sides(t)
			in.int()
			in.int()
			den, mem, x = in.floats(ord), in.floats(ord), in.floats(N)
			celtIir(x, den, x, N, ord, mem)
			if i := sameFloats(x, want); i >= 0 {
				t.Fatalf("line %d: in place: y[%d] = %g, want %g", c.line, i, x[i], want[i])
			}
		}
	})
}
