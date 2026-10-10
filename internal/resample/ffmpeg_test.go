package resample

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The goldens in testdata are FFmpeg's 48-to-16 kHz resampling of the test
// signals below. With RESAMPLE_FFMPEG set to an ffmpeg binary:
//
//	RESAMPLE_FFMPEG=/path/to/ffmpeg RESAMPLE_REGENERATE=1 go test -run TestFFmpegRegenerate
//
// rewrites them (RESAMPLE_FFMPEG_ALT names a second binary whose output must
// be byte-identical), and
//
//	RESAMPLE_FFMPEG=/path/to/ffmpeg go test -run TestFFmpegLive
//
// compares the resampler with the binary on random streams.

// ffmpegArgs is the command the goldens come from; IN and OUT stand for the
// input and output paths.
var ffmpegArgs = []string{
	"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
	"-f", "f32le", "-ar", "48000", "-ac", "1", "-i", "IN",
	"-ar", "16000", "-f", "f32le", "OUT",
}

type testSignal struct {
	name, description string
	samples           func() []float32
}

var testSignals = []testSignal{
	{"impulse-300", "unit impulse at sample 300 of 600", func() []float32 { return impulses(600, map[int]float32{300: 1}) }},
	{"impulse-301", "unit impulse at sample 301 of 600", func() []float32 { return impulses(600, map[int]float32{301: 1}) }},
	{"impulse-302", "unit impulse at sample 302 of 600", func() []float32 { return impulses(600, map[int]float32{302: 1}) }},
	{
		"impulse-edges", "impulses of different heights near both ends of a 301-sample stream",
		func() []float32 {
			return impulses(301, map[int]float32{
				0: 1, 1: -0.5, 2: 0.25, 3: 0.125, 47: -0.75, 48: 0.6, 49: -0.4, 50: 0.3, 51: -0.2,
				248: 0.7, 249: -0.35, 250: 0.9, 251: 0.45, 252: -0.15, 296: 0.55, 298: -0.65, 299: 0.8, 300: -1,
			})
		},
	},
	{"dc", "constant 0.5, 1000 samples", func() []float32 {
		x := make([]float32, 1000)
		for i := range x {
			x[i] = 0.5
		}
		return x
	}},
	{"ramp", "linear ramp from -1 to 1 over 1001 samples", func() []float32 {
		x := make([]float32, 1001)
		for i := range x {
			x[i] = float32(-1 + 2*float64(i)/1000)
		}
		return x
	}},
	{"square", "1 kHz square wave of amplitude 0.8 (24 samples up, 24 down), 4801 samples", func() []float32 {
		x := make([]float32, 4801)
		for i := range x {
			x[i] = 0.8
			if i%48 >= 24 {
				x[i] = -0.8
			}
		}
		return x
	}},
	{"chirp", "exponential sine sweep from 20 Hz to 24 kHz at amplitude 0.9, 24000 samples", func() []float32 {
		const n, f0, f1, rate = 24000, 20.0, 24000.0, 48000.0
		x := make([]float32, n)
		k := math.Log(f1 / f0)
		T := n / rate
		for i := range x {
			t := float64(i) / rate
			x[i] = float32(0.9 * math.Sin(2*math.Pi*f0*T/k*(math.Exp(t/T*k)-1)))
		}
		return x
	}},
	{"noise", "uniform white noise in [-1, 1), 12345 samples (math/rand/v2 PCG seeded 1, 2)", noise},
}

// prefixLengths are the stream lengths of the "prefixes" golden: FFmpeg's
// output for the first n samples of the noise signal, for every n here.
func prefixLengths() []int {
	var ls []int
	for n := 0; n <= 140; n++ {
		ls = append(ls, n)
	}
	return append(ls, 199, 200, 201, 299, 300, 301, 1000, 1001, 1002, 4094, 4095, 4096, 4097, 4098, 4099)
}

func impulses(n int, at map[int]float32) []float32 {
	x := make([]float32, n)
	for i, v := range at {
		x[i] = v
	}
	return x
}

func noise() []float32 {
	rng := rand.New(rand.NewPCG(1, 2))
	x := make([]float32, 12345)
	for i := range x {
		x[i] = float32(2*rng.Float64() - 1)
	}
	return x
}

// manifest describes testdata.
type manifest struct {
	Command   string           `json:"command"`
	FFmpeg    string           `json:"ffmpeg"`
	Identical []string         `json:"identical_output"`
	Signals   []manifestSignal `json:"signals"`
	Prefixes  manifestPrefixes `json:"prefixes"`
}

type manifestSignal struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Samples48   int    `json:"samples48"`
	Samples16   int    `json:"samples16"`
	SHA256In    string `json:"sha256_48k"`
	SHA256Out   string `json:"sha256_16k"`
}

type manifestPrefixes struct {
	Description string `json:"description"`
	Source      string `json:"source"`
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	Lengths     []int  `json:"lengths"`
	Counts      []int  `json:"counts"`
}

// runFFmpeg resamples x with the binary at path.
func runFFmpeg(t testing.TB, path string, x []float32) []float32 {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.f32le"), filepath.Join(dir, "out.f32le")
	if err := os.WriteFile(in, f32le(x), 0o644); err != nil {
		t.Fatal(err)
	}
	args := append([]string(nil), ffmpegArgs...)
	for i, a := range args {
		switch a {
		case "IN":
			args[i] = in
		case "OUT":
			args[i] = out
		}
	}
	cmd := exec.Command(path, args...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return floats(t, b)
}

func ffmpegVersion(t testing.TB, path string) string {
	t.Helper()
	b, err := exec.Command(path, "-version").Output()
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return line
}

func TestFFmpegRegenerate(t *testing.T) {
	bin := os.Getenv("RESAMPLE_FFMPEG")
	if bin == "" || os.Getenv("RESAMPLE_REGENERATE") == "" {
		t.Skip("set RESAMPLE_FFMPEG and RESAMPLE_REGENERATE=1 to rewrite testdata")
	}
	alt := os.Getenv("RESAMPLE_FFMPEG_ALT")
	m := manifest{
		Command: "ffmpeg " + strings.Join(ffmpegArgs, " "),
		FFmpeg:  ffmpegVersion(t, bin),
	}
	if alt != "" {
		m.Identical = append(m.Identical, ffmpegVersion(t, alt))
	}
	resample := func(x []float32) []float32 {
		y := runFFmpeg(t, bin, x)
		if alt != "" && !bytes.Equal(f32le(y), f32le(runFFmpeg(t, alt, x))) {
			t.Fatalf("%s and %s differ", bin, alt)
		}
		return y
	}
	write := func(name string, x []float32) string {
		b := f32le(x)
		if err := os.WriteFile(filepath.Join("testdata", name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return sha256Hex(b)
	}
	for _, s := range testSignals {
		x := s.samples()
		y := resample(x)
		m.Signals = append(m.Signals, manifestSignal{
			Name: s.name, Description: s.description, Samples48: len(x), Samples16: len(y),
			SHA256In: write(s.name+".48k.f32le", x), SHA256Out: write(s.name+".16k.f32le", y),
		})
	}
	src := noise()
	var all []float32
	p := manifestPrefixes{
		Description: "outputs for the first n samples of the noise signal, for each n in lengths, concatenated",
		Source:      "noise.48k.f32le",
		File:        "prefixes.16k.f32le",
		Lengths:     prefixLengths(),
	}
	for _, n := range p.Lengths {
		y := resample(src[:n])
		p.Counts = append(p.Counts, len(y))
		all = append(all, y...)
	}
	p.SHA256 = write(p.File, all)
	m.Prefixes = p
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "manifest.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFFmpegLive compares random streams, fed in random chunks, with the
// binary named by RESAMPLE_FFMPEG.
func TestFFmpegLive(t *testing.T) {
	bin := os.Getenv("RESAMPLE_FFMPEG")
	if bin == "" {
		t.Skip("set RESAMPLE_FFMPEG to compare with an ffmpeg binary")
	}
	rng := rand.New(rand.NewPCG(7, 11))
	var worst gateResult
	for range 200 {
		n := rng.IntN(20000)
		if rng.IntN(4) == 0 {
			n = rng.IntN(300)
		}
		x := make([]float32, n)
		for i := range x {
			x[i] = float32(rng.NormFloat64() * 0.3)
		}
		want := runFFmpeg(t, bin, x)
		got := resampleChunks(x, randomChunks(rng, n, 1+rng.IntN(6000)))
		g := gate(t, "live", got, want)
		worst = worst.max(g)
	}
	t.Logf("worst RMS %.3g, worst peak %.3g", worst.rms, worst.peak)
}
