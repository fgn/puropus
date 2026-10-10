package silk

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// The optimized stages must reproduce libopus's loops bit for bit. Each test
// here runs one against a transcription of the C loop (or the generic
// libopus-order loop the package keeps for other cases) on random input,
// with full-scale and saturating values.

// randInt16s fills x with a mix of full-range, full-scale and small values.
func randInt16s(r *rand.Rand, x []int16) {
	switch r.IntN(4) {
	case 0:
		for i := range x {
			x[i] = int16(r.Uint32())
		}
	case 1:
		// Full scale, both signs: drives the outputs into saturation.
		for i := range x {
			x[i] = []int16{-32768, -32767, 32767, 32766}[r.IntN(4)]
		}
	case 2:
		for i := range x {
			x[i] = int16(r.IntN(201) - 100)
		}
	default:
		// A loud sine-like ramp with noise.
		v := int32(0)
		for i := range x {
			v += int32(r.IntN(8001) - 4000)
			v = max(-32768, min(32767, v))
			x[i] = int16(v)
		}
	}
}

// up2HQRef is silk_resampler_private_up2_HQ as libopus writes it, with the
// state in memory.
func up2HQRef(s *[resamplerMaxIIROrder]int32, out []int16, in []int16, length int) {
	hq0 := [3]int32{resamplerUp2HQ00, resamplerUp2HQ01, resamplerUp2HQ02}
	hq1 := [3]int32{resamplerUp2HQ10, resamplerUp2HQ11, resamplerUp2HQ12}
	for k := 0; k < length; k++ {
		in32 := int32(in[k]) << 10
		Y := in32 - s[0]
		X := smulwb(Y, hq0[0])
		out32a := s[0] + X
		s[0] = in32 + X
		Y = out32a - s[1]
		X = smulwb(Y, hq0[1])
		out32b := s[1] + X
		s[1] = out32a + X
		Y = out32b - s[2]
		X = smlawb(Y, Y, hq0[2])
		out32a = s[2] + X
		s[2] = out32b + X
		out[2*k] = sat16(rshiftRound(out32a, 10))
		Y = in32 - s[3]
		X = smulwb(Y, hq1[0])
		out32a = s[3] + X
		s[3] = in32 + X
		Y = out32a - s[4]
		X = smulwb(Y, hq1[1])
		out32b = s[4] + X
		s[4] = out32a + X
		Y = out32b - s[5]
		X = smlawb(Y, Y, hq1[2])
		out32a = s[5] + X
		s[5] = out32b + X
		out[2*k+1] = sat16(rshiftRound(out32a, 10))
	}
}

func TestUp2HQMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := range 2000 {
		length := 1 + r.IntN(2*maxFsKHz*resamplerMaxBatchSizeMs)
		in := make([]int16, length)
		randInt16s(r, in)
		var s, sRef [resamplerMaxIIROrder]int32
		for i := range s {
			s[i] = int32(r.Uint32()) >> r.IntN(12)
		}
		sRef = s
		got, want := make([]int16, 2*length), make([]int16, 2*length)
		resamplerPrivateUp2HQ(&s, got, in, length)
		up2HQRef(&sRef, want, in, length)
		if !slices.Equal(got, want) || s != sRef {
			t.Fatalf("iteration %d, length %d: output or state differs", iter, length)
		}
	}
}

// TestFIRInterpolMatchesGeneric runs the interpolation stage with the
// decoder's increments to 48 kHz, and some others, against libopus's loop
// for every batch length up to the largest batch.
func TestFIRInterpolMatchesGeneric(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	incs := []int32{invRatio16To48Q16, invRatio12To48Q16, invRatio8To48Q16, 87382, 65536, 98304, 43690}
	for _, inc := range incs {
		for n := 1; n <= resamplerFixedRatioMaxIn+3; n++ {
			for range 8 {
				buf := make([]int16, 2*n+resamplerOrderFIR12)
				randInt16s(r, buf)
				maxIndex := int32(n) << 17
				outLen := int((int64(maxIndex) + int64(inc) - 1) / int64(inc))
				got := make([]int16, outLen+5)
				want := make([]int16, outLen+5)
				restGot := resamplerPrivateIIRFIRInterpol(got, buf, maxIndex, inc)
				restWant := resamplerIIRFIRInterpolGeneric(want, buf, maxIndex, inc)
				if len(restGot) != len(restWant) || !slices.Equal(got, want) {
					t.Fatalf("increment %d, n %d: output differs", inc, n)
				}
			}
		}
	}
}

// TestFIRInterpolCoresMatchGeneric checks the scalar fixed-ratio cores
// directly, whichever core the build dispatches to.
func TestFIRInterpolCoresMatchGeneric(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	cores := []struct {
		inc   int32
		ratio int
		fn    func(out, buf []int16, n int) int
	}{
		{invRatio16To48Q16, 3, interpolFIR3Over2Scalar},
		{invRatio12To48Q16, 4, interpolFIR2Scalar},
		{invRatio8To48Q16, 6, interpolFIR3Scalar},
	}
	for _, c := range cores {
		for n := 1; n <= resamplerFixedRatioMaxIn; n++ {
			for range 4 {
				buf := make([]int16, 2*n+resamplerOrderFIR12)
				randInt16s(r, buf)
				got := make([]int16, c.ratio*n)
				want := make([]int16, c.ratio*n)
				if k := c.fn(got, buf, n); k != c.ratio*n {
					t.Fatalf("increment %d: %d outputs, want %d", c.inc, k, c.ratio*n)
				}
				resamplerIIRFIRInterpolGeneric(want, buf, int32(n)<<17, c.inc)
				if !slices.Equal(got, want) {
					t.Fatalf("increment %d, n %d: output differs", c.inc, n)
				}
			}
		}
	}
}

func BenchmarkUp2HQ(b *testing.B) {
	r := rand.New(rand.NewPCG(7, 8))
	in := make([]int16, 160)
	randInt16s(r, in)
	out := make([]int16, 320)
	var s [resamplerMaxIIROrder]int32
	for b.Loop() {
		resamplerPrivateUp2HQ(&s, out, in, len(in))
	}
}

func BenchmarkFIRInterpol(b *testing.B) {
	r := rand.New(rand.NewPCG(9, 10))
	for _, c := range []struct {
		name string
		inc  int32
		n    int
	}{{"16k", invRatio16To48Q16, 160}, {"12k", invRatio12To48Q16, 120}, {"8k", invRatio8To48Q16, 80}} {
		b.Run(c.name, func(b *testing.B) {
			buf := make([]int16, 2*c.n+resamplerOrderFIR12)
			randInt16s(r, buf)
			out := make([]int16, 480)
			for b.Loop() {
				resamplerPrivateIIRFIRInterpol(out, buf, int32(c.n)<<17, c.inc)
			}
		})
		b.Run(c.name+"-generic", func(b *testing.B) {
			buf := make([]int16, 2*c.n+resamplerOrderFIR12)
			randInt16s(r, buf)
			out := make([]int16, 480)
			for b.Loop() {
				resamplerIIRFIRInterpolGeneric(out, buf, int32(c.n)<<17, c.inc)
			}
		})
	}
}

// addSat32Ref, subSat32Ref and lshiftSat32Ref are silk_ADD_SAT32,
// silk_SUB_SAT32 and silk_LSHIFT_SAT32 as the C macros write them.
func addSat32Ref(a, b int32) int32 {
	sum := int32(uint32(a) + uint32(b))
	if uint32(sum)&0x80000000 == 0 {
		if uint32(a&b)&0x80000000 != 0 {
			return int32Min
		}
		return sum
	}
	if uint32(a|b)&0x80000000 == 0 {
		return int32Max
	}
	return sum
}

func subSat32Ref(a, b int32) int32 {
	diff := int32(uint32(a) - uint32(b))
	if uint32(diff)&0x80000000 == 0 {
		if uint32(a)&(uint32(b)^0x80000000)&0x80000000 != 0 {
			return int32Min
		}
		return diff
	}
	if (uint32(a)^0x80000000)&uint32(b)&0x80000000 != 0 {
		return int32Max
	}
	return diff
}

func lshiftSat32Ref(a int32, shift int) int32 {
	return limit(a, int32(int32Min)>>shift, int32(int32Max)>>shift) << shift
}

// randInt32 returns a full-range, extreme, or moderately sized value.
func randInt32(r *rand.Rand) int32 {
	switch r.IntN(4) {
	case 0:
		return int32(r.Uint32())
	case 1:
		return []int32{int32Min, int32Min + 1, -1, 0, 1, int32Max - 1, int32Max, int32Max >> 4, int32Min >> 4, int32Max>>4 + 1, int32Min>>4 - 1}[r.IntN(11)]
	default:
		return int32(r.Uint32()) >> (4 + r.IntN(20))
	}
}

func TestSaturatingMacros(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	for range 1_000_000 {
		a, b := randInt32(r), randInt32(r)
		if got, want := addSat32(a, b), addSat32Ref(a, b); got != want {
			t.Fatalf("addSat32(%d, %d) = %d, want %d", a, b, got, want)
		}
		if got, want := subSat32(a, b), subSat32Ref(a, b); got != want {
			t.Fatalf("subSat32(%d, %d) = %d, want %d", a, b, got, want)
		}
		shift := r.IntN(31)
		if got, want := lshiftSat32(a, shift), lshiftSat32Ref(a, shift); got != want {
			t.Fatalf("lshiftSat32(%d, %d) = %d, want %d", a, shift, got, want)
		}
	}
}

func TestLPCAddMatchesMacros(t *testing.T) {
	r := rand.New(rand.NewPCG(35, 36))
	edges := []int32{
		int32Min, int32Min + 1, int32Min >> 4, int32Min>>4 - 1, int32Min>>4 + 1, -1, 0, 1,
		int32Max >> 4, int32Max>>4 - 1, int32Max>>4 + 1, int32Max - 1, int32Max,
	}
	check := func(res, pred int32) {
		want := int64(addSat32Ref(res, lshiftSat32Ref(pred, 4)))
		got, ok := lpcAdd(res, pred)
		if !ok {
			got = lpcAddSat(res, pred)
		}
		if got != want {
			t.Fatalf("lpcAdd(%d, %d) = %d, want %d", res, pred, got, want)
		}
	}
	for _, a := range edges {
		for _, b := range edges {
			check(a, b)
		}
	}
	for range 1_000_000 {
		check(randInt32(r), randInt32(r))
	}
}

// lpcSynthesisRef is the short-term prediction loop of silk_decode_core as
// libopus writes it.
func lpcSynthesisRef(sLPCQ14 []int32, presQ14 []int32, pxq []int16, aQ12 *[maxLPCOrder]int16, order int, gainQ10 int32) {
	for i := range pxq {
		s := sLPCQ14[i : i+maxLPCOrder]
		lpcPredQ10 := int32(order >> 1)
		for j := range order {
			lpcPredQ10 = smlawb(lpcPredQ10, s[maxLPCOrder-1-j], int32(aQ12[j]))
		}
		sLPCQ14[maxLPCOrder+i] = addSat32Ref(presQ14[i], lshiftSat32Ref(lpcPredQ10, 4))
		pxq[i] = sat16(rshiftRound(smulww(sLPCQ14[maxLPCOrder+i], gainQ10), 8))
	}
}

func TestLPCSynthesisMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	for iter := range 20000 {
		order := []int{10, 16}[iter&1]
		n := []int{40, 60, 80, 1 + r.IntN(maxSubFrameLength)}[r.IntN(4)]
		var a [maxLPCOrder]int16
		for j := range order {
			if r.IntN(4) == 0 {
				a[j] = int16(r.Uint32())
			} else {
				a[j] = int16(int32(r.Uint32()) >> 19)
			}
		}
		state := make([]int32, maxLPCOrder+n)
		for j := range maxLPCOrder {
			state[j] = randInt32(r)
		}
		res := make([]int32, n)
		for j := range res {
			res[j] = randInt32(r)
		}
		gain := randInt32(r)
		want, got := slices.Clone(state), slices.Clone(state)
		xqWant, xqGot := make([]int16, n), make([]int16, n)
		lpcSynthesisRef(want, res, xqWant, &a, order, gain)
		if order == 16 {
			lpcSynthesis16(got, res, xqGot, &a, gain)
		} else {
			lpcSynthesis10(got, res, xqGot, &a, gain)
		}
		if !slices.Equal(got, want) || !slices.Equal(xqGot, xqWant) {
			t.Fatalf("iteration %d (order %d, n %d): output differs", iter, order, n)
		}
	}
}

func BenchmarkLPCSynthesis16(b *testing.B) {
	r := rand.New(rand.NewPCG(15, 16))
	var a [maxLPCOrder]int16
	for j := range a {
		a[j] = int16(int32(r.Uint32()) >> 21)
	}
	state := make([]int32, maxLPCOrder+80)
	res := make([]int32, 80)
	for j := range res {
		res[j] = int32(r.Uint32()) >> 12
	}
	xq := make([]int16, 80)
	for b.Loop() {
		lpcSynthesis16(state, res, xq, &a, 1<<10)
		copy(state[:maxLPCOrder], state[80:])
	}
}

// decodeExcitationRef is the excitation loop of silk_decode_core as libopus
// writes it.
func decodeExcitationRef(excQ14 []int32, pulses []int16, randSeed, offsetQ10 int32) {
	for i := range excQ14 {
		randSeed = randNext(randSeed)
		excQ14[i] = int32(pulses[i]) << 14
		if excQ14[i] > 0 {
			excQ14[i] -= quantLevelAdjustQ10 << 4
		} else if excQ14[i] < 0 {
			excQ14[i] += quantLevelAdjustQ10 << 4
		}
		excQ14[i] += offsetQ10 << 4
		if randSeed < 0 {
			excQ14[i] = -excQ14[i]
		}
		randSeed += int32(pulses[i])
	}
}

func TestDecodeExcitationMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(19, 20))
	for range 5000 {
		n := 1 + r.IntN(maxFrameLength)
		pulses := make([]int16, n)
		for i := range pulses {
			switch r.IntN(4) {
			case 0:
				pulses[i] = int16(r.Uint32())
			case 1:
				pulses[i] = int16(r.IntN(7) - 3)
			}
		}
		seed := int32(r.Uint32())
		offset := int32(quantizationOffsetsQ10[r.IntN(2)][r.IntN(2)])
		got, want := make([]int32, n), make([]int32, n)
		decodeExcitation(got, pulses, seed, offset)
		decodeExcitationRef(want, pulses, seed, offset)
		if !slices.Equal(got, want) {
			t.Fatalf("n %d: excitation differs", n)
		}
	}
}

func BenchmarkLPCAnalysisFilter(b *testing.B) {
	r := rand.New(rand.NewPCG(27, 28))
	for _, d := range []int{10, 16} {
		b.Run(strconv.Itoa(d), func(b *testing.B) {
			in := make([]int16, 300)
			for i := range in {
				in[i] = int16(r.IntN(8001) - 4000)
			}
			coefs := make([]int16, d)
			for i := range coefs {
				coefs[i] = int16(r.IntN(4001) - 2000)
			}
			out := make([]int16, 300)
			for b.Loop() {
				lpcAnalysisFilter(out, in, coefs, len(in), d)
			}
		})
	}
}

func TestLPCAnalysisFilterMatchesGeneric(t *testing.T) {
	r := rand.New(rand.NewPCG(29, 30))
	for iter := range 20000 {
		d := []int{10, 16}[iter&1]
		length := r.IntN(maxLTPMemLength + 1)
		in := make([]int16, length)
		randInt16s(r, in)
		b := make([]int16, d)
		for j := range b {
			if r.IntN(4) == 0 {
				b[j] = int16(r.Uint32())
			} else {
				b[j] = int16(r.IntN(8193) - 4096)
			}
		}
		// libopus clears d outputs even when length is shorter.
		n := max(length, d)
		got, want := make([]int16, n+1), make([]int16, n+1)
		for i := range got {
			got[i] = int16(r.Uint32())
		}
		copy(want, got)
		lpcAnalysisFilter(got, in, b, length, d)
		lpcAnalysisFilterGeneric(want, in, b, length, d)
		if !slices.Equal(got, want) {
			t.Fatalf("iteration %d (order %d, length %d): output differs", iter, d, length)
		}
	}
}

// decodeSignsRef is silk_decode_signs as libopus writes it.
func decodeSignsRef(psRangeDec *entcode.Decoder, pulses []int16, length, signalType, quantOffsetType int, sumPulses *[maxNbShellBlocks]int) {
	var icdf [2]uint8
	icdfPtr := signICDF[7*(quantOffsetType+(signalType<<1)):]
	length = (length + shellCodecFrameLength/2) >> log2ShellCodecFrameLength
	for i := range length {
		qPtr := pulses[i*shellCodecFrameLength : (i+1)*shellCodecFrameLength]
		p := sumPulses[i]
		if p > 0 {
			icdf[0] = icdfPtr[min(p&0x1F, 6)]
			for j := range qPtr {
				if qPtr[j] > 0 {
					qPtr[j] *= int16((psRangeDec.DecICDF(icdf[:], 8) << 1) - 1)
				}
			}
		}
	}
}

func TestDecodeSignsMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 32))
	for range 5000 {
		frameLength := []int{80, 120, 160, 240, 320}[r.IntN(5)]
		var pulses [maxFrameLength]int16
		var sum [maxNbShellBlocks]int
		for i := range (frameLength + 15) / 16 {
			if r.IntN(3) == 0 {
				continue
			}
			for j := range 16 {
				if r.IntN(3) == 0 {
					pulses[16*i+j] = int16(r.IntN(17408))
					if r.IntN(2) == 0 {
						pulses[16*i+j] = int16(r.IntN(4))
					}
				}
			}
			sum[i] = 1 + r.IntN(40)
		}
		data := make([]byte, 1+r.IntN(100))
		for i := range data {
			data[i] = byte(r.Uint32())
		}
		signalType, offsetType := r.IntN(3), r.IntN(2)
		var a, b entcode.Decoder
		a.Init(data)
		b.Init(data)
		got, want := pulses, pulses
		decodeSigns(&a, got[:], frameLength, signalType, offsetType, &sum)
		decodeSignsRef(&b, want[:], frameLength, signalType, offsetType, &sum)
		if got != want || a.Rng() != b.Rng() || a.Tell() != b.Tell() {
			t.Fatal("signs or range decoder state differ")
		}
	}
}

// decodeSplitRef and shellDecoderRef are decode_split and
// silk_shell_decoder as libopus writes them.
func decodeSplitRef(pChild1, pChild2 *int16, psRangeDec *entcode.Decoder, p int, shellTable []uint8) {
	if p > 0 {
		*pChild1 = int16(psRangeDec.DecICDF(shellTable[shellCodeTableOffsets[p]:], 8))
		*pChild2 = int16(p) - *pChild1
	} else {
		*pChild1 = 0
		*pChild2 = 0
	}
}

func shellDecoderRef(pulses0 []int16, psRangeDec *entcode.Decoder, pulses4 int) {
	var pulses3 [2]int16
	var pulses2 [4]int16
	var pulses1 [8]int16
	decodeSplitRef(&pulses3[0], &pulses3[1], psRangeDec, pulses4, shellCodeTable3[:])
	decodeSplitRef(&pulses2[0], &pulses2[1], psRangeDec, int(pulses3[0]), shellCodeTable2[:])
	decodeSplitRef(&pulses1[0], &pulses1[1], psRangeDec, int(pulses2[0]), shellCodeTable1[:])
	decodeSplitRef(&pulses0[0], &pulses0[1], psRangeDec, int(pulses1[0]), shellCodeTable0[:])
	decodeSplitRef(&pulses0[2], &pulses0[3], psRangeDec, int(pulses1[1]), shellCodeTable0[:])
	decodeSplitRef(&pulses1[2], &pulses1[3], psRangeDec, int(pulses2[1]), shellCodeTable1[:])
	decodeSplitRef(&pulses0[4], &pulses0[5], psRangeDec, int(pulses1[2]), shellCodeTable0[:])
	decodeSplitRef(&pulses0[6], &pulses0[7], psRangeDec, int(pulses1[3]), shellCodeTable0[:])
	decodeSplitRef(&pulses2[2], &pulses2[3], psRangeDec, int(pulses3[1]), shellCodeTable2[:])
	decodeSplitRef(&pulses1[4], &pulses1[5], psRangeDec, int(pulses2[2]), shellCodeTable1[:])
	decodeSplitRef(&pulses0[8], &pulses0[9], psRangeDec, int(pulses1[4]), shellCodeTable0[:])
	decodeSplitRef(&pulses0[10], &pulses0[11], psRangeDec, int(pulses1[5]), shellCodeTable0[:])
	decodeSplitRef(&pulses1[6], &pulses1[7], psRangeDec, int(pulses2[3]), shellCodeTable1[:])
	decodeSplitRef(&pulses0[12], &pulses0[13], psRangeDec, int(pulses1[6]), shellCodeTable0[:])
	decodeSplitRef(&pulses0[14], &pulses0[15], psRangeDec, int(pulses1[7]), shellCodeTable0[:])
}

func TestShellDecoderMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(33, 34))
	for range 200000 {
		data := make([]byte, 1+r.IntN(24))
		for i := range data {
			data[i] = byte(r.Uint32())
		}
		pulses4 := []int{0, 1, 1, 2, 2, 3, 4, r.IntN(silkMaxPulses + 1)}[r.IntN(8)]
		var a, b entcode.Decoder
		a.Init(data)
		b.Init(data)
		var got, want [shellCodecFrameLength]int16
		for i := range got {
			got[i], want[i] = int16(r.Uint32()), int16(r.Uint32())
		}
		// Several blocks in a row, so the decoder state carries over.
		for range 3 {
			shellDecoder(got[:], &a, pulses4)
			shellDecoderRef(want[:], &b, pulses4)
			if got != want || a.Rng() != b.Rng() || a.Tell() != b.Tell() {
				t.Fatalf("pulses4 %d: %v, want %v (or the range decoder state differs)", pulses4, got, want)
			}
		}
	}
}

// ltpSynthesisRef is the long-term prediction loop of silk_decode_core as
// libopus writes it.
func ltpSynthesisRef(sLTPQ15 []int32, predLag, bufIdx int, presQ14, excQ14 []int32, bQ14 *[ltpOrder]int16) {
	b0, b1, b2, b3, b4 := int32(bQ14[0]), int32(bQ14[1]), int32(bQ14[2]), int32(bQ14[3]), int32(bQ14[4])
	for i := range presQ14 {
		ltpPredQ13 := int32(2)
		ltpPredQ13 = smlawb(ltpPredQ13, sLTPQ15[predLag], b0)
		ltpPredQ13 = smlawb(ltpPredQ13, sLTPQ15[predLag-1], b1)
		ltpPredQ13 = smlawb(ltpPredQ13, sLTPQ15[predLag-2], b2)
		ltpPredQ13 = smlawb(ltpPredQ13, sLTPQ15[predLag-3], b3)
		ltpPredQ13 = smlawb(ltpPredQ13, sLTPQ15[predLag-4], b4)
		predLag++
		presQ14[i] = excQ14[i] + ltpPredQ13<<1
		sLTPQ15[bufIdx] = presQ14[i] << 1
		bufIdx++
	}
}

func TestLTPSynthesisMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(37, 38))
	for range 20000 {
		n := []int{40, 60, 80, 1 + r.IntN(maxSubFrameLength)}[r.IntN(4)]
		// Short lags, down to 1, read outputs of the same subframe.
		lag := []int{1, 2, 3, 4, 5, 6, 16 + r.IntN(300), 1 + r.IntN(20)}[r.IntN(8)]
		bufIdx := maxLTPMemLength
		predLag := bufIdx - lag + ltpOrder/2
		state := make([]int32, maxLTPMemLength+maxFrameLength)
		for i := range bufIdx {
			state[i] = randInt32(r)
		}
		exc := make([]int32, n)
		for i := range exc {
			exc[i] = randInt32(r)
		}
		var b [ltpOrder]int16
		for i := range b {
			b[i] = int16(r.Uint32())
		}
		got, want := slices.Clone(state), slices.Clone(state)
		resGot, resWant := make([]int32, n), make([]int32, n)
		ltpSynthesis(got, predLag, bufIdx, resGot, exc, &b)
		ltpSynthesisRef(want, predLag, bufIdx, resWant, exc, &b)
		if !slices.Equal(got, want) || !slices.Equal(resGot, resWant) {
			t.Fatalf("n %d lag %d: output differs", n, lag)
		}
	}
}
