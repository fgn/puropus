package silk

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// vectorLine is one line of a reference file of tools/libopus-ref/silk_ref.c
// (the format is documented there), split into fields after its kind.
type vectorLine struct {
	line   int
	fields []string
	pos    int
}

// readVectors returns the lines of testdata/name by kind.
func readVectors(t *testing.T, name string) map[string][]*vectorLine {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]*vectorLine{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 1<<22)
	for n := 1; sc.Scan(); n++ {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		out[fields[0]] = append(out[fields[0]], &vectorLine{line: n, fields: fields[1:]})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// next returns the next field as an int.
func (v *vectorLine) next(t *testing.T) int {
	t.Helper()
	if v.pos >= len(v.fields) {
		t.Fatalf("line %d: too few fields", v.line)
	}
	n, err := strconv.Atoi(v.fields[v.pos])
	if err != nil {
		t.Fatalf("line %d: %v", v.line, err)
	}
	v.pos++
	return n
}

// ints returns the next n fields as ints.
func (v *vectorLine) ints(t *testing.T, n int) []int {
	t.Helper()
	out := make([]int, n)
	for i := range out {
		out[i] = v.next(t)
	}
	return out
}

func (v *vectorLine) int16s(t *testing.T, n int) []int16 {
	t.Helper()
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(v.next(t))
	}
	return out
}

func (v *vectorLine) int32s(t *testing.T, n int) []int32 {
	t.Helper()
	out := make([]int32, n)
	for i := range out {
		out[i] = int32(v.next(t))
	}
	return out
}

// bytes returns a byte string written as a count and hex digits ("-" when
// empty).
func (v *vectorLine) bytes(t *testing.T) []byte {
	t.Helper()
	n := v.next(t)
	s := v.fields[v.pos]
	v.pos++
	if n == 0 {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != n {
		t.Fatalf("line %d: bad byte string", v.line)
	}
	return b
}

// done fails unless every field was consumed.
func (v *vectorLine) done(t *testing.T) {
	t.Helper()
	if v.pos != len(v.fields) {
		t.Fatalf("line %d: %d unread fields", v.line, len(v.fields)-v.pos)
	}
}

func checkSlice[T comparable](t *testing.T, v *vectorLine, what string, got, want []T) bool {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("line %d: %s = %v, libopus %v", v.line, what, got, want)
		return false
	}
	return true
}

// unitVectors holds testdata/units.txt, read once.
var unitVectors map[string][]*vectorLine

func units(t *testing.T, kind string) []*vectorLine {
	t.Helper()
	if unitVectors == nil {
		unitVectors = readVectors(t, "units.txt")
	}
	lines := unitVectors[kind]
	if len(lines) == 0 {
		t.Fatalf("no %s vectors", kind)
	}
	for _, v := range lines {
		v.pos = 0
	}
	return lines
}

// TestNLSF2A compares silk_NLSF2A on random, unsorted, clustered and edge
// NLSF vectors of orders 10 and 16.
func TestNLSF2A(t *testing.T) {
	for _, v := range units(t, "nlsf2a") {
		d := v.next(t)
		nlsf := v.int16s(t, d)
		want := v.int16s(t, d)
		v.done(t)
		got := make([]int16, d)
		nlsf2A(got, nlsf, d)
		checkSlice(t, v, "a_Q12", got, want)
	}
}

// TestNLSFDecode compares silk_NLSF_decode (unpacking, residual
// dequantization, stabilization) on random codebook paths of both codebooks.
func TestNLSFDecode(t *testing.T) {
	cbs := [2]*nlsfCB{&nlsfCBNBMB, &nlsfCBWB}
	for _, v := range units(t, "nlsfdec") {
		cb := cbs[v.next(t)]
		order := int(cb.order)
		ind := make([]int8, order+1)
		for i := range ind {
			ind[i] = int8(v.next(t))
		}
		want := v.int16s(t, order)
		v.done(t)
		got := make([]int16, order)
		nlsfDecode(got, ind, cb)
		checkSlice(t, v, "NLSF_Q15", got, want)
	}
}

// TestNLSFStabilize compares silk_NLSF_stabilize with the codebooks' and
// random minimum distances, including inputs that take its fallback.
func TestNLSFStabilize(t *testing.T) {
	for _, v := range units(t, "stabilize") {
		l := v.next(t)
		dmin := v.int16s(t, l+1)
		got := v.int16s(t, l)
		want := v.int16s(t, l)
		v.done(t)
		nlsfStabilize(got, dmin, l)
		checkSlice(t, v, "NLSF_Q15", got, want)
	}
}

// TestLPCInversePredGain compares silk_LPC_inverse_pred_gain on random
// coefficients of orders 1 to 24, filters from NLSFs and unstable ones.
func TestLPCInversePredGain(t *testing.T) {
	for _, v := range units(t, "invgain") {
		order := v.next(t)
		a := v.int16s(t, order)
		want := int32(v.next(t))
		v.done(t)
		if got := lpcInversePredGain(a, order); got != want {
			t.Errorf("line %d: %d, libopus %d", v.line, got, want)
		}
	}
}

// TestGainsDequant compares silk_gains_dequant for independent and
// conditional coding of 2 and 4 subframes.
func TestGainsDequant(t *testing.T) {
	for _, v := range units(t, "gains") {
		cond := v.next(t) != 0
		nb := v.next(t)
		prev := int8(v.next(t))
		var ind [maxNbSubfr]int8
		for i := range nb {
			ind[i] = int8(v.next(t))
		}
		wantPrev := int8(v.next(t))
		want := v.int32s(t, nb)
		v.done(t)
		var g [maxNbSubfr]int32
		gainsDequant(&g, &ind, &prev, cond, nb)
		checkSlice(t, v, "gains", g[:nb], want)
		if prev != wantPrev {
			t.Errorf("line %d: prev_ind %d, libopus %d", v.line, prev, wantPrev)
		}
	}
}

// TestDecodePitch compares silk_decode_pitch for every contour of every rate
// and frame size, with lag indices up to and beyond the coded range.
func TestDecodePitch(t *testing.T) {
	for _, v := range units(t, "pitch") {
		fs, nb := v.next(t), v.next(t)
		lagIndex, contour := int16(v.next(t)), int8(v.next(t))
		want := v.ints(t, nb)
		v.done(t)
		got := make([]int, nb)
		decodePitch(lagIndex, contour, got, fs, nb)
		checkSlice(t, v, "pitch lags", got, want)
	}
}

// TestShellDecoder decodes blocks of 16 pulses that libopus's shell encoder
// coded, and compares the pulses and the range decoder state with libopus's
// shell decoder.
func TestShellDecoder(t *testing.T) {
	for _, v := range units(t, "shell") {
		n := v.next(t)
		sums := v.ints(t, n)
		data := v.bytes(t)
		want := v.int16s(t, n*shellCodecFrameLength)
		wantTell, wantRng := v.next(t), uint32(v.next(t))
		v.done(t)
		var dec entcode.Decoder
		dec.Init(data)
		got := make([]int16, n*shellCodecFrameLength)
		for b := range n {
			shellDecoder(got[b*shellCodecFrameLength:], &dec, sums[b])
		}
		checkSlice(t, v, "pulses", got, want)
		if dec.Tell() != wantTell || dec.Rng() != wantRng {
			t.Errorf("line %d: tell %d rng %d, libopus %d %d", v.line, dec.Tell(), dec.Rng(), wantTell, wantRng)
		}
	}
}

// TestDecodePulses decodes frames of excitation pulses that libopus's
// silk_encode_pulses coded (all signal types, offsets and frame lengths,
// pulses large enough for LSB coding) and compares them and the range
// decoder state with libopus's silk_decode_pulses.
func TestDecodePulses(t *testing.T) {
	for _, v := range units(t, "pulses") {
		signalType, offset, length := v.next(t), v.next(t), v.next(t)
		data := v.bytes(t)
		len16 := (length + shellCodecFrameLength - 1) &^ (shellCodecFrameLength - 1)
		want := v.int16s(t, len16)
		wantTell, wantRng := v.next(t), uint32(v.next(t))
		v.done(t)
		var dec entcode.Decoder
		dec.Init(data)
		var got [maxFrameLength]int16
		decodePulses(&dec, got[:], signalType, offset, length)
		checkSlice(t, v, "pulses", got[:len16], want)
		if dec.Tell() != wantTell || dec.Rng() != wantRng {
			t.Errorf("line %d: tell %d rng %d, libopus %d %d", v.line, dec.Tell(), dec.Rng(), wantTell, wantRng)
		}
	}
}

// TestStereoMSToLR compares silk_stereo_MS_to_LR on random quiet and loud
// signals (with saturation), quantized and arbitrary predictors.
func TestStereoMSToLR(t *testing.T) {
	for _, v := range units(t, "ms2lr") {
		fs, length := v.next(t), v.next(t)
		pred := [2]int32{int32(v.next(t)), int32(v.next(t))}
		var st stereoDecState
		copy(st.predPrevQ13[:], v.int16s(t, 2))
		copy(st.sMid[:], v.int16s(t, 2))
		copy(st.sSide[:], v.int16s(t, 2))
		x1 := v.int16s(t, length+2)
		x2 := v.int16s(t, length+2)
		want1 := v.int16s(t, length+2)
		want2 := v.int16s(t, length+2)
		wantState := v.int16s(t, 6)
		v.done(t)
		stereoMSToLR(&st, x1, x2, &pred, fs, length)
		checkSlice(t, v, "x1", x1, want1)
		checkSlice(t, v, "x2", x2, want2)
		gotState := slices.Concat(st.predPrevQ13[:], st.sMid[:], st.sSide[:])
		checkSlice(t, v, "state", gotState, wantState)
	}
}

// TestResampler runs silk_resampler from every internal rate to every
// output rate up to 48 kHz on a chirp with noise and clipping, in chunks of
// 10 and 20 ms.
func TestResampler(t *testing.T) {
	for _, v := range units(t, "resample") {
		fsIn, fsOut, nchunks := v.next(t), v.next(t), v.next(t)
		chunks := v.ints(t, nchunks)
		total := 0
		for _, c := range chunks {
			total += c
		}
		x := v.int16s(t, total)
		want := v.int16s(t, total*fsOut/fsIn)
		v.done(t)
		var s resamplerState
		if err := resamplerInit(&s, int32(fsIn), int32(fsOut)); err != nil {
			t.Fatalf("line %d: %v", v.line, err)
		}
		got := make([]int16, len(want))
		in, out := 0, 0
		for _, c := range chunks {
			resampler(&s, got[out:], x[in:], c)
			in += c
			out += c * fsOut / fsIn
		}
		checkSlice(t, v, "output", got, want)
	}
}

// silkUnits is testdata/silk-units.json at the repository root: libopus
// SILK values (see its "source" field and testdata/manifest.json).
type silkUnits struct {
	// Silk is the SILK synthesis of hybridAnchorPacket's SILK layer as
	// silk_Decode passes it to the resampler.
	Silk       []int16 `json:"silk"`
	Resamplers []struct {
		Rate   int     `json:"rate"`
		Input  []int16 `json:"input"`
		Output []int16 `json:"output"`
	} `json:"resamplers"`
}

func readSilkUnits(t *testing.T) silkUnits {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "silk-units.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref silkUnits
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestSilkUnitsSynthesis decodes the SILK layer of the hybrid packet of
// opus/decoder_test.go (hybridAnchorPacket: 20 ms mono FB, configuration
// 15, an inactive WB SILK frame) on a fresh decoder and compares the 16 kHz
// synthesis, as silk_Decode hands it to the resampler, with libopus's
// (silk-units.json "silk").
func TestSilkUnitsSynthesis(t *testing.T) {
	ref := readSilkUnits(t)
	packet := []byte{
		0x78,
		0x0b, 0xe8, 0x3e, 0xcf, 0xb5, 0xce, 0x77, 0xd9,
		0x71, 0x55, 0x20, 0xcb, 0x41, 0x34, 0x7d, 0x80,
		0xc1, 0x9a, 0xdc, 0x78, 0xb0, 0x88, 0x29, 0xb9,
		0xeb, 0x38, 0x44, 0xab, 0xb9, 0x95, 0x62, 0x9b,
		0xae, 0x55, 0xb2, 0x41, 0x99, 0xb6, 0xae, 0x37,
		0x7b, 0x44, 0x87, 0x2b, 0x8a, 0x39, 0x78, 0x52,
		0x9c, 0xa5, 0x89, 0xbf,
	}
	var d Decoder
	d.Init()
	ctl := DecControl{NChannelsAPI: 1, NChannelsInternal: 1, APISampleRate: 48000, InternalSampleRate: 16000, PayloadSizeMs: 20}
	var dec entcode.Decoder
	dec.Init(packet[1:])
	var pcm [960]int16
	if n, err := d.Decode(&ctl, FlagDecodeNormal, true, &dec, pcm[:]); n != 960 || err != nil {
		t.Fatalf("Decode: %d, %v", n, err)
	}
	// samplesOut1Tmp holds the frame after one sample of stereo history.
	if got := d.samplesOut1Tmp[0][1 : 1+len(ref.Silk)]; !slices.Equal(got, ref.Silk) {
		t.Errorf("synthesis differs from libopus:\n got %v\nwant %v", got, ref.Silk)
	}
}

// TestResamplerUnits replays the libopus resampler runs of
// testdata/silk-units.json at the repository root (each internal rate to 48
// kHz), as one call and in 10 ms chunks.
func TestResamplerUnits(t *testing.T) {
	ref := readSilkUnits(t)
	if len(ref.Resamplers) != 3 {
		t.Fatalf("%d resampler runs", len(ref.Resamplers))
	}
	for _, r := range ref.Resamplers {
		for _, chunk := range []int{len(r.Input), r.Rate / 100} {
			var s resamplerState
			if err := resamplerInit(&s, int32(r.Rate), 48000); err != nil {
				t.Fatal(err)
			}
			got := make([]int16, len(r.Output))
			out := 0
			for in := 0; in < len(r.Input); in += chunk {
				resampler(&s, got[out:], r.Input[in:], chunk)
				out += chunk * 48000 / r.Rate
			}
			if !slices.Equal(got, r.Output) {
				t.Errorf("%d Hz in chunks of %d: output differs from libopus", r.Rate, chunk)
			}
		}
	}
}

// TestResamplerRates checks the rate pairs silk_resampler_init accepts for
// the decoder.
func TestResamplerRates(t *testing.T) {
	var s resamplerState
	for _, in := range []int32{8000, 12000, 16000} {
		for _, out := range []int32{8000, 12000, 16000, 24000, 48000} {
			err := resamplerInit(&s, in, out)
			if (err == nil) != (out >= in) {
				t.Errorf("resamplerInit(%d, %d): %v", in, out, err)
			}
		}
	}
	for _, in := range []int32{24000, 48000, 44100} {
		if resamplerInit(&s, in, 48000) == nil {
			t.Errorf("resamplerInit(%d, 48000) accepted", in)
		}
	}
}
