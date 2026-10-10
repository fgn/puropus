package puropus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/fgn/puropus/opus"
)

// Byte-at-a-time input and odd read sizes give the same PCM as a whole-file
// decode, and the retained native PCM stays aligned with the packets across
// ReleasePackets.
func TestSilkStreamingRetainsAlignedPCM(t *testing.T) {
	for _, name := range []string{"ogg-silk-nb-10ms.opus", "ogg-silk-mb-60ms.opus", "ogg-silk-wb-60ms-fec.opus", "ogg-hybrid-fb-10ms.opus", "ogg-silk-bandwidth-switch.opus"} {
		for _, rate := range []int{16000, 48000} {
			t.Run(fmt.Sprintf("%s/%d", name, rate), func(t *testing.T) {
				data := readFixture(t, name)
				want, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{SampleRate: rate})
				native, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
				d, err := NewDecoder(&chunkReader{data: data, size: 1}, DecodeOptions{SampleRate: rate, KeepNative: true})
				if err != nil {
					t.Fatal(err)
				}
				got, err := readAll(d, make([]float32, 113))
				if !errors.Is(err, io.EOF) {
					t.Fatal(err)
				}
				if !slices.Equal(got, want) {
					t.Fatal("fragmented input/odd output reads changed PCM")
				}
				if !slices.Equal(d.Source().Native, native) {
					t.Fatal("retained PCM differs from native decode")
				}
				d.ReleasePackets(d.Position() / 2)
				src := d.Source()
				if len(src.Packets) == 0 {
					t.Fatal("released all packets")
				}
				if !slices.Equal(src.Native, native[src.Packets[0].Start:]) {
					t.Fatal("release misaligned retained PCM")
				}
			})
		}
	}
}

// A strict packet failure drains the output resampler just as EOF does, so
// the last samples of the valid prefix are delivered at 16 kHz too.
func TestSilkStrictFailureReturnsCompletePrefix(t *testing.T) {
	for _, name := range []string{"ogg-silk-nb-10ms.opus", "ogg-silk-mb-60ms.opus", "ogg-silk-wb-60ms-fec.opus", "ogg-hybrid-fb-10ms.opus", "webm-celt-60ms.webm"} {
		t.Run(name, func(t *testing.T) {
			src := sourceFor(t, name)
			target := src.Packets[10]
			whole, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := Cut(src, Edit{Keep: []Interval{{0, samplesToDuration(target.Start)}}, Snap: SnapInward})
			if err != nil {
				t.Fatal(err)
			}
			whole.Packets[10].Data = invalidHybridPacket(t, target.Duration)
			var encoded, good bytes.Buffer
			if err = whole.Write(&encoded, FormatWebM); err != nil {
				t.Fatal(err)
			}
			if err = prefix.Write(&good, FormatWebM); err != nil {
				t.Fatal(err)
			}
			bad := encoded.Bytes()
			for _, rate := range []int{16000, 48000} {
				want, _ := decodeAll(t, bytes.NewReader(good.Bytes()), DecodeOptions{SampleRate: rate})
				expected := int((target.Start*int64(rate) + SampleRate - 1) / SampleRate)
				if len(want) != expected {
					t.Fatalf("%dHz: EOF prefix has %d samples, TOC timeline requires %d", rate, len(want), expected)
				}
				d, err := NewDecoder(bytes.NewReader(bad), DecodeOptions{SampleRate: rate})
				if err != nil {
					t.Fatal(err)
				}
				buf := make([]float32, 113)
				got, err := readAll(d, buf)
				if errors.Is(err, io.EOF) || errors.Is(err, ErrTruncated) {
					t.Fatalf("wrong strict error: %v", err)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%dHz: prefix has %d samples, want %d, or differs", rate, len(got), len(want))
				}
				if n, e := d.Read(buf); n != 0 || !errors.Is(e, err) {
					t.Fatalf("terminal read = %d, %v", n, e)
				}
			}
			// Replacing the rejected packet with explicit lost frames gives an
			// independent complete-stream expectation, including recovery,
			// unless the next packet carries in-band FEC, which the rejected
			// packet is then decoded from (tolerantReplay).
			rejected := &Stream{Header: whole.Header, Packets: slices.Clone(whole.Packets)}
			fec := opus.HasLBRR(whole.Packets[11].Data)
			parsed, err := opus.Parse(target.Data, false)
			if err != nil {
				t.Fatal(err)
			}
			whole.Packets[10].Data = []byte{target.Data[0]&0xfc | 3, byte(parsed.FrameCount)}
			var lost bytes.Buffer
			if err := whole.Write(&lost, FormatWebM); err != nil {
				t.Fatal(err)
			}
			for _, rate := range []int{16000, 48000} {
				tolerant, _ := decodeAll(t, &chunkReader{data: bad, size: 1}, DecodeOptions{Tolerant: true, SampleRate: rate})
				want, _ := decodeAll(t, bytes.NewReader(lost.Bytes()), DecodeOptions{SampleRate: rate})
				if fec {
					want = tolerantReplay(t, rejected)
					if rate != SampleRate {
						want = resampleTo16k(want, rejected.Header.Channels)
					}
				}
				if !slices.Equal(tolerant, want) {
					t.Fatalf("%dHz: tolerant concealment, FEC or recovery differs from a signalled loss (FEC %v)", rate, fec)
				}
				if len(tolerant) != int((src.Packets[len(src.Packets)-1].End()*int64(rate)+SampleRate-1)/SampleRate) {
					t.Fatal("tolerant duration changed")
				}
			}
		})
	}
}

// A chain of SILK links, the middle one with a +6 dB output gain, decodes to
// the concatenation of the links decoded on their own.
func TestSilkChainedLinksResetStateAndApplyGain(t *testing.T) {
	var chain []byte
	var want []float32
	for i, name := range []string{"ogg-silk-nb-10ms.opus", "ogg-silk-mb-60ms.opus", "ogg-silk-nb-10ms.opus"} {
		data := readFixture(t, name)
		if i == 1 {
			at := bytes.Index(data, []byte("OpusHead"))
			if at < 0 {
				t.Fatal("missing OpusHead")
			}
			binary.LittleEndian.PutUint16(data[at+16:], 256*6)
			fixOggCRC(data, oggPages(t, data)[0])
		}
		pcm, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
		want = append(want, pcm...)
		chain = append(chain, data...)
	}
	got, _ := decodeAll(t, &chunkReader{data: chain, size: 1}, DecodeOptions{})
	if !slices.Equal(got, want) {
		t.Fatal("link reset, gain or trimming changed PCM")
	}
}
