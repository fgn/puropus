package silk_test

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/fgn/puropus/opus/internal/entcode"
	"github.com/fgn/puropus/opus/internal/silk"
)

// silkPacket is a SILK-only code 0 packet prepared for decoding: its frame
// and the silk_Decode control values opus_decode_frame would set.
type silkPacket struct {
	frame            []byte
	samples48        int // samples per channel at 48 kHz
	payloadMs        int
	channelsInternal int
	internalRate     int
}

func prepare(tb testing.TB, packets [][]byte) []silkPacket {
	tb.Helper()
	out := make([]silkPacket, 0, len(packets))
	for i, p := range packets {
		toc, frames, err := parsePacket(p)
		if err != nil || toc>>3 >= 12 || len(frames) != 1 {
			tb.Fatalf("packet %d is not a SILK-only single frame packet", i)
		}
		n := frameSamples(toc)
		out = append(out, silkPacket{
			frame:            frames[0],
			samples48:        n,
			payloadMs:        n / 48,
			channelsInternal: 1 + int(toc>>2&1),
			internalRate:     []int{8000, 12000, 16000}[toc>>5],
		})
	}
	return out
}

// decodeAll decodes packets with silk_Decode calls as opus_decode_frame
// makes them, without the float conversion of package opus.
func decodeAll(tb testing.TB, d *silk.Decoder, ctl *silk.DecControl, packets []silkPacket, pcm []int16) {
	var dec entcode.Decoder
	for i := range packets {
		p := &packets[i]
		dec.Init(p.frame)
		ctl.PayloadSizeMs = p.payloadMs
		ctl.NChannelsInternal = p.channelsInternal
		ctl.InternalSampleRate = p.internalRate
		for decoded := 0; decoded < p.samples48; {
			n, err := d.Decode(ctl, silk.FlagDecodeNormal, decoded == 0, &dec, pcm)
			if err != nil {
				tb.Fatal(err)
			}
			decoded += n
		}
	}
}

func fixturePackets(tb testing.TB, name string) []silkPacket {
	tb.Helper()
	return prepare(tb, readOggOpus(tb, filepath.Join(repoTestdata, name+".opus")).packets)
}

// stereoPackets returns the SILK packets of a stereo vector.
func stereoPackets(tb testing.TB, name string) []silkPacket {
	tb.Helper()
	v := readOpusVector(tb, filepath.Join("..", "..", "testdata", "stereo-vectors", name+".json"))
	var packets [][]byte
	for _, r := range v.Records {
		if r.Missing || len(r.Packet) <= 2 {
			continue
		}
		p, err := hex.DecodeString(r.Packet)
		if err != nil {
			tb.Fatal(err)
		}
		packets = append(packets, p)
	}
	return prepare(tb, packets)
}

// TestDecodeAllocs requires decoding and concealment not to allocate.
func TestDecodeAllocs(t *testing.T) {
	cases := []struct {
		name     string
		channels int
		packets  []silkPacket
	}{
		{"wb20/mono", 1, fixturePackets(t, "ogg-silk-wb")},
		{"nb10/mono", 1, fixturePackets(t, "ogg-silk-nb-10ms")},
		{"mb60/mono", 1, fixturePackets(t, "ogg-silk-mb-60ms")},
		{"wb20-stereo/stereo", 2, stereoPackets(t, "loss-silk")},
		{"wb20-stereo/mono", 1, stereoPackets(t, "loss-silk")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var d silk.Decoder
			d.Init()
			ctl := silk.DecControl{NChannelsAPI: c.channels, APISampleRate: 48000}
			pcm := make([]int16, 2*960)
			i := 0
			allocs := testing.AllocsPerRun(len(c.packets), func() {
				decodeAll(t, &d, &ctl, c.packets[i:i+1], pcm)
				i = (i + 1) % len(c.packets)
			})
			if allocs != 0 {
				t.Errorf("%v allocations per packet", allocs)
			}
			var dec entcode.Decoder
			ctl.PayloadSizeMs = 20
			allocs = testing.AllocsPerRun(10, func() {
				if _, err := d.Decode(&ctl, silk.FlagPacketLost, true, &dec, pcm); err != nil {
					t.Fatal(err)
				}
			})
			if allocs != 0 {
				t.Errorf("%v allocations per concealed frame", allocs)
			}
		})
	}
}

// benchmarkDecode decodes a stream repeatedly and reports the CPU time per
// second of audio.
func benchmarkDecode(b *testing.B, packets []silkPacket, channels int) {
	audio := 0
	for _, p := range packets {
		audio += p.samples48
	}
	var d silk.Decoder
	ctl := silk.DecControl{NChannelsAPI: channels, APISampleRate: 48000}
	pcm := make([]int16, 2*960)
	b.ReportAllocs()
	for b.Loop() {
		d.Init()
		decodeAll(b, &d, &ctl, packets, pcm)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/(float64(audio)/48000), "ns/audio-s")
}

// BenchmarkDecode measures SILK decoding through silk_Decode to 48 kHz
// int16 PCM, per second of audio: mono WB 20 ms (the reference case), NB
// 10 ms, MB 60 ms, and a stereo WB 20 ms stream to stereo output.
func BenchmarkDecode(b *testing.B) {
	cases := []struct {
		name     string
		channels int
		packets  []silkPacket
	}{
		{"wb20", 1, fixturePackets(b, "ogg-silk-wb")},
		{"nb10", 1, fixturePackets(b, "ogg-silk-nb-10ms")},
		{"mb60", 1, fixturePackets(b, "ogg-silk-mb-60ms")},
		{"wb20-stereo", 2, stereoPackets(b, "loss-silk")},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) { benchmarkDecode(b, c.packets, c.channels) })
	}
}

// BenchmarkConceal measures SILK packet loss concealment of WB 20 ms frames
// after a decoded stream, per second of concealed audio.
func BenchmarkConceal(b *testing.B) {
	packets := fixturePackets(b, "ogg-silk-wb")
	var (
		d   silk.Decoder
		dec entcode.Decoder
	)
	d.Init()
	ctl := silk.DecControl{NChannelsAPI: 1, APISampleRate: 48000}
	pcm := make([]int16, 960)
	decodeAll(b, &d, &ctl, packets[:20], pcm)
	ctl.PayloadSizeMs = 20
	b.ReportAllocs()
	for b.Loop() {
		if _, err := d.Decode(&ctl, silk.FlagPacketLost, true, &dec, pcm); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/0.02, "ns/audio-s")
}
