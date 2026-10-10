package puropus

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fgn/puropus/opus"
)

// gateRMS and gateMax bound the RMS and peak difference from a reference
// decode: libopus float output for every fixture at 48 kHz, and FFmpeg's
// 48-to-16 kHz resampling of it at 16 kHz. All fixtures are synthetic; the
// official Opus vectors have their own gate in the opus package.
const (
	gateRMS = 1e-6
	gateMax = 1e-4
)

const ms = time.Millisecond

// fixtures are the committed streams with 16 kHz and 48 kHz reference
// decodes.
var fixtures = []string{
	"ogg-silk-wb.opus", "ogg-hybrid-swb.opus", "ogg-celt-fb.opus", "ogg-celt-fb-10ms.opus",
	"ogg-silk-wb-fec.opus", "ogg-hybrid-fb-fec.opus",
	"ogg-silk-nb.opus", "ogg-silk-mb.opus", "ogg-silk-wb-40ms.opus", "ogg-silk-wb-60ms.opus",
	"ogg-silk-nb-60ms.opus", "ogg-silk-wb-10ms.opus", "ogg-silk-mb-10ms.opus",
	"ogg-hybrid-swb-10ms.opus", "ogg-hybrid-fb-10ms.opus",
	"ogg-silk-nb-10ms.opus", "ogg-silk-nb-40ms.opus", "ogg-silk-mb-40ms.opus", "ogg-silk-mb-60ms.opus",
	"ogg-silk-nb-fec.opus", "ogg-silk-mb-fec.opus", "ogg-silk-wb-60ms-fec.opus",
	"webm-celt-20ms.webm", "webm-celt-40ms.webm", "webm-celt-60ms.webm",
}

// silkFixture reports whether a fixture carries SILK or hybrid packets; the
// others are CELT.
func silkFixture(name string) bool {
	return strings.HasPrefix(name, "ogg-silk-") || strings.HasPrefix(name, "ogg-hybrid-")
}

// readGolden48kPCM reads the complete libopus float decode of a fixture at
// 48 kHz, after pre-skip and end trimming: <stem>.48k.f32le for the SILK and
// hybrid fixtures, <stem>.libopus48.f32le (tools/libopus-ref/gen-goldens.sh)
// for the CELT ones.
func readGolden48kPCM(t testing.TB, name string) []float32 {
	if silkFixture(name) {
		return readPCMGolden(t, stem(name)+".48k.f32le")
	}
	return readPCMGolden(t, stem(name)+".libopus48.f32le")
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// sourceFor reads the packets of a fixture without decoding it.
func sourceFor(t *testing.T, name string) Source {
	t.Helper()
	src, err := ReadPackets(bytes.NewReader(readFixture(t, name)))
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func stem(name string) string { return strings.TrimSuffix(name, filepath.Ext(name)) }

func readGolden16k(t testing.TB, name string) []float32 {
	return readPCMGolden(t, stem(name)+".16k.f32le")
}

// readPCMGolden reads a fixture of little-endian float32 samples.
func readPCMGolden(t testing.TB, name string) []float32 {
	t.Helper()
	raw := readFixture(t, name)
	if len(raw) == 0 || len(raw)%4 != 0 {
		t.Fatalf("invalid float32 golden %s: %d bytes", name, len(raw))
	}
	return float32sLE(raw)
}

// float32sLE decodes little-endian float32 samples.
func float32sLE(raw []byte) []float32 {
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}

// checkpoints holds every Stride-th sample of a 48 kHz decode and its total
// length: of libopus for the SILK and hybrid fixtures, and of FFmpeg's native
// decoder for the CELT fixtures, which only a secondary comparison uses.
type checkpoints struct {
	Samples int       `json:"samples"`
	Stride  int       `json:"stride"`
	Values  []float32 `json:"values"`
}

func readGolden48k(t testing.TB, name string) checkpoints {
	t.Helper()
	var cp checkpoints
	if err := json.Unmarshal(readFixture(t, stem(name)+".48k.json"), &cp); err != nil {
		t.Fatal(err)
	}
	return cp
}

// decodeAll decodes r to the end and returns the PCM and the decoder.
func decodeAll(t testing.TB, r io.Reader, opts DecodeOptions) ([]float32, *Decoder) {
	t.Helper()
	dec, err := NewDecoder(r, opts)
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := readAll(dec, make([]float32, 4096))
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return pcm, dec
}

// readAll reads from d in reads of len(buf) until it fails, and returns the
// samples and the error that ended the stream.
func readAll(d *Decoder, buf []float32) ([]float32, error) {
	var pcm []float32
	for {
		n, err := d.Read(buf)
		pcm = append(pcm, buf[:n]...)
		if err != nil {
			return pcm, err
		}
	}
}

// invalidHybridPacket returns a packet of the given 48 kHz duration that
// parses but cannot be decoded: the TOC and code 3 CBR framing are valid, but
// each 10 ms hybrid frame declares 220 bytes of CELT redundancy inside a
// 51-byte frame. The payload is invalid for mono and stereo decoders alike.
func invalidHybridPacket(t *testing.T, samples int) []byte {
	t.Helper()
	payload, err := hex.DecodeString("3fc0d5d41550fd4f9ed4d013e1caaeb47059f3219f26ccd99dd76bc80aa2e84cf66451135f30abc8a958ebbf964b271ac0d785")
	if err != nil {
		t.Fatal(err)
	}
	if samples <= 0 || samples%480 != 0 || samples > 5760 {
		t.Fatalf("invalid test duration %d", samples)
	}
	packet := []byte{14<<3 | 3, byte(samples / 480)}
	for i := 0; i < samples/480; i++ {
		packet = append(packet, payload...)
	}
	parsed, err := opus.Parse(packet, false)
	if err != nil || parsed.Duration48() != samples {
		t.Fatalf("invalid test framing: %v", err)
	}
	return packet
}

// compare checks got against a reference decode within gateRMS and gateMax.
func compare(t *testing.T, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sample count %d, want %d", len(got), len(want))
	}
	var sum float64
	worst := 0.0
	for i := range want {
		if math.IsNaN(float64(got[i])) || math.IsInf(float64(got[i]), 0) || math.IsNaN(float64(want[i])) || math.IsInf(float64(want[i]), 0) {
			t.Fatalf("non-finite sample %d: got %v, want %v", i, got[i], want[i])
		}
		d := math.Abs(float64(got[i] - want[i]))
		sum += d * d
		worst = math.Max(worst, d)
	}
	rms := math.Sqrt(sum / float64(len(want)))
	if rms > gateRMS || worst > gateMax {
		t.Fatalf("rms %.3g (gate %.0e), max %.3g (gate %.0e)", rms, gateRMS, worst, gateMax)
	}
	t.Logf("samples=%d rms=%.3g max=%.3g", len(want), rms, worst)
}

// maxAbsDiff returns the largest absolute difference between a and the
// first len(a) samples of b.
func maxAbsDiff(a, b []float32) float64 {
	d := 0.0
	for i := range a {
		if x := math.Abs(float64(a[i] - b[i])); x > d {
			d = x
		}
	}
	return d
}

func TestDecodeMatchesReferenceAt16kHz(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			got, dec := decodeAll(t, bytes.NewReader(readFixture(t, name)), DecodeOptions{SampleRate: 16000})
			want := readGolden16k(t, name)
			if len(got) != len(want) {
				t.Fatalf("sample count %d, want %d", len(got), len(want))
			}
			if dec.Position() != time.Duration(len(got))*time.Second/16000 {
				t.Fatalf("position %v for %d samples", dec.Position(), len(got))
			}
			compare(t, got, want)
		})
	}
}

func TestDecodeMatchesReferenceAt48kHz(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			got, _ := decodeAll(t, bytes.NewReader(readFixture(t, name)), DecodeOptions{})
			cp := readGolden48k(t, name)
			if len(got) != cp.Samples {
				t.Fatalf("sample count %d, want %d", len(got), cp.Samples)
			}
			if !silkFixture(name) {
				logCheckpointDistance(t, "FFmpeg native CELT decoder", got, cp)
			}
			compare(t, got, readGolden48kPCM(t, name))
		})
	}
}

// logCheckpointDistance reports, without gating, how far got is from the
// checkpoints of another decoder. The CELT fixtures keep FFmpeg's native
// decode as such a secondary black-box comparison; libopus is the reference.
func logCheckpointDistance(t *testing.T, decoder string, got []float32, cp checkpoints) {
	t.Helper()
	var sum float64
	worst := 0.0
	for i, v := range cp.Values {
		d := math.Abs(float64(got[i*cp.Stride] - v))
		sum += d * d
		worst = math.Max(worst, d)
	}
	t.Logf("%s, %d checkpoints (not gated): rms=%.3g max=%.3g", decoder, len(cp.Values), math.Sqrt(sum/float64(len(cp.Values))), worst)
}

func TestWebMUsesOpusHeadPreSkipWithoutCodecDelay(t *testing.T) {
	data := append([]byte(nil), readFixture(t, "webm-celt-60ms.webm")...)
	headAt := bytes.Index(data, []byte("OpusHead"))
	if headAt < 0 {
		t.Fatal("fixture has no OpusHead")
	}
	const preSkip = 312
	binary.LittleEndian.PutUint16(data[headAt+10:], preSkip)

	pcm, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	if dec.Header().PreSkip != preSkip {
		t.Fatalf("pre-skip %d, want OpusHead value %d", dec.Header().PreSkip, preSkip)
	}
	cp := readGolden48k(t, "webm-celt-60ms.webm")
	if got, want := len(pcm), cp.Samples-preSkip; got != want {
		t.Fatalf("sample count %d, want %d after pre-skip", got, want)
	}
}

func TestDecoderPositionBeyondDurationMultiplicationLimit(t *testing.T) {
	const want = 54 * time.Hour
	dec := Decoder{delivered: 54 * 60 * 60 * SampleRate, rate: SampleRate}
	if got := dec.Position(); got != want {
		t.Fatalf("position %v, want %v", got, want)
	}
}

// TestFixturesCoverAllModes locks in that the committed fixtures exercise
// SILK, hybrid, and CELT decoding.
func TestFixturesCoverAllModes(t *testing.T) {
	seen := map[opus.Mode]string{}
	configs := map[int]int{}
	for _, name := range fixtures {
		src, err := ReadPackets(bytes.NewReader(readFixture(t, name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range src.Packets {
			parsed, err := opus.Parse(p.Data, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := seen[parsed.Mode]; !ok {
				seen[parsed.Mode] = name
			}
			configs[parsed.Config] += parsed.FrameCount
		}
	}
	for _, mode := range []opus.Mode{opus.ModeSILK, opus.ModeHybrid, opus.ModeCELT} {
		if _, ok := seen[mode]; !ok {
			t.Errorf("no fixture exercises mode %d", mode)
		}
	}
	for config := 0; config < 16; config++ {
		if configs[config] < 10 {
			t.Errorf("SILK/hybrid config %d has only %d frames; want at least 10", config, configs[config])
		}
	}
	t.Logf("modes: %v", seen)
	t.Logf("frames per configuration: %v", configs)
}

// chunkReader returns at most size bytes per Read call, so the decoder must
// cope with input as fragmented as a network stream delivers it.
type chunkReader struct {
	data []byte
	size int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), c.size)], c.data)
	c.data = c.data[n:]
	return n, nil
}

// envStreams returns the recordings named by PUROPUS_STREAMS, a
// filepath.ListSeparator-separated list of globs, in sorted order.
func envStreams(tb testing.TB) []string {
	tb.Helper()
	var paths []string
	for _, pattern := range filepath.SplitList(os.Getenv("PUROPUS_STREAMS")) {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			tb.Fatal(err)
		}
		slices.Sort(matches)
		paths = append(paths, matches...)
	}
	return paths
}

// TestStreamingInputProducesIdenticalOutput requires bit-identical PCM
// whatever size the input arrives in, from single bytes to the whole file.
// Set PUROPUS_STREAMS to include longer recordings.
func TestStreamingInputProducesIdenticalOutput(t *testing.T) {
	paths := []string{filepath.Join("testdata", "ogg-celt-fb.opus"), filepath.Join("testdata", "webm-celt-60ms.webm")}
	for _, path := range append(paths, envStreams(t)...) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		whole, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: 16000})
		for _, size := range []int{1, 7, 4096, 32768} {
			got, _ := decodeAll(t, &chunkReader{data: data, size: size}, DecodeOptions{SampleRate: 16000})
			if len(got) != len(whole) {
				t.Fatalf("%s in %d-byte reads: %d vs %d samples", path, size, len(got), len(whole))
			}
			for i := range whole {
				if math.Float32bits(got[i]) != math.Float32bits(whole[i]) {
					t.Fatalf("%s in %d-byte reads: sample %d differs", path, size, i)
				}
			}
		}
	}
}

func TestTruncatedInputReturnsDecodedPrefixThenErrTruncated(t *testing.T) {
	for _, name := range []string{"ogg-celt-fb.opus", "webm-celt-60ms.webm"} {
		data := readFixture(t, name)
		cut := data[:len(data)*2/3]
		dec, err := NewDecoder(bytes.NewReader(cut), DecodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pcm, err := readAll(dec, make([]float32, 4096))
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("%s: want ErrTruncated, got %v", name, err)
		}
		if len(pcm) < 48000 {
			t.Fatalf("%s: only %d samples decoded before truncation", name, len(pcm))
		}
	}
}

func TestTolerantConcealsCorruptPacket(t *testing.T) {
	data := readFixture(t, "webm-celt-60ms.webm")
	src, err := ReadPackets(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	whole, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
	if err != nil {
		t.Fatal(err)
	}
	whole.Packets[10].Data = invalidHybridPacket(t, whole.Packets[10].Duration)
	var corrupt bytes.Buffer
	if err := whole.Write(&corrupt, FormatWebM); err != nil {
		t.Fatal(err)
	}
	strict, err := NewDecoder(bytes.NewReader(corrupt.Bytes()), DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, strictErr := readAll(strict, make([]float32, 1<<16))
	if errors.Is(strictErr, io.EOF) {
		t.Fatal("strict decoder accepted the undecodable packet")
	}
	if errors.Is(strictErr, ErrTruncated) {
		t.Fatalf("strict decoder reported truncation instead of a decode error: %v", strictErr)
	}
	tolerant, _ := decodeAll(t, bytes.NewReader(corrupt.Bytes()), DecodeOptions{Tolerant: true})
	clean, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	if len(tolerant) != len(clean) {
		t.Fatalf("tolerant decode has %d samples, clean has %d", len(tolerant), len(clean))
	}
}

func TestRejectsUnknownContainerAndEmptyInput(t *testing.T) {
	if _, err := NewDecoder(bytes.NewReader([]byte("RIFF....WAVE")), DecodeOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
	if _, err := NewDecoder(bytes.NewReader(nil), DecodeOptions{}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	if _, err := NewDecoder(bytes.NewReader(readFixture(t, "ogg-celt-fb.opus")), DecodeOptions{SampleRate: 44100}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported for 44.1 kHz, got %v", err)
	}
}

func TestReadPacketsFailsOnGarbage(t *testing.T) {
	if _, err := ReadPackets(bytes.NewReader([]byte("OggS garbage that is not a page"))); err == nil {
		t.Fatal("garbage accepted")
	}
	_, err := ReadPackets(io.LimitReader(bytes.NewReader(readFixture(t, "ogg-celt-fb.opus")), 5000))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}

func TestSourcePacketsMatchReadPackets(t *testing.T) {
	data := readFixture(t, "ogg-silk-wb.opus")
	_, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{KeepNative: true})
	fromDecoder := dec.Source()
	fromReader, err := ReadPackets(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(fromDecoder.Packets) != len(fromReader.Packets) || len(fromDecoder.Packets) == 0 {
		t.Fatalf("%d vs %d packets", len(fromDecoder.Packets), len(fromReader.Packets))
	}
	for i := range fromReader.Packets {
		a, b := fromDecoder.Packets[i], fromReader.Packets[i]
		if a.Start != b.Start || a.Duration != b.Duration || !bytes.Equal(a.Data, b.Data) {
			t.Fatalf("packet %d differs: %+v vs %+v", i, a, b)
		}
	}
	if fromReader.Header.PreSkip == 0 {
		t.Fatal("Ogg fixture is expected to carry a pre-skip")
	}
	if first := fromReader.Packets[0]; first.Start != 0 || first.Skip != fromReader.Header.PreSkip || first.Coded() != 960 {
		t.Fatalf("first packet %+v, want Start 0 and Skip %d of 960 coded samples", first, fromReader.Header.PreSkip)
	}
	if got, want := len(fromDecoder.Native), int(fromDecoder.Duration()*SampleRate/time.Second); got != want {
		t.Fatalf("native PCM has %d samples for a %v source", got, fromDecoder.Duration())
	}
}

// Source describes exactly the audio the decoder produces. Packets tile the
// timeline from 0, the first one absorbing the pre-skip and the last one the
// end trimming, so Duration and the span map need no correction.
func TestSourceTimelineMatchesDecodedAudio(t *testing.T) {
	for _, name := range fixtures {
		data := readFixture(t, name)
		direct, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
		src := sourceFor(t, name)
		if got := durationToSamples(src.Duration()); got != int64(len(direct)) {
			t.Fatalf("%s: Duration covers %d samples, decoder produced %d", name, got, len(direct))
		}
		cursor := int64(0)
		for i, p := range src.Packets {
			if p.Start != cursor {
				t.Fatalf("%s: packet %d starts at %d, previous ended at %d", name, i, p.Start, cursor)
			}
			parsed, err := opus.Parse(p.Data, false)
			if err != nil {
				t.Fatal(err)
			}
			if p.Coded() != parsed.Duration48() {
				t.Fatalf("%s: packet %d accounts for %d samples, coded %d", name, i, p.Coded(), parsed.Duration48())
			}
			cursor = p.End()
		}
		if src.Packets[0].Skip != src.Header.PreSkip {
			t.Fatalf("%s: first packet skips %d, header pre-skip %d", name, src.Packets[0].Skip, src.Header.PreSkip)
		}
	}
}

// TestNativeEqualsNativeRateDecode checks the retained 48 kHz PCM is exactly
// what a 48 kHz decode returns (before output gain, which these fixtures do
// not use), across the block boundaries of the retention arena.
func TestNativeEqualsNativeRateDecode(t *testing.T) {
	for _, name := range []string{"ogg-celt-fb.opus", "webm-celt-60ms.webm"} {
		data := readFixture(t, name)
		direct, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
		_, dec := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: 16000, KeepNative: true})
		native := dec.Source().Native
		if len(native) != len(direct) {
			t.Fatalf("%s: native %d samples, direct %d", name, len(native), len(direct))
		}
		for i := range direct {
			if native[i] != direct[i] {
				t.Fatalf("%s: sample %d differs", name, i)
			}
		}
		if again := dec.Source().Native; &again[0] != &native[0] {
			t.Fatalf("%s: Source() re-concatenated an unchanged native buffer", name)
		}
	}
	if Acceleration() == "" {
		t.Fatal("Acceleration is empty")
	}
}

func TestReleasePacketsBoundsRetention(t *testing.T) {
	data := readFixture(t, "webm-celt-60ms.webm") // 3 s of 60 ms packets
	dec, err := NewDecoder(bytes.NewReader(data), DecodeOptions{SampleRate: 16000, KeepNative: true})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float32, 16000) // one second per Read
	var windows []Source
	for {
		n, err := dec.Read(buf)
		if n > 0 {
			src := dec.Source()
			windows = append(windows, Source{Header: src.Header, Packets: append([]Packet(nil), src.Packets...)})
			dec.ReleasePackets(dec.Position())
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(windows) < 3 {
		t.Fatalf("expected at least 3 windows, got %d", len(windows))
	}
	// Each window holds the packets not yet released; a packet straddling the
	// boundary must survive into the next window and no packet is lost.
	seen := map[int64]bool{}
	for _, w := range windows {
		for _, p := range w.Packets {
			seen[p.Start] = true
		}
	}
	all, _ := ReadPackets(bytes.NewReader(data))
	for _, p := range all.Packets {
		if !seen[p.Start] {
			t.Fatalf("packet at %d never appeared in a window", p.Start)
		}
	}
	if left := len(dec.Source().Packets); left > 2 {
		t.Fatalf("%d packets still retained after releasing everything", left)
	}
	src := dec.Source()
	if len(src.Native) > 2*SampleRate {
		t.Fatalf("native PCM not released: %d samples", len(src.Native))
	}
	if len(src.Packets) > 0 && int64(len(src.Native)) != src.Packets[len(src.Packets)-1].End()-src.Packets[0].Start {
		t.Fatalf("native PCM has %d samples for packets covering [%d, %d)", len(src.Native), src.Packets[0].Start, src.Packets[len(src.Packets)-1].End())
	}
}

// The native PCM a Decoder retains always starts at its first retained
// packet, however ReleasePackets is paced.
func TestReleasePacketsKeepsNativeAlignedWithPackets(t *testing.T) {
	data := readFixture(t, "ogg-celt-fb.opus")
	direct, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
	dec, err := NewDecoder(bytes.NewReader(data), DecodeOptions{SampleRate: 16000, KeepNative: true})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float32, 7000) // an odd pace, so releases fall inside packets and blocks
	for {
		_, err := dec.Read(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		dec.ReleasePackets(dec.Position() - 150*ms)
		src := dec.Source()
		if len(src.Packets) == 0 {
			t.Fatal("no packets retained")
		}
		from, to := src.Packets[0].Start, src.Packets[len(src.Packets)-1].End()
		if int64(len(src.Native)) != to-from {
			t.Fatalf("native has %d samples for packets covering [%d, %d)", len(src.Native), from, to)
		}
		if d := maxAbsDiff(src.Native, direct[from:to]); d != 0 {
			t.Fatalf("native PCM from %d differs from the decode by %g", from, d)
		}
	}
}
