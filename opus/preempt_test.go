package opus

import (
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDecodeSurvivesPreemption decodes CELT and hybrid streams (mono and
// stereo, long and transient frames) at 48 kHz on more goroutines than processors for a
// while and requires every result to be bit-identical. Go's asynchronous
// preemption saves the upper halves of the vector registers only for the
// widths internal/cpu reports, so a vector kernel wider than the features
// it was dispatched on can resume with corrupted lanes. Run it under each
// GODEBUG=cpu.* setting the dispatch tests use.
func TestDecodeSurvivesPreemption(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	streams := []packetStream{
		readContainerStream(t, filepath.Join("..", "testdata", "webm-celt-60ms.webm")),
		readContainerStream(t, filepath.Join("..", "testdata", "ogg-hybrid-swb.opus")),
		readVectorStream(t, "transients-celt", 2),
		readVectorStream(t, "channels-celt", 2),
		readVectorStream(t, "channels-hybrid-fec", 2),
	}
	decode := func(s packetStream) []float32 {
		d, err := NewDecoder(s.channels)
		if err != nil {
			t.Error(err)
			return nil
		}
		out := make([]float32, MaxPacketSamples*s.channels)
		var all []float32
		for _, p := range s.packets {
			n, err := d.Decode(p, out)
			if err != nil {
				t.Error(err)
				return nil
			}
			all = append(all, out[:n*s.channels]...)
		}
		return all
	}
	want := make([][]float32, len(streams))
	for i, s := range streams {
		want[i] = decode(s)
	}
	var failed atomic.Bool
	var wg sync.WaitGroup
	deadline := time.Now().Add(400 * time.Millisecond)
	for g := 0; g < 2*runtime.GOMAXPROCS(0)+1; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := g; time.Now().Before(deadline) && !failed.Load(); k++ {
				s := k % len(streams)
				got := decode(streams[s])
				for i := range want[s] {
					if math.Float32bits(got[i]) != math.Float32bits(want[s][i]) {
						failed.Store(true)
						t.Errorf("%s: sample %d is %v under preemption, want %v", streams[s].name, i, got[i], want[s][i])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}
