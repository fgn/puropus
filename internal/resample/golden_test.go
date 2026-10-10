package resample

import (
	"encoding/json"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func readManifest(t testing.TB) manifest {
	t.Helper()
	var m manifest
	if err := json.Unmarshal(readFile(t, filepath.Join("testdata", "manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Signals) != len(testSignals) {
		t.Fatalf("manifest lists %d signals, want %d", len(m.Signals), len(testSignals))
	}
	return m
}

func readFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// readHashed reads a testdata file and checks its SHA-256.
func readHashed(t testing.TB, name, sum string) []float32 {
	t.Helper()
	b := readFile(t, filepath.Join("testdata", name))
	if got := sha256Hex(b); got != sum {
		t.Fatalf("%s: SHA-256 %s, manifest says %s", name, got, sum)
	}
	return floats(t, b)
}

// TestFFmpegGoldens compares the resampler with FFmpeg's output for the test
// signals, fed whole and in chunks.
func TestFFmpegGoldens(t *testing.T) {
	m := readManifest(t)
	forEachKernel(t, func(t *testing.T) {
		var worst gateResult
		for i, s := range m.Signals {
			x := readHashed(t, s.Name+".48k.f32le", s.SHA256In)
			want := readHashed(t, s.Name+".16k.f32le", s.SHA256Out)
			if !slices.Equal(f32le(x), f32le(testSignals[i].samples())) {
				t.Errorf("%s: input differs from the signal generator", s.Name)
			}
			if len(want) != s.Samples16 || int64(len(want)) != outputCount(int64(len(x))) {
				t.Errorf("%s: %d reference samples for %d inputs", s.Name, len(want), len(x))
			}
			for _, sizes := range [][]int{{len(x) + 1}, {1}, {7}, {120}, {960}} {
				worst = worst.max(gate(t, s.Name, resampleChunks(x, sizes), want))
			}
		}
		t.Logf("worst RMS error %.3g, peak %.3g", worst.rms, worst.peak)
	})
}

// TestFFmpegPrefixes checks the output of every prefix length of the noise
// signal, which pins the output count of short streams.
func TestFFmpegPrefixes(t *testing.T) {
	m := readManifest(t)
	p := m.Prefixes
	src := readFile(t, filepath.Join("testdata", p.Source))
	x := floats(t, src)
	all := readHashed(t, p.File, p.SHA256)
	if len(p.Lengths) != len(p.Counts) || !slices.Equal(p.Lengths, prefixLengths()) {
		t.Fatal("prefix lengths do not match the generator")
	}
	forEachKernel(t, func(t *testing.T) {
		var worst gateResult
		rest := all
		for i, n := range p.Lengths {
			want := rest[:p.Counts[i]]
			rest = rest[p.Counts[i]:]
			if got := outputCount(int64(n)); got != int64(len(want)) {
				t.Errorf("outputCount(%d) = %d, reference has %d", n, got, len(want))
			}
			worst = worst.max(gate(t, "prefix", resampleChunks(x[:n], []int{n + 1}), want))
			worst = worst.max(gate(t, "prefix", resampleChunks(x[:n], []int{1}), want))
		}
		if len(rest) != 0 {
			t.Errorf("%d unused reference samples", len(rest))
		}
		t.Logf("worst RMS error %.3g, peak %.3g", worst.rms, worst.peak)
	})
}

// TestTapsMatchImpulseResponse reads the filter taps off FFmpeg's response
// to unit impulses at samples 300, 301 and 302: output m is centred on input
// 3m, so impulse p reaches output m through h(3m-p).
func TestTapsMatchImpulseResponse(t *testing.T) {
	m := readManifest(t)
	var h [nTaps]float32
	seen := 0
	for p := 300; p <= 302; p++ {
		var y []float32
		for _, s := range m.Signals {
			if s.Name == "impulse-"+strconv.Itoa(p) {
				y = readHashed(t, s.Name+".16k.f32le", s.SHA256Out)
			}
		}
		if y == nil {
			t.Fatalf("no impulse-%d golden", p)
		}
		for mi, v := range y {
			d := 3*mi - p
			if d < -postTaps || d > preTaps {
				if v != 0 {
					t.Errorf("impulse %d reaches output %d (distance %d)", p, mi, d)
				}
				continue
			}
			h[preTaps-d] = v
			seen++
		}
	}
	if seen != nTaps {
		t.Fatalf("assembled %d taps", seen)
	}
	for i := range h {
		if h[i] != taps[i] {
			t.Errorf("tap %d (distance %d): designed %g, measured %g", i, preTaps-i, taps[i], h[i])
		}
	}
}

// TestRepositoryGoldens resamples the repository's 48 kHz references (libopus
// decodes) and compares them with the 16 kHz references made from them by
// FFmpeg.
func TestRepositoryGoldens(t *testing.T) {
	cases := repositoryGoldens(t)
	forEachKernel(t, func(t *testing.T) {
		var mono, stereo gateResult
		for _, c := range cases {
			got := make([][]float32, len(c.in))
			for ch, x := range c.in {
				got[ch] = resampleChunks(x, []int{960})
			}
			inter := make([]float32, 0, len(c.want))
			for i := range got[0] {
				for ch := range got {
					inter = append(inter, got[ch][i])
				}
			}
			g := gate(t, c.name, inter, c.want)
			t.Logf("%-30s RMS %.3g peak %.3g", c.name, g.rms, g.peak)
			if len(c.in) == 1 {
				mono = mono.max(g)
			} else {
				stereo = stereo.max(g)
			}
		}
		t.Logf("mono: worst RMS error %.3g, peak %.3g", mono.rms, mono.peak)
		t.Logf("stereo: worst RMS error %.3g, peak %.3g", stereo.rms, stereo.peak)
	})
}

type repositoryGolden struct {
	name string
	in   [][]float32 // 48 kHz input per channel
	want []float32   // interleaved 16 kHz reference
}

// repositoryGoldens pairs the repository's 48 kHz and 16 kHz references:
// testdata/<stem>.48k.f32le with <stem>.16k.f32le for the mono SILK and
// hybrid fixtures (whole streams, nothing to trim), and the libopus stereo
// vectors, trimmed by 317 leading and 269 trailing samples per channel, with
// testdata/stereo-containers.
func repositoryGoldens(t *testing.T) []repositoryGolden {
	root := filepath.Join("..", "..", "testdata")
	var fixtures struct {
		Fixtures map[string]struct {
			Samples48 int  `json:"samples48"`
			Samples16 int  `json:"samples16"`
			Full48    bool `json:"full48"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(root, "manifest.json")), &fixtures); err != nil {
		t.Fatal(err)
	}
	var cases []repositoryGolden
	for _, name := range slices.Sorted(maps.Keys(fixtures.Fixtures)) {
		f := fixtures.Fixtures[name]
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		path48 := filepath.Join(root, stem+".48k.f32le")
		if _, err := os.Stat(path48); err != nil {
			continue // CELT fixtures have checkpoints, not full 48 kHz PCM
		}
		if !f.Full48 {
			t.Errorf("%s: 48 kHz PCM without full48", name)
		}
		x := floats(t, readFile(t, path48))
		want := floats(t, readFile(t, filepath.Join(root, stem+".16k.f32le")))
		if len(x) != f.Samples48 || len(want) != f.Samples16 {
			t.Errorf("%s: %d/%d samples, manifest says %d/%d", name, len(x), len(want), f.Samples48, f.Samples16)
		}
		cases = append(cases, repositoryGolden{stem, [][]float32{x}, want})
	}
	if len(cases) != 20 {
		t.Errorf("found %d mono fixtures with 48 kHz PCM, want 20", len(cases))
	}
	const preSkip, trim = 317, 269
	for _, name := range []string{"channels-silk-fec", "channels-hybrid-fec", "channels-celt", "modes"} {
		pcm := floats(t, readFile(t, filepath.Join("..", "..", "opus", "testdata", "stereo-vectors", name+".f32le")))
		pcm = pcm[2*preSkip : len(pcm)-2*trim]
		c := repositoryGolden{name: "stereo " + name, in: make([][]float32, 2)}
		for ch := range c.in {
			c.in[ch] = make([]float32, len(pcm)/2)
			for i := range c.in[ch] {
				c.in[ch][i] = pcm[2*i+ch]
			}
		}
		c.want = floats(t, readFile(t, filepath.Join(root, "stereo-containers", name+".16k.f32le")))
		cases = append(cases, c)
	}
	return cases
}

// TestDesignDC checks that the taps sum to one, so a constant passes
// unchanged apart from rounding.
func TestDesignDC(t *testing.T) {
	sum := 0.0
	for _, v := range taps {
		sum += float64(v)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("taps sum to %v", sum)
	}
}
