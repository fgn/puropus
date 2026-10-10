package silk_test

import (
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fgn/puropus/opus/internal/entcode"
	"github.com/fgn/puropus/opus/internal/silk"
)

// decodeStream decodes packets on a new decoder and returns all its output.
func decodeStream(tb testing.TB, packets []silkPacket, channels int) []int16 {
	var d silk.Decoder
	d.Init()
	ctl := silk.DecControl{NChannelsAPI: channels, APISampleRate: 48000}
	var dec entcode.Decoder
	pcm := make([]int16, 2*960)
	var all []int16
	for i := range packets {
		p := &packets[i]
		dec.Init(p.frame)
		ctl.PayloadSizeMs = p.payloadMs
		ctl.NChannelsInternal = p.channelsInternal
		ctl.InternalSampleRate = p.internalRate
		for decoded := 0; decoded < p.samples48; {
			n, err := d.Decode(&ctl, silk.FlagDecodeNormal, decoded == 0, &dec, pcm)
			if err != nil {
				tb.Fatal(err)
			}
			all = append(all, pcm[:n*channels]...)
			decoded += n
		}
	}
	return all
}

// TestDecodeSurvivesPreemption decodes SILK streams of every internal rate,
// mono and stereo, on more goroutines than processors for a while and
// requires every result to be identical. Go's asynchronous preemption saves
// only the vector registers the CPU features it reports cover, so a kernel
// wider than the features it was dispatched on would resume with corrupted
// lanes. Run it under each GODEBUG=cpu.* setting the dispatch tests use.
func TestDecodeSurvivesPreemption(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	streams := []struct {
		packets  []silkPacket
		channels int
	}{
		{fixturePackets(t, "ogg-silk-wb"), 1},
		{fixturePackets(t, "ogg-silk-mb"), 1},
		{fixturePackets(t, "ogg-silk-nb"), 1},
		{stereoPackets(t, "loss-silk"), 2},
	}
	want := make([][]int16, len(streams))
	for i, s := range streams {
		want[i] = decodeStream(t, s.packets, s.channels)
	}
	var failed atomic.Bool
	var wg sync.WaitGroup
	deadline := time.Now().Add(400 * time.Millisecond)
	for g := 0; g < 2*runtime.GOMAXPROCS(0)+1; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) && !failed.Load() {
				for i, s := range streams {
					if got := decodeStream(t, s.packets, s.channels); !slices.Equal(got, want[i]) {
						failed.Store(true)
						t.Errorf("stream %d differs under preemption", i)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}
