package celt

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// Gates on the PCM, against libopus's float decoder (the SIMD build of
// decode_float): the normal one, and the one for CELT pitch concealment and
// the frames after it, where libopus's own builds differ.
const (
	rmsGate       = 1e-6
	peakGate      = 1e-4
	plcRMSGate    = 1e-4
	plcPeakGate   = 4e-4
	repoTestdata  = "../../../testdata"
	opusTestdata  = "../../testdata"
	officialNames = "01 07 11" // the CELT-only RFC 8251 vectors
)

// exactArch reports whether the PCM must be bit-exact with libopus's
// generic C build: on amd64 and 386 Go's float32 arithmetic and the libm
// results the float build uses (exp, cos, sqrt) round as glibc's do on the
// frames tested. On other architectures the frame hashes are only reported.
func exactArch() bool {
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "386"
}

// checkFrameLogs compares the frames decoded with celt_decode's log of the
// generic build: every CELT call, its sizes, bands, channels and final range
// must match, and its PCM hash where exactArch. It returns the number of
// bit-exact frames.
func checkFrameLogs(t *testing.T, got, want []frameLog) int {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d CELT calls, want %d", len(got), len(want))
	}
	exact := 0
	for i := range want {
		g, w := got[i], want[i]
		hash := g.hash
		g.hash = w.hash
		if g != w {
			t.Fatalf("call %d: %v, want %v", i, got[i], w)
		}
		if hash == w.hash {
			exact++
		} else if exactArch() {
			t.Errorf("call %d (%s, record %d): PCM differs from libopus's generic build", i, w.kind, w.record)
		}
	}
	return exact
}

// gateResult accumulates the PCM error of frames under one gate.
type gateResult struct {
	frames        int
	worstFrameRMS float64
	peak          float64
	failures      int
}

func (g gateResult) String() string {
	return fmt.Sprintf("%d frames, worst frame RMS %.3g, peak %.3g", g.frames, g.worstFrameRMS, g.peak)
}

// add gates one frame.
func (g *gateResult) add(t *testing.T, what string, got, want []float32, rmsLimit, peakLimit float64) {
	t.Helper()
	if len(got) == 0 {
		return
	}
	rms, peak := pcmError(got, want)
	g.frames++
	g.worstFrameRMS = max(g.worstFrameRMS, rms)
	g.peak = max(g.peak, peak)
	if rms > rmsLimit || peak > peakLimit || math.IsNaN(rms) {
		g.failures++
		if g.failures <= 5 {
			t.Errorf("%s: RMS %.3g, peak %.3g, gate %g / %g", what, rms, peak, rmsLimit, peakLimit)
		}
	}
}

// gateStream checks the decoded PCM of s against want, which starts skip
// samples per channel into the decode, frame by frame: the normal gate up to
// the first pitch concealment, the concealment gate (plcRMS and plcPeak, or
// the default when 0) from there on. It returns the results of both gates.
func gateStream(t *testing.T, s *celtStream, want []float32, skip int, plcRMS, plcPeak float64) (normal, plc gateResult) {
	t.Helper()
	if plcRMS == 0 {
		plcRMS, plcPeak = plcRMSGate, plcPeakGate
	}
	ch := s.channels
	pos := 0
	inPLC := false
	for i, l := range s.logs {
		if s.pitchPLC[i] {
			inPLC = true
		}
		from := max(pos, skip)
		to := min(pos+l.samples, skip+len(want)/ch)
		pos += l.samples
		if from >= to {
			continue
		}
		got := s.pcm[from*ch : to*ch]
		ref := want[(from-skip)*ch : (to-skip)*ch]
		what := fmt.Sprintf("call %d (%s, record %d)", i, l.kind, l.record)
		if inPLC {
			plc.add(t, what, got, ref, plcRMS, plcPeak)
		} else {
			normal.add(t, what, got, ref, rmsGate, peakGate)
		}
	}
	return normal, plc
}

// fixture is a CELT fixture of the repository's testdata/.
type fixture struct {
	stem                 string
	channels, skip, trim int
}

func readFixtures(t *testing.T) []fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/fixtures.txt")
	if err != nil {
		t.Fatal(err)
	}
	var fs []fixture
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		var v [3]int
		for i := range v {
			if v[i], err = strconv.Atoi(f[i+1]); err != nil {
				t.Fatal(err)
			}
		}
		fs = append(fs, fixture{f[0], v[0], v[1], v[2]})
	}
	if len(fs) != 5 {
		t.Fatalf("%d fixtures, want 5", len(fs))
	}
	return fs
}

// TestFixturesMatchLibopus decodes the CELT fixtures of testdata/ (Ogg and
// WebM, 10 to 60 ms packets) and compares every frame's final range and PCM
// with libopus: the frame logs of the generic build exactly, and the
// container's PCM (after pre-skip and end trimming) with the goldens of
// libopus's float API within the gates.
func TestFixturesMatchLibopus(t *testing.T) {
	for _, f := range readFixtures(t) {
		t.Run(f.stem, func(t *testing.T) {
			s := newCeltStream(t, 48000, f.channels)
			if err := s.decodeRecords(readBit(t, "testdata/"+f.stem+".bit")); err != nil {
				t.Fatal(err)
			}
			exact := checkFrameLogs(t, s.logs, readFrameLog(t, fmt.Sprintf("testdata/%s.c%d.frames.txt", f.stem, f.channels)))
			gold := readF32(t, filepath.Join(repoTestdata, f.stem+".libopus48.f32le"))
			total := len(s.pcm) / f.channels
			if total-f.skip-f.trim != len(gold)/f.channels {
				t.Fatalf("%d samples after trimming, want %d", total-f.skip-f.trim, len(gold)/f.channels)
			}
			normal, plc := gateStream(t, s, gold, f.skip, 0, 0)
			if plc.frames != 0 {
				t.Fatal("unexpected concealment")
			}
			t.Logf("%d frames, final ranges match, %d bit-exact with the generic build; vs golden: %v", len(s.logs), exact, normal)
		})
	}
}

// sequence is a CELT sequence of opus/testdata: the libopus vector and the
// packet list made from it.
type sequence struct {
	name  string // testdata/<name>.bit
	json  string // the vector in opus/testdata
	mono  string // its mono PCM
	multi string // its stereo PCM, if any
	// plcRMS and plcPeak replace the concealment gate when nonzero.
	plcRMS, plcPeak float64
}

var sequences = []sequence{
	{"transition-loss-celt", "transition-vectors/loss-celt", "transition-vectors/loss-celt.f32le", "", 0, 0},
	{"transition-loss-celt-2p5ms", "transition-vectors/loss-celt-2p5ms", "transition-vectors/loss-celt-2p5ms.f32le", "", 0, 0},
	// Twelve losses in a row: libopus's generic C build, which the decoder
	// reproduces bit for bit (checkFrameLogs), is itself RMS 2.60e-4 and
	// peak 4.79e-4 away from the SIMD build's PCM on the second to fifth
	// pitch-concealed frames (decode_float of both builds of
	// tools/libopus-ref on testdata/transition-loss-celt-long.bit). The
	// difference comes from the summation order of the SSE xcorr_kernel in
	// celt_fir and celt_iir; the gate here is that distance.
	{"transition-loss-celt-long", "transition-vectors/loss-celt-long", "transition-vectors/loss-celt-long.f32le", "", 3e-4, 5e-4},
	{"stereo-loss-celt", "stereo-vectors/loss-celt", "stereo-vectors/loss-celt.mono.f32le", "stereo-vectors/loss-celt.f32le", 0, 0},
	{"stereo-loss-celt-2p5ms", "stereo-vectors/loss-celt-2p5ms", "stereo-vectors/loss-celt-2p5ms.mono.f32le", "stereo-vectors/loss-celt-2p5ms.f32le", 0, 0},
	{"stereo-loss-celt-5ms", "stereo-vectors/loss-celt-5ms", "stereo-vectors/loss-celt-5ms.mono.f32le", "stereo-vectors/loss-celt-5ms.f32le", 0, 0},
	{"stereo-channels-celt", "stereo-vectors/channels-celt", "stereo-vectors/channels-celt.mono.f32le", "stereo-vectors/channels-celt.f32le", 0, 0},
	{"stereo-transients-celt", "stereo-vectors/transients-celt", "stereo-vectors/transients-celt.mono.f32le", "stereo-vectors/transients-celt.f32le", 0, 0},
}

// TestSequencesMatchLibopus decodes libopus's CELT sequences of
// opus/testdata (losses of 2.5 to 20 ms frames in single and long runs,
// TOC-only and empty frames, mono and stereo packets, transients) to mono
// and stereo: each frame's final range, concealment included, matches the
// generic build's, and the PCM matches libopus's float API within the
// normal gate before the first pitch concealment and the concealment gate
// from there on.
func TestSequencesMatchLibopus(t *testing.T) {
	for _, seq := range sequences {
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", seq.name, channels), func(t *testing.T) {
				s := newCeltStream(t, 48000, channels)
				if err := s.decodeRecords(readBit(t, "testdata/"+seq.name+".bit")); err != nil {
					t.Fatal(err)
				}
				exact := checkFrameLogs(t, s.logs, readFrameLog(t, fmt.Sprintf("testdata/%s.c%d.frames.txt", seq.name, channels)))
				ref := seq.mono
				if channels == 2 {
					ref = seq.multi
				}
				pitch, noise := 0, 0
				for i, l := range s.logs {
					if l.kind == "plc" {
						if s.pitchPLC[i] {
							pitch++
						} else {
							noise++
						}
					}
				}
				msg := fmt.Sprintf("%d calls (%d pitch and %d noise concealment), final ranges match, %d bit-exact with the generic build",
					len(s.logs), pitch, noise, exact)
				if ref == "" {
					t.Log(msg + "; no libopus stereo PCM")
					return
				}
				want := readF32(t, filepath.Join(opusTestdata, ref))
				if len(want) != len(s.pcm) {
					t.Fatalf("%d samples, want %d", len(s.pcm)/channels, len(want)/channels)
				}
				normal, plc := gateStream(t, s, want, 0, seq.plcRMS, seq.plcPeak)
				t.Logf("%s; normal gate: %v; concealment gate: %v", msg, normal, plc)
			})
		}
	}
}

// TestSequencesFinalRange checks the final range of every packet of the
// sequences against the libopus vectors, as OPUS_GET_FINAL_RANGE reports it:
// the CELT range of the last frame, 0 after concealment.
func TestSequencesFinalRange(t *testing.T) {
	for _, seq := range sequences {
		t.Run(seq.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(opusTestdata, seq.json+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var v struct {
				Records []struct {
					Missing    bool   `json:"missing"`
					Packet     string `json:"packet_hex"`
					FrameSize  int    `json:"frame_size"`
					Result     *int   `json:"result"`
					FinalRange uint32 `json:"final_range"`
				} `json:"records"`
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			s := newCeltStream(t, 48000, 2)
			checked := 0
			for i, r := range v.Records {
				if r.Result != nil && *r.Result < 0 {
					continue // libopus rejects the packet and keeps its state
				}
				s.record = i
				calls := len(s.logs)
				p, err := hex.DecodeString(r.Packet)
				if err != nil {
					t.Fatal(err)
				}
				if r.Missing {
					err = s.conceal(r.FrameSize)
				} else {
					err = s.decodePacket(p)
				}
				if err != nil {
					t.Fatalf("record %d: %v", i, err)
				}
				last := s.logs[len(s.logs)-1]
				rng := last.rng
				if last.kind != "frame" || last.bytes <= 1 || len(s.logs) == calls {
					rng = 0
				}
				if rng != r.FinalRange {
					t.Fatalf("record %d: final range %08x, want %08x", i, rng, r.FinalRange)
				}
				checked++
			}
			t.Logf("%d packets", checked)
		})
	}
}

// TestOtherRatesMatchLibopus decodes at the other output rates of
// celt_decoder_init, where CELT downsamples itself, against the generic
// build's frame logs.
func TestOtherRatesMatchLibopus(t *testing.T) {
	for _, rate := range []int{8000, 12000, 16000, 24000} {
		for _, tc := range []struct {
			name     string
			channels int
		}{{"webm-celt-20ms", 1}, {"stereo-channels-celt", 2}} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, rate), func(t *testing.T) {
				s := newCeltStream(t, rate, tc.channels)
				if err := s.decodeRecords(readBit(t, "testdata/"+tc.name+".bit")); err != nil {
					t.Fatal(err)
				}
				exact := checkFrameLogs(t, s.logs, readFrameLog(t, fmt.Sprintf("testdata/%s.r%d.c%d.frames.txt", tc.name, rate, tc.channels)))
				t.Logf("%d calls, %d bit-exact", len(s.logs), exact)
			})
		}
	}
}

// TestHybridStartBandMatchesLibopus decodes CELT frames with start band 17,
// as the CELT layer of hybrid frames decodes (no post-filter, the folding of
// special_hybrid_folding, noise concealment only), against the generic
// build's frame logs, including losses and mono and stereo output.
func TestHybridStartBandMatchesLibopus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels int
	}{{"webm-celt-20ms", 1}, {"stereo-loss-celt", 1}, {"stereo-loss-celt", 2}} {
		t.Run(fmt.Sprintf("%s/%d", tc.name, tc.channels), func(t *testing.T) {
			s := newCeltStream(t, 48000, tc.channels)
			s.startBand = 17
			if err := s.decodeRecords(readBit(t, "testdata/"+tc.name+".bit")); err != nil {
				t.Fatal(err)
			}
			for _, pitch := range s.pitchPLC {
				if pitch {
					t.Fatal("pitch concealment with start band 17")
				}
			}
			exact := checkFrameLogs(t, s.logs, readFrameLog(t, fmt.Sprintf("testdata/%s.s17.c%d.frames.txt", tc.name, tc.channels)))
			t.Logf("%d calls, %d bit-exact", len(s.logs), exact)
		})
	}
}

// TestOfficialVectors decodes the CELT-only RFC 8251 test vectors in
// $OPUS_TEST_VECTORS to mono and stereo: every packet's final range matches
// the vector's, and, when the references of `make -C tools/libopus-ref
// celt-official-refs` exist ($CELT_OFFICIAL_REFS, by default
// tools/libopus-ref/build/celt-official), every frame's final range and
// hash matches the generic build and the PCM libopus's float API within the
// gates.
func TestOfficialVectors(t *testing.T) {
	dir := os.Getenv("OPUS_TEST_VECTORS")
	if dir == "" {
		t.Skip("set OPUS_TEST_VECTORS to the RFC 8251 opus_newvectors directory")
	}
	refs := os.Getenv("CELT_OFFICIAL_REFS")
	if refs == "" {
		refs = "../../../tools/libopus-ref/build/celt-official"
	}
	for _, n := range strings.Fields(officialNames) {
		recs := readBit(t, filepath.Join(dir, "testvector"+n+".bit"))
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", n, channels), func(t *testing.T) {
				s := newCeltStream(t, 48000, channels)
				if err := s.decodeRecords(recs); err != nil {
					t.Fatal(err)
				}
				// The final range of a packet is that of its last frame.
				last := make([]uint32, len(recs))
				for _, l := range s.logs {
					last[l.record] = l.rng
				}
				for i, r := range recs {
					if r.rng != last[i] {
						t.Fatalf("packet %d: final range %08x, want %08x", i, last[i], r.rng)
					}
				}
				msg := fmt.Sprintf("%d packets, %d frames, final ranges match", len(recs), len(s.logs))
				base := filepath.Join(refs, fmt.Sprintf("testvector%s.c%d", n, channels))
				if _, err := os.Stat(base + ".frames.txt"); err != nil {
					t.Log(msg + "; no libopus PCM references")
					return
				}
				exact := checkFrameLogs(t, s.logs, readFrameLog(t, base+".frames.txt"))
				want := readF32(t, base+".f32le")
				if len(want) != len(s.pcm) {
					t.Fatalf("%d samples, want %d", len(s.pcm)/channels, len(want)/channels)
				}
				normal, _ := gateStream(t, s, want, 0, 0, 0)
				t.Logf("%s, %d bit-exact with the generic build; vs decode_float: %v", msg, exact, normal)
			})
		}
	}
}

// TestAntiCollapseReference replays the stereo anti-collapse reference of
// opus/testdata/stereo-anticollapse: libopus's anti_collapse on two channels
// of 960 coefficients, LM 3, bands 0 to 21, seed 12345, pulses 13+7*band,
// logE -0.25*band, prev1logE logE-1-c and prev2logE logE-2+c for channel c,
// and collapse masks 0x55 and 0xaa.
func TestAntiCollapseReference(t *testing.T) {
	dir := filepath.Join(opusTestdata, "stereo-anticollapse")
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for name, sum := range manifest.Files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != sum {
			t.Fatalf("%s: SHA-256 mismatch", name)
		}
	}
	X := readF32(t, filepath.Join(dir, "anticollapse.input.f32le"))
	want := readF32(t, filepath.Join(dir, "anticollapse.output.f32le"))
	const C, LM, N = 2, 3, 960
	if len(X) != C*N || len(want) != C*N {
		t.Fatal("unexpected reference size")
	}
	m := StaticMode()
	var pulses [maxBands]int
	var logE, prev1, prev2 [2 * maxBands]float32
	var masks [C * maxBands]uint8
	for i := range maxBands {
		pulses[i] = 13 + 7*i
		for c := range C {
			logE[c*maxBands+i] = -0.25 * float32(i)
			prev1[c*maxBands+i] = logE[c*maxBands+i] - 1 - float32(c)
			prev2[c*maxBands+i] = logE[c*maxBands+i] - 2 + float32(c)
		}
		masks[i*C] = 0x55
		masks[i*C+1] = 0xaa
	}
	antiCollapse(m, X, masks[:], LM, C, N, 0, maxBands, logE[:], prev1[:], prev2[:], pulses[:], 12345)
	worst := 0.0
	for i := range want {
		d := math.Abs(float64(X[i]) - float64(want[i]))
		worst = max(worst, d)
		if d > 1e-6 {
			t.Fatalf("X[%d] = %g, want %g", i, X[i], want[i])
		}
	}
	t.Logf("largest difference %g", worst)
}

// TestEnergyOutsideCodedBands checks that after a frame the bands outside
// [start, end) hold zero energy and both older energy histories libopus's
// silence value, so that a later frame enabling those bands never
// anti-collapses against stale energy (celt_decode_with_ec).
func TestEnergyOutsideCodedBands(t *testing.T) {
	recs := readBit(t, "testdata/stereo-transients-celt.bit")
	for _, tc := range []struct{ start, end int }{{17, 21}, {0, 13}, {0, 17}, {17, 19}} {
		d, err := NewDecoder(48000, 2)
		if err != nil {
			t.Fatal(err)
		}
		pcm := make([]float32, 2*960)
		// Fill every band's history first.
		for _, r := range recs[:4] {
			frames, err := parseFrames(r.packet)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.DecodeWithEC(frames[0], pcm, 960, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err = d.SetStartBand(tc.start); err != nil {
			t.Fatal(err)
		}
		if err = d.SetEndBand(tc.end); err != nil {
			t.Fatal(err)
		}
		frames, err := parseFrames(recs[4].packet)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.DecodeWithEC(frames[0], pcm, 960, nil); err != nil {
			t.Fatal(err)
		}
		for c := range 2 {
			for i := range maxBands {
				k := c*maxBands + i
				if i >= tc.start && i < tc.end {
					continue
				}
				if d.oldBandE[k] != 0 || d.oldLogE[k] != -28 || d.oldLogE2[k] != -28 {
					t.Fatalf("bands [%d, %d): band %d channel %d: energy %g, %g, %g", tc.start, tc.end, i, c,
						d.oldBandE[k], d.oldLogE[k], d.oldLogE2[k])
				}
			}
		}
	}
}

// TestResetMatchesNewDecoder checks that Reset returns a used decoder to the
// state of a new one, keeping its configuration.
func TestResetMatchesNewDecoder(t *testing.T) {
	s := newCeltStream(t, 48000, 2)
	if err := s.decodeRecords(readBit(t, "testdata/stereo-loss-celt.bit")[:20]); err != nil {
		t.Fatal(err)
	}
	d := s.dec
	d.Reset()
	fresh, err := NewDecoder(48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d.decoderState != fresh.decoderState {
		t.Fatal("Reset leaves decoder state behind")
	}
	if d.end != 21 || d.streamChannels != 2 || d.channels != 2 || d.disableInv {
		t.Fatal("Reset changes the configuration")
	}
}

// TestDecoderArguments checks the argument errors of libopus's CELT decoder:
// OPUS_BAD_ARG for sample rates, channel counts, bands and frame sizes it
// does not support and frames over 1275 bytes, and a short output buffer.
func TestDecoderArguments(t *testing.T) {
	for _, tc := range []struct{ rate, channels int }{{44100, 1}, {48000, 0}, {48000, 3}, {96000, 2}} {
		if _, err := NewDecoder(tc.rate, tc.channels); !errors.Is(err, ErrBadArg) {
			t.Errorf("NewDecoder(%d, %d): %v", tc.rate, tc.channels, err)
		}
	}
	d, err := NewDecoder(48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d.PhaseInversionDisabled() || d.Channels() != 2 || d.Mode() != StaticMode() {
		t.Error("unexpected stereo configuration")
	}
	if m, err := NewDecoder(16000, 1); err != nil || !m.PhaseInversionDisabled() {
		t.Error("mono decoders disable phase inversion")
	}
	for _, v := range []int{-1, 21} {
		if d.SetStartBand(v) == nil {
			t.Errorf("SetStartBand(%d) accepted", v)
		}
	}
	for _, v := range []int{0, 22} {
		if d.SetEndBand(v) == nil {
			t.Errorf("SetEndBand(%d) accepted", v)
		}
	}
	for _, v := range []int{0, 3} {
		if d.SetStreamChannels(v) == nil {
			t.Errorf("SetStreamChannels(%d) accepted", v)
		}
	}
	pcm := make([]float32, 2*960)
	frame := make([]byte, 100)
	for _, n := range []int{0, 100, 1920} {
		if _, err := d.DecodeWithEC(frame, pcm, n, nil); !errors.Is(err, ErrBadArg) {
			t.Errorf("frame size %d: %v", n, err)
		}
	}
	if _, err := d.DecodeWithEC(make([]byte, 1276), pcm, 960, nil); !errors.Is(err, ErrBadArg) {
		t.Errorf("1276-byte frame: %v", err)
	}
	if _, err := d.DecodeWithEC(frame, pcm[:2*960-1], 960, nil); !errors.Is(err, ErrBufferTooSmall) {
		t.Errorf("short buffer: %v", err)
	}
	if _, err := d.DecodeWithEC(frame, nil, 960, nil); !errors.Is(err, ErrBadArg) {
		t.Errorf("nil buffer: %v", err)
	}
	if d.SetStartBand(17) != nil || d.SetEndBand(17) != nil {
		t.Fatal("bands rejected")
	}
	for _, f := range [][]byte{frame, nil} {
		if _, err := d.DecodeWithEC(f, pcm, 960, nil); !errors.Is(err, ErrBadArg) {
			t.Errorf("start band 17, end band 17: %v", err)
		}
	}
	if w := d.Mode().Window(); len(w) != d.Mode().Overlap() || w[0] != window120[0] || w[119] != 1 {
		t.Error("unexpected window")
	}
}

// TestSharedRangeDecoder decodes frames through a range decoder the caller
// owns, as hybrid frames are, and with a shortened storage, as a frame
// carrying redundancy is (opus_decode_frame).
func TestSharedRangeDecoder(t *testing.T) {
	recs := readBit(t, "testdata/webm-celt-20ms.bit")
	a, err := NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pa := make([]float32, 960)
	pb := make([]float32, 960)
	for i, r := range recs[:40] {
		frame := r.packet[1:]
		if i%3 == 2 {
			// A frame shortened as by trailing redundancy decodes the same
			// whether the decoder or the caller sets up the range decoder.
			frame = frame[:len(frame)-len(frame)/4]
		}
		_, errA := a.DecodeWithEC(frame, pa, 960, nil)
		var dec entcode.Decoder
		dec.Init(r.packet[1:])
		dec.SetStorage(len(frame))
		_, errB := b.DecodeWithEC(frame, pb, 960, &dec)
		if !errors.Is(errA, errB) || (errA != nil && !errors.Is(errA, ErrInternal)) {
			t.Fatalf("frame %d: errors %v and %v", i, errA, errB)
		}
		if a.FinalRange() != b.FinalRange() || sameFloats(pa, pb) >= 0 {
			t.Fatalf("frame %d: the decoders differ", i)
		}
	}
}

// TestDecodeDoesNotAllocate checks that decoding and concealment allocate
// nothing.
func TestDecodeDoesNotAllocate(t *testing.T) {
	recs := readBit(t, "testdata/stereo-transients-celt.bit")
	d, err := NewDecoder(48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 2*960)
	frames := make([][]byte, len(recs))
	for i, r := range recs {
		f, err := parseFrames(r.packet)
		if err != nil {
			t.Fatal(err)
		}
		frames[i] = f[0]
	}
	i := 0
	allocs := testing.AllocsPerRun(200, func() {
		data := frames[i%len(frames)]
		if i%7 == 3 {
			data = nil // conceal
		}
		_, _ = d.DecodeWithEC(data, pcm, 960, nil)
		i++
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per frame", allocs)
	}
}
