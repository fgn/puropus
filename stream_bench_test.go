package puropus

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fgn/puropus/internal/webm"
)

// benchStream is one recording, held in memory, with its packet count.
type benchStream struct {
	name     string
	data     []byte
	packets  int
	channels int
}

// benchStreams returns the recordings named by PUROPUS_STREAMS, a
// filepath.ListSeparator-separated list of globs, or the committed 60 ms
// fullband CELT WebM fixture when it is unset.
func benchStreams(b *testing.B) []benchStream {
	b.Helper()
	paths := envStreams(b)
	if len(paths) == 0 {
		paths = []string{filepath.Join("testdata", "webm-celt-60ms.webm")}
	}
	streams := make([]benchStream, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		src, err := ReadPackets(bytes.NewReader(data))
		if err != nil {
			b.Fatalf("%s: %v", path, err)
		}
		streams = append(streams, benchStream{
			name:     filepath.Base(path),
			data:     data,
			packets:  len(src.Packets),
			channels: src.Header.Channels,
		})
	}
	return streams
}

// pipeChunks writes data to w in chunk-sized writes and closes it, the way
// an upload handler forwards network reads into a decoder's pipe.
func pipeChunks(w *io.PipeWriter, data []byte, chunk int) {
	for len(data) > 0 {
		n := min(chunk, len(data))
		if _, err := w.Write(data[:n]); err != nil {
			return
		}
		data = data[n:]
	}
	_ = w.Close()
}

// decodeStreamed decodes data to 16 kHz through a pipe fed with 32 KiB
// writes, pulling PCM with Read into an 8192-value buffer, and returns the
// number of values delivered.
func decodeStreamed(b *testing.B, data []byte, buf []float32) int {
	r, w := io.Pipe()
	go pipeChunks(w, data, 32<<10)
	dec, err := NewDecoder(r, DecodeOptions{SampleRate: 16000})
	if err != nil {
		b.Fatal(err)
	}
	total := 0
	for {
		n, err := dec.Read(buf)
		total += n
		if errors.Is(err, io.EOF) {
			return total
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStreamTo16k decodes whole recordings to 16 kHz as a live ingest
// service does: the container arrives through an io.Pipe in 32 KiB writes
// and PCM is pulled incrementally with Read. One op decodes every stream in
// turn. Besides the usual figures it reports wall time per second of audio,
// process CPU time per hour of audio (where the platform exposes it), and
// allocations per packet.
func BenchmarkStreamTo16k(b *testing.B) {
	streams := benchStreams(b)
	packets := 0
	for _, s := range streams {
		packets += s.packets
	}
	buf := make([]float32, 8192)

	b.ReportAllocs()
	b.ResetTimer()
	var mem0, mem1 runtime.MemStats
	runtime.ReadMemStats(&mem0)
	cpu0 := processCPUTime()
	start := time.Now()
	samples := 0
	for i := 0; i < b.N; i++ {
		for _, s := range streams {
			samples += decodeStreamed(b, s.data, buf) / s.channels
		}
	}
	wall := time.Since(start)
	cpu := processCPUTime() - cpu0
	b.StopTimer()
	runtime.ReadMemStats(&mem1)

	audio := float64(samples) / 16000
	b.ReportMetric(float64(wall.Nanoseconds())/audio, "ns/audio-s")
	if cpu > 0 {
		b.ReportMetric(cpu.Seconds()/audio*3600, "cpu-s/audio-h")
	}
	b.ReportMetric(float64(mem1.Mallocs-mem0.Mallocs)/float64(packets*b.N), "allocs/packet")
	b.ReportMetric(0, "ns/op")
	b.ReportMetric(audio/float64(b.N), "audio-s/op")
	b.ReportMetric(float64(packets), "packets/op")
}

// TestReadDoesNotAllocate requires Read to allocate nothing per packet once
// decoding has started, at 48 and 16 kHz, from Ogg and WebM, mono and
// stereo, when packets are not kept. Two of the streams discard a few
// samples from every WebM block, so the resampler's output per packet is
// rarely a multiple of four (its vector kernels compute four at a time).
func TestReadDoesNotAllocate(t *testing.T) {
	stereo, _ := stereoStreamReference(t, "modes")
	var stereoOgg, stereoWebM bytes.Buffer
	if err := stereo.Write(&stereoOgg, FormatOgg); err != nil {
		t.Fatal(err)
	}
	if err := stereo.Write(&stereoWebM, FormatWebM); err != nil {
		t.Fatal(err)
	}
	streams := map[string][]byte{
		"ogg-celt-fb.opus":          readFixture(t, "ogg-celt-fb.opus"),
		"webm-celt-60ms.webm":       readFixture(t, "webm-celt-60ms.webm"),
		"ogg-silk-wb.opus":          readFixture(t, "ogg-silk-wb.opus"),
		"ogg-hybrid-swb.opus":       readFixture(t, "ogg-hybrid-swb.opus"),
		"stereo modes Ogg":          stereoOgg.Bytes(),
		"stereo modes WebM":         stereoWebM.Bytes(),
		"ogg-switch-silk-celt-wb":   readFixture(t, "ogg-switch-silk-celt-wb.opus"),
		"ogg-silk-wb-60ms-fec.opus": readFixture(t, "ogg-silk-wb-60ms-fec.opus"),
		"trimmed blocks, mono":      paddedWebM(t, 1, sourceFor(t, "webm-celt-60ms.webm").Packets),
		"trimmed blocks, stereo":    paddedWebM(t, 2, stereo.Packets),
	}
	for name, data := range streams {
		for _, rate := range []int{48000, 16000} {
			d, err := NewDecoder(bytes.NewReader(data), DecodeOptions{SampleRate: rate})
			if err != nil {
				t.Fatal(err)
			}
			// Each read takes 100 ms of audio; the first 200 ms grow the
			// buffers, and the next 500 ms, several packets, must not
			// allocate.
			buf := make([]float32, rate/10*d.Channels())
			for i := 0; i < 2; i++ {
				if _, err := d.Read(buf); err != nil {
					t.Fatal(err)
				}
			}
			allocs := allocations(func() {
				for range 5 {
					if _, err := d.Read(buf); err != nil {
						t.Fatal(err)
					}
				}
			})
			if allocs != 0 {
				t.Errorf("%s at %d Hz: %d allocations in 5 reads", name, rate, allocs)
			}
		}
	}
}

// paddedWebM writes packets as a WebM whose every block discards one to
// seven samples at its end.
func paddedWebM(t *testing.T, channels int, packets []Packet) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := webm.NewWriter(&b, webm.WriterOptions{Channels: channels})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range packets {
		if err := w.WriteBlock(p.Data, p.Coded(), 1+i%7); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// allocations returns the number of heap allocations f makes. It counts each
// one: testing.AllocsPerRun reports the mean per run rounded down, which reads
// as zero when not every run allocates.
func allocations(f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.Mallocs - before.Mallocs
}
