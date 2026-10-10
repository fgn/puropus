package puropus

import (
	"bytes"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDecodeSurvivesPreemption decodes the same stream to 16 kHz on more
// goroutines than processors for a while and requires every result to be
// bit-identical. Go's asynchronous preemption saves the upper halves of the
// vector registers only for the widths internal/cpu reports, YMM with AVX2
// and ZMM with AVX-512, so a kernel wider than the features it was
// dispatched on can resume with corrupted lanes. Run it under each
// GODEBUG=cpu.* setting the dispatch tests use.
func TestDecodeSurvivesPreemption(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	data := readFixture(t, "webm-celt-60ms.webm")
	want, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: 16000})
	var failed atomic.Bool
	var wg sync.WaitGroup
	deadline := time.Now().Add(400 * time.Millisecond)
	for g := 0; g < 2*runtime.GOMAXPROCS(0)+1; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) && !failed.Load() {
				got, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: 16000})
				for i := range want {
					if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
						failed.Store(true)
						t.Errorf("%s: sample %d is %v under preemption, want %v", Acceleration(), i, got[i], want[i])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}
