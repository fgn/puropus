package celt

import (
	"math/rand"
	"testing"
	"time"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// BenchmarkDecode decodes the CELT fixtures' packets, reporting the time
// per second of audio: webm-celt-60ms is mono fullband CELT in 60 ms
// packets (three 20 ms frames each) at about 64 kb/s.
func BenchmarkDecode(b *testing.B) {
	for _, stem := range []string{"webm-celt-60ms", "webm-celt-20ms", "ogg-celt-fb-10ms", "stereo-transients-celt"} {
		b.Run(stem, func(b *testing.B) {
			recs := readBit(b, "testdata/"+stem+".bit")
			channels := 1
			var frames [][]byte
			var frameSize int
			for _, r := range recs {
				f, err := parseFrames(r.packet)
				if err != nil {
					b.Fatal(err)
				}
				frames = append(frames, f...)
				frameSize = 120 << (r.packet[0] >> 3 & 3)
				if r.packet[0]&4 != 0 {
					channels = 2
				}
			}
			d, err := NewDecoder(48000, channels)
			if err != nil {
				b.Fatal(err)
			}
			pcm := make([]float32, 960*channels)
			audio := time.Duration(len(frames)*frameSize) * time.Second / 48000
			b.ReportAllocs()
			var elapsed time.Duration
			n := 0
			for b.Loop() {
				start := time.Now()
				d.Reset()
				for _, f := range frames {
					if _, err := d.DecodeWithEC(f, pcm, frameSize, nil); err != nil {
						b.Fatal(err)
					}
				}
				elapsed += time.Since(start)
				n++
			}
			b.ReportMetric(float64(elapsed.Nanoseconds())/float64(n)/audio.Seconds(), "ns/audio-s")
			b.ReportMetric(float64(len(frames)), "frames/op")
		})
	}
}

// BenchmarkInverseMDCT960 computes the 1920-point inverse MDCT of a 20 ms
// frame (960 outputs) with the overlap-add.
func BenchmarkInverseMDCT960(b *testing.B) {
	m := StaticMode()
	rng := rand.New(rand.NewSource(1))
	in := make([]float32, 960)
	for i := range in {
		in[i] = float32(rng.NormFloat64())
	}
	out := make([]float32, 960+m.overlap)
	b.ReportAllocs()
	for b.Loop() {
		m.mdct.cltMdctBackward(in, out, m.window, m.overlap, 0, 1)
	}
}

// BenchmarkAlgUnquant decodes and normalises one band's pulses: N
// dimensions, K pulses (the most the pulse cache allows for N is used for
// the larger bands, which are split before that), normal spreading.
func BenchmarkAlgUnquant(b *testing.B) {
	for _, tc := range []struct {
		name    string
		N, K, B int
	}{
		{"N8K8", 8, 8, 1},
		{"N16K12", 16, 12, 1},
		{"N32K7", 32, 7, 1},
		{"N88K5B4", 88, 5, 4},
		{"N176K4", 176, 4, 1},
	} {
		b.Run(tc.name, func(b *testing.B) {
			data := make([]byte, 64)
			rand.New(rand.NewSource(2)).Read(data)
			var iy [maxBandSize]int32
			X := make([]float32, tc.N)
			var dec entcode.Decoder
			b.ReportAllocs()
			for b.Loop() {
				dec.Init(data)
				algUnquant(X, tc.N, tc.K, spreadNormal, tc.B, &dec, 1, iy[:])
			}
		})
	}
}

// BenchmarkQuantAllBands decodes the shape of all bands of a 20 ms frame
// from random bytes after its allocation, mono and stereo.
func BenchmarkQuantAllBands(b *testing.B) {
	m := StaticMode()
	for _, tc := range []struct {
		name  string
		C, LM int
		bytes int
		short bool
	}{
		{"mono-20ms-160B", 1, 3, 160, false},
		{"mono-20ms-transient-160B", 1, 3, 160, true},
		{"stereo-20ms-320B", 2, 3, 320, false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			data := make([]byte, tc.bytes)
			rand.New(rand.NewSource(3)).Read(data)
			var caps, offsets, pulses, ebits, prio, tf [maxBands]int
			var intensity, balance int
			var dual bool
			initCaps(m, caps[:], tc.LM, tc.C)
			var dec entcode.Decoder
			dec.Init(data)
			total := len(data)*8<<bitRes - dec.TellFrac() - 1
			coded := cltComputeAllocation(m, 0, maxBands, offsets[:], caps[:], 5, &intensity, &dual, total,
				&balance, pulses[:], ebits[:], prio[:], tc.C, tc.LM, &dec)
			saved := dec
			N := m.shortMdctSize << tc.LM
			X := make([]float32, tc.C*N)
			var Y []float32
			if tc.C == 2 {
				Y = X[N:]
			}
			var masks [2 * maxBands]uint8
			var ctx bandCtx
			var norm [maxNormSize]float32
			b.ReportAllocs()
			for b.Loop() {
				dec = saved
				seed := uint32(1)
				quantAllBands(&ctx, m, 0, maxBands, X[:N], Y, masks[:tc.C*maxBands], pulses[:], tc.short,
					spreadNormal, dual, intensity, tf[:], len(data)*(8<<bitRes), balance, &dec, tc.LM, coded,
					&seed, false, norm[:])
			}
		})
	}
}
