package celt

import (
	"math"
	"testing"
)

// FuzzDecodeWithEC decodes arbitrary frames with arbitrary configurations
// and losses: the decoder must not panic and its output must stay finite.
// The first byte of the input selects the configuration, the rest is a
// sequence of frames, each preceded by its length.
func FuzzDecodeWithEC(f *testing.F) {
	for _, name := range []string{"webm-celt-20ms", "stereo-transients-celt", "transition-loss-celt-2p5ms"} {
		recs := readBit(f, "testdata/"+name+".bit")
		for i := 0; i+3 < len(recs) && i < 12; i += 3 {
			in := []byte{byte(i)}
			for _, r := range recs[i : i+3] {
				if len(r.packet) > 1 {
					in = append(in, byte(len(r.packet)-1))
					in = append(in, r.packet[1:]...)
				} else {
					in = append(in, 0)
				}
			}
			f.Add(in)
		}
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) < 1 {
			return
		}
		cfg := in[0]
		in = in[1:]
		channels := 1 + int(cfg&1)
		rates := [...]int{48000, 48000, 16000, 24000}
		rate := rates[cfg>>1&3]
		d, err := NewDecoder(rate, channels)
		if err != nil {
			t.Fatal(err)
		}
		LM := int(cfg >> 3 & 3)
		frameSize := (120 << LM) * rate / 48000
		ends := [...]int{21, 13, 17, 19}
		if err = d.SetEndBand(ends[cfg>>5&3]); err != nil {
			t.Fatal(err)
		}
		if cfg&0x80 != 0 {
			if err = d.SetStartBand(17); err != nil {
				t.Fatal(err)
			}
		}
		pcm := make([]float32, 960*channels)
		for k := 0; len(in) > 0; k++ {
			n := min(int(in[0]), len(in)-1)
			frame := in[1 : 1+n]
			in = in[1+n:]
			if err = d.SetStreamChannels(1 + k&1*int(cfg>>2&1)); err != nil {
				t.Fatal(err)
			}
			got, err := d.DecodeWithEC(frame, pcm, frameSize, nil)
			if err != nil {
				continue
			}
			for _, v := range pcm[:got*channels] {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					t.Fatalf("frame %d: non-finite output", k)
				}
			}
		}
	})
}
