package silk_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// opusVector is a libopus 1.5.2 reference sequence of opus/testdata
// (transition-vectors, stereo-vectors): packets or losses with the
// opus_decode_float call libopus made, its result and final range, and the
// position of its output in the PCM file next to it.
type opusVector struct {
	Channels int `json:"channels"`
	Samples  int `json:"samples"`
	Records  []struct {
		Index      int    `json:"index"`
		Event      string `json:"event"`
		Missing    bool   `json:"missing"`
		Packet     string `json:"packet_hex"`
		FrameSize  int    `json:"frame_size"`
		Result     *int   `json:"result"`
		Samples    int    `json:"samples"`
		Offset     int    `json:"pcm_offset"`
		FinalRange uint32 `json:"final_range"`
	} `json:"records"`
}

func readOpusVector(t testing.TB, path string) opusVector {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v opusVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// replayVector decodes the records of v on a decoder with the given output
// channels and compares every result, final range and output sample with
// libopus's (want, interleaved). As in TestOggFixtures, the final ranges of
// packets with CELT redundancy and the samples libopus mixes with its CELT
// audio are left out and counted.
func replayVector(t *testing.T, v opusVector, channels int, want []float32) {
	t.Helper()
	if len(want) != v.Samples*channels {
		t.Fatalf("reference has %d values, want %d", len(want), v.Samples*channels)
	}
	d := newSilkOpus(channels)
	out := make([]float32, 5760*channels)
	events := map[string]int{}
	celt, celtSamples := 0, 0
	for _, r := range v.Records {
		var data []byte
		if !r.Missing {
			var err error
			if data, err = hex.DecodeString(r.Packet); err != nil {
				t.Fatalf("record %d: %v", r.Index, err)
			}
		}
		n, err := d.decode(data, r.FrameSize, out)
		if r.Result != nil && *r.Result < 0 {
			// libopus rejects the packet and leaves its state unchanged.
			if err == nil {
				t.Fatalf("record %d (%s): decoded, libopus returned %d", r.Index, r.Event, *r.Result)
			}
			events[r.Event]++
			continue
		}
		if err != nil {
			t.Fatalf("record %d (%s): %v", r.Index, r.Event, err)
		}
		if n != r.Samples {
			t.Fatalf("record %d (%s): %d samples, libopus %d", r.Index, r.Event, n, r.Samples)
		}
		ref := want[r.Offset*channels : (r.Offset+n)*channels]
		if d.celtRange {
			celt++
		} else if d.rangeFinal != r.FinalRange {
			t.Errorf("record %d (%s): final range %08x, libopus %08x", r.Index, r.Event, d.rangeFinal, r.FinalRange)
		}
		for _, c := range d.celtSamples {
			copy(out[c[0]*channels:c[1]*channels], ref[c[0]*channels:c[1]*channels])
			celtSamples += c[1] - c[0]
		}
		if msg := pcmMismatch(out[:n*channels], ref); msg != "" {
			t.Fatalf("record %d (%s): %s", r.Index, r.Event, msg)
		}
		events[r.Event]++
	}
	t.Logf("records by event: %v", events)
	if celt > 0 {
		t.Logf("%d packets with CELT redundancy: their final ranges and %d samples per channel mixed with CELT audio not compared", celt, celtSamples)
	}
}

// TestLossVectors replays the SILK-only mono sequences of
// opus/testdata/transition-vectors: single losses and loss runs of 10, 20 and
// 60 ms frames, recovery, TOC-only packets (DTX), packets of zero-length
// frames, a malformed packet, and quiet passages with encoded DTX. Packet
// loss concealment, comfort noise and the recovery after them must match
// libopus bit for bit, with every final range and sample count.
func TestLossVectors(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "transition-vectors")
	for _, name := range []string{"loss-silk", "loss-silk-nb10", "loss-silk-mb10", "loss-silk-nb60", "loss-silk-mb60", "loss-silk-quiet-dtx"} {
		t.Run(name, func(t *testing.T) {
			v := readOpusVector(t, filepath.Join(root, name+".json"))
			if v.Channels != 1 {
				t.Fatalf("%d channels", v.Channels)
			}
			replayVector(t, v, 1, readF32(t, filepath.Join(root, name+".f32le")))
		})
	}
}

// TestStereoVectors replays the SILK-only stereo sequences of
// opus/testdata/stereo-vectors on stereo and mono decoders: mid/side
// prediction and unmixing, mid-only frames, switches between mono and stereo
// packets with and without in-band FEC, bandwidth switches, losses and DTX.
func TestStereoVectors(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "stereo-vectors")
	for _, name := range []string{"loss-silk", "loss-silk-nb10", "loss-silk-mb60", "quiet-dtx", "channels-silk-fec", "bandwidth-silk"} {
		v := readOpusVector(t, filepath.Join(root, name+".json"))
		t.Run(name+"/2ch", func(t *testing.T) {
			replayVector(t, v, 2, readF32(t, filepath.Join(root, name+".f32le")))
		})
		t.Run(name+"/1ch", func(t *testing.T) {
			replayVector(t, v, 1, readF32(t, filepath.Join(root, name+".mono.f32le")))
		})
	}
}
