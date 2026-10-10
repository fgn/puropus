package puropus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// stereoPreSkip and stereoTrim are the samples per channel trimmed from the
// start and end of the libopus stereo decodes, matching the 16 kHz
// references in testdata/stereo-containers, so that streams built from the
// vectors carry both a pre-skip and end trimming.
const (
	stereoPreSkip = 317
	stereoTrim    = 269
)

// stereoStreamReference builds a Stream from one of the opus package's
// libopus stereo vectors and returns it with the libopus decode of its
// timeline.
func stereoStreamReference(t *testing.T, name string) (*Stream, []float32) {
	t.Helper()
	root := filepath.Join("opus", "testdata", "stereo-vectors")
	raw, err := os.ReadFile(filepath.Join(root, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Records []struct {
			Packet  string `json:"packet_hex"`
			Samples int    `json:"samples"`
		} `json:"records"`
	}
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	stream := &Stream{Header: Header{Channels: 2, PreSkip: stereoPreSkip}}
	var at int64
	for i, r := range vector.Records {
		data, err := hex.DecodeString(r.Packet)
		if err != nil {
			t.Fatal(err)
		}
		skip, trim := 0, 0
		if i == 0 {
			skip = stereoPreSkip
		}
		if i == len(vector.Records)-1 {
			trim = stereoTrim
		}
		n := r.Samples - skip - trim
		stream.Packets = append(stream.Packets, Packet{Data: data, Start: at, Duration: n, Skip: skip, Trim: trim})
		at += int64(n)
	}
	pcm, err := os.ReadFile(filepath.Join(root, name+".f32le"))
	if err != nil {
		t.Fatal(err)
	}
	return stream, readStereoFloats(t, pcm)[stereoPreSkip*2 : len(pcm)/4-stereoTrim*2]
}

func readStereoFloats(t *testing.T, data []byte) []float32 {
	t.Helper()
	if len(data)%8 != 0 {
		t.Fatal("incomplete stereo PCM")
	}
	return float32sLE(data)
}

func TestStereoContainerStreaming(t *testing.T) {
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(readFixture(t, "stereo-containers/manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 4 {
		t.Fatal("incomplete stereo resampler manifest")
	}
	for _, name := range []string{"channels-silk-fec", "channels-hybrid-fec", "channels-celt", "modes"} {
		stream, native := stereoStreamReference(t, name)
		for _, format := range []Format{FormatOgg, FormatWebM} {
			for _, rate := range []int{48000, 16000} {
				t.Run(fmt.Sprintf("%s/%s/%d", name, format, rate), func(t *testing.T) {
					var encoded bytes.Buffer
					if err := stream.Write(&encoded, format); err != nil {
						t.Fatal(err)
					}
					d, err := NewDecoder(&chunkReader{data: encoded.Bytes(), size: 1}, DecodeOptions{SampleRate: rate, KeepNative: true})
					if err != nil {
						t.Fatal(err)
					}
					if d.Channels() != 2 {
						t.Fatal("lost stereo header")
					}
					var got []float32
					var buf [113]float32
					for {
						buf[112] = 12345
						n, err := d.Read(buf[:])
						if n%2 != 0 || buf[112] != 12345 {
							t.Fatal("split or overran a stereo frame")
						}
						got = append(got, buf[:n]...)
						if err != nil {
							if !errors.Is(err, io.EOF) {
								t.Fatal(err)
							}
							break
						}
					}
					want := native
					if rate == 16000 {
						filename := name + ".16k.f32le"
						b := readFixture(t, "stereo-containers/"+filename)
						if fmt.Sprintf("%x", sha256.Sum256(b)) != manifest.Files[filename] {
							t.Fatal("resampler reference hash mismatch")
						}
						want = readStereoFloats(t, b)
					}
					compare(t, got, want)
					if d.Position() != samplesAtRateToDuration(int64(len(got)/2), int64(rate)) {
						t.Fatal("position counts samples rather than frames")
					}
					compare(t, d.Source().Native, native)
					d.ReleasePackets(d.Position() / 2)
					remaining := d.Source()
					if len(remaining.Packets) == 0 {
						t.Fatal("released all packets")
					}
					compare(t, remaining.Native, native[remaining.Packets[0].Start*2:])
					// Rewriting the retained window must keep both channels and packet bytes.
					cut, err := Cut(remaining, Edit{Keep: []Interval{{remaining.Packets[0].StartTime(), remaining.Duration()}}})
					if err != nil {
						t.Fatal(err)
					}
					var rewritten bytes.Buffer
					if err = cut.Write(&rewritten, format); err != nil {
						t.Fatal(err)
					}
					packets, err := ReadPackets(bytes.NewReader(rewritten.Bytes()))
					if err != nil {
						t.Fatal(err)
					}
					if packets.Header.Channels != 2 || len(packets.Packets) != len(cut.Packets) {
						t.Fatal("stereo cut changed packet count or channels")
					}
					for i, p := range packets.Packets {
						if !slices.Equal(p.Data, cut.Packets[i].Data) {
							t.Fatal("packet copy changed stereo data")
						}
					}
				})
			}
		}
	}
}

func TestStereoRejectedPacketPreservesRecovery(t *testing.T) {
	for _, name := range []string{"channels-silk-fec", "channels-hybrid-fec", "channels-celt"} {
		for _, rate := range []int{48000, 16000} {
			t.Run(fmt.Sprintf("%s/%d", name, rate), func(t *testing.T) {
				stream, _ := stereoStreamReference(t, name)
				original := stream.Packets[10].Data
				stream.Packets[10].Data = invalidHybridPacket(t, stream.Packets[10].Duration)
				var corrupt bytes.Buffer
				if err := stream.Write(&corrupt, FormatWebM); err != nil {
					t.Fatal(err)
				}
				// The same TOC with an empty frame signals a lost packet.
				stream.Packets[10].Data = []byte{original[0] & 0xfc}
				var lost bytes.Buffer
				if err := stream.Write(&lost, FormatWebM); err != nil {
					t.Fatal(err)
				}
				got, _ := decodeAll(t, &chunkReader{data: corrupt.Bytes(), size: 1}, DecodeOptions{SampleRate: rate, Tolerant: true})
				want, _ := decodeAll(t, bytes.NewReader(lost.Bytes()), DecodeOptions{SampleRate: rate})
				if !slices.Equal(got, want) {
					t.Fatal("stereo rejection changed concealment or recovery state")
				}
			})
		}
	}
}

// A stereo sample needs two values; a one-value buffer must fail instead of
// returning (0, nil) forever.
func TestStereoReadRejectsBufferShorterThanASample(t *testing.T) {
	stream, _ := stereoStreamReference(t, "channels-celt")
	var encoded bytes.Buffer
	if err := stream.WriteOgg(&encoded); err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(&encoded, DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := d.Read(make([]float32, 1)); n != 0 || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("Read(1 value) = %d, %v; want 0, io.ErrShortBuffer", n, err)
	}
	if n, err := d.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = %d, %v; want 0, nil", n, err)
	}
	if n, err := d.Read(make([]float32, 3)); n != 2 || err != nil {
		t.Fatalf("Read(3 values) = %d, %v; want one stereo sample", n, err)
	}
}
