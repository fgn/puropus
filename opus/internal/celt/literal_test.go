package celt

import (
	"math"
	"math/rand"
	"testing"
)

// The tests in this file hold C-literal transcriptions of libopus functions
// that the decoder implements in a restructured form, and require the two
// to agree bit for bit on random input.

// expRotation1Literal is exp_rotation1 as libopus writes it, any stride.
func expRotation1Literal(X []float32, n, stride int, c, s float32) {
	ms := -s
	for i := 0; i < n-stride; i++ {
		x1 := X[i]
		x2 := X[i+stride]
		X[i+stride] = float32(c*x2) + float32(s*x1)
		X[i] = float32(c*x1) + float32(ms*x2)
	}
	for i := n - 2*stride - 1; i >= 0; i-- {
		x1 := X[i]
		x2 := X[i+stride]
		X[i+stride] = float32(c*x2) + float32(s*x1)
		X[i] = float32(c*x1) + float32(ms*x2)
	}
}

// expRotationLiteral is exp_rotation as libopus writes it.
func expRotationLiteral(X []float32, n, dir, stride, K, spread int) {
	stride2 := 0
	if 2*K >= n || spread == spreadNone {
		return
	}
	factor := spreadFactor[spread-1]
	gain := float32(n) / float32(n+factor*K)
	theta := float32(0.5 * (gain * gain))
	c := celtCosNorm(theta)
	s := celtCosNorm(q15One - theta)
	if n >= 8*stride {
		stride2 = 1
		for (stride2*stride2+stride2)*stride+(stride>>2) < n {
			stride2++
		}
	}
	n /= stride
	for i := 0; i < stride; i++ {
		if dir < 0 {
			if stride2 != 0 {
				expRotation1Literal(X[i*n:], n, stride2, s, c)
			}
			expRotation1Literal(X[i*n:], n, 1, c, s)
		} else {
			expRotation1Literal(X[i*n:], n, 1, c, -s)
			if stride2 != 0 {
				expRotation1Literal(X[i*n:], n, stride2, s, -c)
			}
		}
	}
}

// sameBits reports the first index where a and b differ in their bits, or
// -1.
func sameBits(a, b []float32) int {
	for i := range a {
		if math.Float32bits(a[i]) != math.Float32bits(b[i]) {
			return i
		}
	}
	return -1
}

func randomFloats(rng *rand.Rand, x []float32) {
	for i := range x {
		x[i] = float32(rng.NormFloat64())
	}
}

func TestExpRotationMatchesLiteral(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var got, want [maxBandSize]float32
	for iter := 0; iter < 20000; iter++ {
		stride := 1 << rng.Intn(5)
		n := stride * (1 + rng.Intn(maxBandSize/stride))
		K := rng.Intn(n/2 + 2)
		spread := rng.Intn(4)
		dir := 1 - 2*rng.Intn(2)
		randomFloats(rng, want[:n])
		copy(got[:n], want[:n])
		expRotationLiteral(want[:n], n, dir, stride, K, spread)
		expRotation(got[:n], n, dir, stride, K, spread)
		if i := sameBits(got[:n], want[:n]); i >= 0 {
			t.Fatalf("n=%d dir=%d stride=%d K=%d spread=%d: X[%d] = %g, want %g", n, dir, stride, K, spread, i, got[i], want[i])
		}
	}
}

func TestRotationGainsMatchRunTime(t *testing.T) {
	for n, off := range rotationGainOffset {
		for spread := 1; spread <= 3 && off >= 0; spread++ {
			for K := 0; 2*K < n; K++ {
				g := rotationGains[spread-1][int(off)+K]
				w := rotationGainOf(n, K, spread)
				if math.Float32bits(g.c) != math.Float32bits(w.c) || math.Float32bits(g.s) != math.Float32bits(w.s) {
					t.Fatalf("n=%d K=%d spread=%d: table %v, run time %v", n, K, spread, g, w)
				}
			}
		}
	}
}

func TestBits2pulsesMatchesSearch(t *testing.T) {
	m := StaticMode()
	for LM := -1; LM <= m.maxLM; LM++ {
		for band := 0; band < m.nbEBands; band++ {
			if m.cache.index[(LM+1)*m.nbEBands+band] < 0 {
				continue
			}
			for bits := -64; bits <= 20000; bits++ {
				if got, want := bits2pulses(m, band, LM, bits), bits2pulsesSearch(m, band, LM, bits); got != want {
					t.Fatalf("LM=%d band=%d bits=%d: %d, want %d", LM, band, bits, got, want)
				}
			}
		}
	}
}

// cwrsiLiteral is cwrsi as libopus writes it.
func cwrsiLiteral(n, k int, i uint32, y []int) float32 {
	var p uint32
	var s, k0, val int
	var yy float32
	yi := 0
	for n > 2 {
		var q uint32
		if k >= n {
			row := celtPVQURow[n]
			p = row[k+1]
			s = -b2i(i >= p)
			i -= p & uint32(s)
			k0 = k
			q = row[n]
			if q > i {
				k = n
				for {
					k--
					p = celtPVQURow[k][n]
					if p <= i {
						break
					}
				}
			} else {
				for p = row[k]; p > i; p = row[k] {
					k--
				}
			}
			i -= p
			val = (k0 - k + s) ^ s
			y[yi] = val
			yi++
			yy += float32(float32(val) * float32(val))
		} else {
			p = celtPVQURow[k][n]
			q = celtPVQURow[k+1][n]
			if p <= i && i < q {
				i -= p
				y[yi] = 0
				yi++
			} else {
				s = -b2i(i >= q)
				i -= q & uint32(s)
				k0 = k
				for {
					k--
					p = celtPVQURow[k][n]
					if p <= i {
						break
					}
				}
				i -= p
				val = (k0 - k + s) ^ s
				y[yi] = val
				yi++
				yy += float32(float32(val) * float32(val))
			}
		}
		n--
	}
	p = uint32(2*k + 1)
	s = -b2i(i >= p)
	i -= p & uint32(s)
	k0 = k
	k = int((i + 1) >> 1)
	if k != 0 {
		i -= uint32(2*k - 1)
	}
	val = (k0 - k + s) ^ s
	y[yi] = val
	yi++
	yy += float32(float32(val) * float32(val))
	s = -int(i)
	val = (k + s) ^ s
	y[yi] = val
	yy += float32(float32(val) * float32(val))
	return yy
}

// pvqCodebookFits reports whether CELT_PVQ_U_DATA holds V(n, k) and V(n, k)
// fits in 32 bits, as it must for ec_dec_uint.
func pvqCodebookFits(n, k int) bool {
	fits := func(a, b int) bool {
		r, c := min(a, b), max(a, b)
		if r >= len(celtPVQURow) {
			return false
		}
		start := len(celtPVQUData) - len(celtPVQURow[r])
		end := len(celtPVQUData) - 1
		if r+1 < len(celtPVQURow) {
			end = len(celtPVQUData) - len(celtPVQURow[r+1]) + r
		}
		return start+c <= end
	}
	return fits(n, k) && fits(n, k+1) && uint64(celtPVQU(n, k))+uint64(celtPVQU(n, k+1)) < 1<<32
}

func TestCwrsiMatchesLiteral(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var got [maxBandSize]int32
	var want [maxBandSize]int
	pairs := 0
	for n := 2; n <= maxBandSize; n++ {
		for k := 1; pvqCodebookFits(n, k); k++ {
			pairs++
			v := celtPVQV(n, k)
			for iter := 0; iter < 64; iter++ {
				var i uint32
				switch iter {
				case 0:
				case 1:
					i = v - 1
				default:
					i = uint32(rng.Uint64() % uint64(v))
				}
				yg := cwrsi(n, k, i, got[:n])
				yw := cwrsiLiteral(n, k, i, want[:n])
				if math.Float32bits(yg) != math.Float32bits(yw) {
					t.Fatalf("n=%d k=%d i=%d: Ryy %g, want %g", n, k, i, yg, yw)
				}
				for j := 0; j < n; j++ {
					if int(got[j]) != want[j] {
						t.Fatalf("n=%d k=%d i=%d: y %v, want %v", n, k, i, got[:n], want[:n])
					}
				}
			}
		}
	}
	t.Logf("%d (n, k) pairs", pairs)
}

// TestPostfilterDeemphasisMatchesPasses compares the fused post-filter and
// de-emphasis with combFilter's two calls per channel followed by
// deemphasis, as celt_decode_with_ec runs them.
func TestPostfilterDeemphasisMatchesPasses(t *testing.T) {
	m := StaticMode()
	rng := rand.New(rand.NewSource(9))
	const H = decodeBufferSize
	gains := []float32{0, 0.09375, 0.75, -0.5}
	for iter := 0; iter < 4000; iter++ {
		LM := rng.Intn(4)
		N := m.shortMdctSize << LM
		CC := 1 + rng.Intn(2)
		T0, T1, T2 := rng.Intn(combFilterMaxPeriod), rng.Intn(combFilterMaxPeriod), rng.Intn(combFilterMaxPeriod)
		g0, g1, g2 := gains[rng.Intn(4)], gains[rng.Intn(4)], gains[rng.Intn(4)]
		if rng.Intn(3) == 0 {
			T1, g1 = T0, g0
		}
		tap0, tap1, tap2 := rng.Intn(3), rng.Intn(3), rng.Intn(3)
		var want, got [2][]float32
		for c := 0; c < CC; c++ {
			want[c] = make([]float32, H)
			randomFloats(rng, want[c])
			got[c] = append([]float32(nil), want[c]...)
		}
		memW := []float32{float32(rng.NormFloat64()), float32(rng.NormFloat64())}
		memG := append([]float32(nil), memW...)
		pcmW := make([]float32, CC*N)
		pcmG := make([]float32, CC*N)
		T0, T1 = max(T0, combFilterMinPeriod), max(T1, combFilterMinPeriod)
		var outSyn [2][]float32
		for c := 0; c < CC; c++ {
			outSyn[c] = want[c][H-N:]
			combFilter(outSyn[c], want[c], H-N, T0, T1, m.shortMdctSize, g0, g1, tap0, tap1, m.window, m.overlap)
			if LM != 0 {
				combFilter(outSyn[c][m.shortMdctSize:], want[c], H-N+m.shortMdctSize, T1, T2, N-m.shortMdctSize,
					g1, g2, tap1, tap2, m.window, m.overlap)
			}
		}
		deemphasis(outSyn, pcmW, N, CC, 1, m.preemph[:], memW, make([]float32, N))
		var spanBuf [4]combSpan
		spans := combSpans(spanBuf[:0], 0, T0, T1, m.shortMdctSize, g0, g1, tap0, tap1, m.overlap)
		if LM != 0 {
			spans = combSpans(spans, m.shortMdctSize, T1, T2, N-m.shortMdctSize, g1, g2, tap1, tap2, m.overlap)
		}
		if CC == 2 {
			postfilterDeemphasisStereo(got[0], got[1], H-N, spans, m.window, pcmG, m.preemph[0], memG)
		} else {
			memG[0] = postfilterDeemphasis(got[0], H-N, spans, m.window, pcmG, m.preemph[0], memG[0])
		}
		for c := 0; c < CC; c++ {
			if i := sameBits(got[c], want[c]); i >= 0 {
				t.Fatalf("iter %d: channel %d history[%d] = %g, want %g", iter, c, i, got[c][i], want[c][i])
			}
		}
		if i := sameBits(pcmG, pcmW); i >= 0 {
			t.Fatalf("iter %d: pcm[%d] = %g, want %g", iter, i, pcmG[i], pcmW[i])
		}
		if i := sameBits(memG, memW); i >= 0 {
			t.Fatalf("iter %d: mem[%d] = %g, want %g", iter, i, memG[i], memW[i])
		}
	}
}

func TestThetaGainsMatchBitexact(t *testing.T) {
	for itheta := 1; itheta < 16384; itheta++ {
		imid := int(bitexactCos(int16(itheta)))
		iside := int(bitexactCos(int16(16384 - itheta)))
		gm, gs := thetaGains[itheta], thetaGains[16384-itheta]
		if int(gm.cos) != imid || int(gs.cos) != iside {
			t.Fatalf("itheta %d: cos %d %d, want %d %d", itheta, gm.cos, gs.cos, imid, iside)
		}
		if imid <= 0 || iside <= 0 {
			continue
		}
		if got, want := int(gs.term)-int(gm.term), bitexactLog2tan(iside, imid); got != want {
			t.Fatalf("itheta %d: log2tan %d, want %d", itheta, got, want)
		}
	}
}

func TestIsqrt32MatchesBits(t *testing.T) {
	check := func(v uint32) {
		if got, want := isqrt32(v), isqrt32Bits(v); got != want {
			t.Fatalf("isqrt32(%d) = %d, want %d", v, got, want)
		}
	}
	// Around every square, and the top of the range.
	for r := uint64(1); r*r <= 1<<32-1; r++ {
		for _, v := range []uint64{r*r - 1, r * r, r*r + 1} {
			if v >= 1 && v < 1<<32 {
				check(uint32(v))
			}
		}
	}
	check(1<<32 - 1)
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 1000000; i++ {
		check(1 + uint32(rng.Uint64()%(1<<32-1)))
	}
}

// hadamardLiteral is deinterleave_hadamard (or, with inverse,
// interleave_hadamard) as libopus writes it.
func hadamardLiteral(X []float32, N0, stride int, hadamard, inverse bool) {
	N := N0 * stride
	tmp := make([]float32, N)
	ordery := orderyTable[stride-2:]
	for i := 0; i < stride; i++ {
		for j := 0; j < N0; j++ {
			row := i
			if hadamard {
				row = ordery[i]
			}
			if inverse {
				tmp[j*stride+i] = X[row*N0+j]
			} else {
				tmp[row*N0+j] = X[j*stride+i]
			}
		}
	}
	copy(X, tmp)
}

func TestHadamardMatchesLiteral(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	var got, want, tmp [maxBandSize]float32
	for iter := 0; iter < 20000; iter++ {
		stride := 2 << rng.Intn(4)
		N0 := 1 + rng.Intn(maxBandSize/stride)
		N := N0 * stride
		hadamard := rng.Intn(2) == 0
		for _, inverse := range []bool{false, true} {
			randomFloats(rng, want[:N])
			copy(got[:N], want[:N])
			hadamardLiteral(want[:N], N0, stride, hadamard, inverse)
			if inverse {
				interleaveHadamard(got[:N], N0, stride, hadamard, tmp[:])
			} else {
				deinterleaveHadamard(got[:N], N0, stride, hadamard, tmp[:])
			}
			if i := sameBits(got[:N], want[:N]); i >= 0 {
				t.Fatalf("N0=%d stride=%d hadamard=%v inverse=%v: X[%d] = %g, want %g", N0, stride, hadamard, inverse, i, got[i], want[i])
			}
		}
	}
}
