package opus

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fgn/puropus/internal/ogg"
	"github.com/fgn/puropus/internal/webm"
)

// packetStream is a packet sequence for the allocation test and the
// benchmarks.
type packetStream struct {
	name     string
	channels int
	packets  [][]byte
	samples  int // per channel, at 48 kHz
}

// readContainerStream reads the Opus packets of an Ogg Opus or WebM file.
func readContainerStream(tb testing.TB, path string) packetStream {
	tb.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	s := packetStream{name: filepath.Base(path)}
	if bytes.HasPrefix(data, []byte("OggS")) {
		r := ogg.NewReader(bytes.NewReader(data))
		for {
			p, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				tb.Fatal(err)
			}
			switch {
			case bytes.HasPrefix(p.Data, []byte("OpusHead")):
				head, err := ParseHead(p.Data)
				if err != nil {
					tb.Fatal(err)
				}
				s.channels = head.Channels
			case IsTags(p.Data):
			default:
				s.packets = append(s.packets, p.Data)
			}
		}
	} else {
		r, err := webm.NewReader(bytes.NewReader(data))
		if err != nil {
			tb.Fatal(err)
		}
		s.channels = r.Track().Channels
		for {
			b, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				tb.Fatal(err)
			}
			s.packets = append(s.packets, b.Data)
		}
	}
	for _, p := range s.packets {
		parsed, err := Parse(p, false)
		if err != nil {
			tb.Fatalf("%s: %v", path, err)
		}
		s.samples += parsed.Duration48()
	}
	return s
}

// readVectorStream reads the received packets of a stereo vector sequence.
func readVectorStream(tb testing.TB, name string, channels int) packetStream {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "stereo-vectors", name+".json"))
	if err != nil {
		tb.Fatal(err)
	}
	var vector stereoVector
	if err := json.Unmarshal(raw, &vector); err != nil {
		tb.Fatal(err)
	}
	s := packetStream{name: "stereo-vectors/" + name, channels: channels}
	for _, r := range vector.Records {
		if r.Missing {
			continue
		}
		p, err := hex.DecodeString(r.Packet)
		if err != nil {
			tb.Fatal(err)
		}
		s.packets = append(s.packets, p)
		s.samples += r.Samples
	}
	return s
}

// pairFrames returns s with every two consecutive single-frame packets joined
// into one code 2 packet of two frames, which makes 10 ms hybrid packets
// multi-frame ones.
func pairFrames(tb testing.TB, s packetStream) packetStream {
	tb.Helper()
	out := packetStream{name: s.name + " paired", channels: s.channels}
	for i := 0; i+1 < len(s.packets); i += 2 {
		a, b := s.packets[i], s.packets[i+1]
		if a[0]&3 != 0 || b[0] != a[0] || len(a)-1 >= 252 {
			tb.Fatal("cannot pair packets")
		}
		p := append([]byte{a[0] | 2, byte(len(a) - 1)}, a[1:]...)
		out.packets = append(out.packets, append(p, b[1:]...))
	}
	for _, p := range out.packets {
		parsed, err := Parse(p, false)
		if err != nil || parsed.FrameCount != 2 {
			tb.Fatalf("paired packet: %v", err)
		}
		out.samples += parsed.Duration48()
	}
	return out
}

// decodeStreams are the committed streams of every mode and channel layout.
func decodeStreams(tb testing.TB) []packetStream {
	tb.Helper()
	fixture := func(name string) packetStream {
		return readContainerStream(tb, filepath.Join("..", "testdata", name))
	}
	return []packetStream{
		fixture("ogg-celt-fb.opus"),
		fixture("webm-celt-60ms.webm"),
		fixture("ogg-silk-wb.opus"),
		fixture("ogg-silk-wb-60ms.opus"),
		fixture("ogg-hybrid-swb.opus"),
		pairFrames(tb, fixture("ogg-hybrid-fb-10ms.opus")),
		fixture("ogg-switch-silk-celt-wb.opus"),
		readVectorStream(tb, "channels-celt", 2),
		readVectorStream(tb, "loss-silk", 2),
		readVectorStream(tb, "loss-hybrid", 2),
		readVectorStream(tb, "modes", 2),
		readVectorStream(tb, "modes", 1),
	}
}

// TestDecodeDoesNotAllocate requires decoding, concealment and FEC decoding
// to allocate nothing per packet once the decoder exists, in every mode, for
// multi-frame packets and mode transitions, mono and stereo.
func TestDecodeDoesNotAllocate(t *testing.T) {
	for _, s := range decodeStreams(t) {
		d, err := NewDecoder(s.channels)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]float32, MaxPacketSamples*s.channels)
		i := 0
		allocs := testing.AllocsPerRun(len(s.packets), func() {
			if _, err := d.Decode(s.packets[i%len(s.packets)], out); err != nil {
				t.Fatal(err)
			}
			i++
		})
		if allocs != 0 {
			t.Errorf("%s: %v allocations per packet", s.name, allocs)
		}
		allocs = testing.AllocsPerRun(20, func() {
			if _, err := d.Conceal(MaxPacketSamples, out); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 0 {
			t.Errorf("%s: %v allocations per concealment", s.name, allocs)
		}
		allocs = testing.AllocsPerRun(20, func() {
			if _, err := d.DecodeFEC(s.packets[i%len(s.packets)], MaxPacketSamples, out); err != nil {
				t.Fatal(err)
			}
			i++
		})
		if allocs != 0 {
			t.Errorf("%s: %v allocations per FEC decode", s.name, allocs)
		}
	}
}

// benchmarkDecode decodes s in a loop and reports the time per second of
// audio.
func benchmarkDecode(b *testing.B, s packetStream) {
	d, err := NewDecoder(s.channels)
	if err != nil {
		b.Fatal(err)
	}
	out := make([]float32, MaxPacketSamples*s.channels)
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		for _, p := range s.packets {
			if _, err := d.Decode(p, out); err != nil {
				b.Fatal(err)
			}
		}
	}
	elapsed := time.Since(start)
	audio := float64(s.samples) / SampleRate * float64(b.N)
	b.ReportMetric(float64(elapsed.Nanoseconds())/audio, "ns/audio-s")
	b.ReportMetric(float64(len(s.packets)), "packets/op")
}

// BenchmarkDecode decodes the committed streams of every mode at 48 kHz,
// the codec alone.
func BenchmarkDecode(b *testing.B) {
	for _, s := range decodeStreams(b) {
		b.Run(strings.ReplaceAll(s.name, "/", "-"), func(b *testing.B) { benchmarkDecode(b, s) })
	}
}

// BenchmarkDecodeStreams decodes the Ogg Opus and WebM recordings named by
// PUROPUS_STREAMS, a filepath.ListSeparator-separated list of globs, at 48
// kHz, the codec alone. It is skipped when the variable is unset.
func BenchmarkDecodeStreams(b *testing.B) {
	var paths []string
	for _, pattern := range filepath.SplitList(os.Getenv("PUROPUS_STREAMS")) {
		if pattern = strings.TrimSpace(pattern); pattern == "" {
			continue
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			b.Fatal(err)
		}
		slices.Sort(matches)
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		b.Skip("set PUROPUS_STREAMS to Ogg Opus or WebM recordings")
	}
	for _, path := range paths {
		s := readContainerStream(b, path)
		b.Run(s.name, func(b *testing.B) { benchmarkDecode(b, s) })
	}
}

// BenchmarkSnapshot measures what DecodeParsed pays to be able to undo a
// hybrid packet: the SILK state for one frame, the CELT state too for more.
func BenchmarkSnapshot(b *testing.B) {
	for _, channels := range []int{1, 2} {
		for _, withCELT := range []bool{false, true} {
			name := "silk"
			if withCELT {
				name = "silk+celt"
			}
			b.Run(name+"/"+[]string{"", "mono", "stereo"}[channels], func(b *testing.B) {
				d, err := NewDecoder(channels)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					d.saveState(withCELT)
				}
			})
		}
	}
}
