package puropus

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fgn/puropus/internal/resample"
	"github.com/fgn/puropus/opus"
)

// decodeLogCase is a case of opus/testdata/decode-logs/manifest.json, the
// libopus references of opus/decode_logs_test.go (tools/libopus-ref/
// decode-logs.sh): a packet list, the records lost from it, the FEC policy
// and the SHA-256 of libopus's decode.
type decodeLogCase struct {
	Name   string `json:"name"`
	Source struct {
		Fixture  string `json:"fixture"`
		Vector   string `json:"vector"`
		Splice   string `json:"splice"`
		Segments []struct {
			Fixture string `json:"fixture"`
			First   int    `json:"first"`
			Count   int    `json:"count"`
		} `json:"segments"`
	} `json:"source"`
	Channels            int    `json:"channels"`
	Skip                int    `json:"skip"`
	Trim                int    `json:"trim"`
	Drop                string `json:"drop"`
	FEC                 string `json:"fec"`
	FECRecords          int    `json:"fec_records"`
	PCMSHA256           string `json:"pcm_sha256"`
	DefaultBuildDiffers int    `json:"default_build_pcm_differs"`
}

// exact reports whether the case's PCM must have libopus's hash here: on
// amd64 and 386, and for the cases both libopus builds decode alike (integer
// SILK output), as in opus/decode_logs_test.go.
func (c decodeLogCase) exact() bool {
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "386" || c.DefaultBuildDiffers == 0
}

// dropped returns the record indices the case loses.
func (c decodeLogCase) dropped(t *testing.T) map[int]bool {
	t.Helper()
	drop := map[int]bool{}
	for _, field := range strings.Split(c.Drop, ",") {
		if field == "" {
			continue
		}
		first, last, ok := strings.Cut(field, "-")
		a, err := strconv.Atoi(first)
		if err != nil {
			t.Fatal(err)
		}
		b := a
		if ok {
			if b, err = strconv.Atoi(last); err != nil {
				t.Fatal(err)
			}
		}
		for i := a; i <= b; i++ {
			drop[i] = true
		}
	}
	return drop
}

// tolerantFECCases returns the decode-logs cases the Tolerant Decoder
// reproduces: losses of whole packets recovered with the "lbrr" policy,
// decoded to the stream's own channel count.
func tolerantFECCases(t *testing.T) []decodeLogCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("opus", "testdata", "decode-logs", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases []decodeLogCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var cases []decodeLogCase
	for _, c := range manifest.Cases {
		if c.FEC != "lbrr" {
			continue
		}
		mono := c.Source.Fixture != "" || c.Source.Splice != ""
		if (mono && c.Channels == 1) || (c.Source.Vector != "" && c.Channels == 2) {
			cases = append(cases, c)
		}
	}
	if len(cases) < 8 {
		t.Fatalf("only %d Tolerant FEC cases", len(cases))
	}
	return cases
}

// decodeLogStream builds the stream of a case: a fixture's packets with its
// pre-skip and end trimming, or the packets of a sequence or splice with
// neither, as decode-logs.sh decodes them.
func decodeLogStream(t *testing.T, c decodeLogCase) *Stream {
	t.Helper()
	switch {
	case c.Source.Fixture != "":
		src := sourceFor(t, c.Source.Fixture)
		return &Stream{Header: src.Header, Packets: slices.Clone(src.Packets)}
	case c.Source.Vector != "":
		raw, err := os.ReadFile(filepath.Join("opus", "testdata", c.Source.Vector+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Records []struct {
				Missing bool   `json:"missing"`
				Packet  string `json:"packet_hex"`
			} `json:"records"`
		}
		if err := json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		var packets [][]byte
		for _, r := range vector.Records {
			if r.Missing {
				t.Fatal("sequence with lost records")
			}
			p, err := hex.DecodeString(r.Packet)
			if err != nil {
				t.Fatal(err)
			}
			packets = append(packets, p)
		}
		return packetStream(t, c.Channels, packets)
	case c.Source.Splice != "":
		var packets [][]byte
		for _, s := range c.Source.Segments {
			packets = append(packets, fixturePackets(t, s.Fixture)[s.First:s.First+s.Count]...)
		}
		return packetStream(t, c.Channels, packets)
	}
	t.Fatalf("case %s has no source", c.Name)
	return nil
}

// fixturePackets returns the packet data of a fixture.
func fixturePackets(t *testing.T, name string) [][]byte {
	t.Helper()
	src := sourceFor(t, name)
	packets := make([][]byte, 0, len(src.Packets))
	for _, p := range src.Packets {
		packets = append(packets, p.Data)
	}
	return packets
}

// packetStream lays packets out as a stream without pre-skip or trimming.
func packetStream(t *testing.T, channels int, packets [][]byte) *Stream {
	t.Helper()
	s := &Stream{Header: Header{Channels: channels}}
	var at int64
	for _, data := range packets {
		parsed, err := opus.Parse(data, false)
		if err != nil {
			t.Fatal(err)
		}
		n := parsed.Duration48()
		s.Packets = append(s.Packets, Packet{Data: data, Start: at, Duration: n})
		at += int64(n)
	}
	return s
}

// tolerantReplay is what the Tolerant Decoder must return at 48 kHz for a
// stream of one link, made with the opus package directly: a packet that
// DecodeParsed rejects is decoded with DecodeFEC from the next packet when
// HasLBRR reports in-band FEC in it, and concealed otherwise; the pre-skip
// and end trimming apply to the result. The output gain must be 0 dB.
func tolerantReplay(t *testing.T, s *Stream) []float32 {
	t.Helper()
	d, err := opus.NewDecoder(s.Header.Channels)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, opus.MaxPacketSamples*s.Header.Channels)
	var pcm []float32
	for i, p := range s.Packets {
		parsed, err := opus.Parse(p.Data, false)
		if err != nil {
			t.Fatal(err)
		}
		n, err := d.DecodeParsed(parsed, p.Data, out)
		switch {
		case err == nil:
		case i+1 < len(s.Packets) && opus.HasLBRR(s.Packets[i+1].Data):
			n, err = d.DecodeFEC(s.Packets[i+1].Data, parsed.Duration48(), out)
		default:
			n, err = d.Conceal(parsed.Duration48(), out)
		}
		if err != nil {
			t.Fatal(err)
		}
		if n != p.Coded() {
			t.Fatalf("packet %d: %d samples, the stream says %d", i, n, p.Coded())
		}
		c := s.Header.Channels
		pcm = append(pcm, out[p.Skip*c:(n-p.Trim)*c]...)
	}
	return pcm
}

// resampleTo16k resamples interleaved 48 kHz PCM as the Decoder does for a
// 16 kHz output, which does not depend on how the input is split.
func resampleTo16k(pcm []float32, channels int) []float32 {
	frames := len(pcm) / channels
	outs := make([][]float32, channels)
	for c := range channels {
		in := make([]float32, frames)
		for i := range in {
			in[i] = pcm[i*channels+c]
		}
		var r resample.Resampler48To16
		buf := make([]float32, frames/3+256)
		n := r.Process(in, buf)
		outs[c] = append(outs[c], buf[:n]...)
		for {
			n = r.Flush(buf)
			if n == 0 {
				break
			}
			outs[c] = append(outs[c], buf[:n]...)
		}
	}
	out := make([]float32, 0, len(outs[0])*channels)
	for i := range outs[0] {
		for c := range channels {
			out = append(out, outs[c][i])
		}
	}
	return out
}

// pcmSHA256 is the SHA-256 of PCM as float32 little-endian bytes.
func pcmSHA256(pcm []float32) string {
	raw := make([]byte, 4*len(pcm))
	for i, v := range pcm {
		binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(v))
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// TestTolerantRecoversFromFEC loses packets of SILK, hybrid and mixed
// streams, mono and stereo, by replacing them with packets that parse but do
// not decode, and decodes the result in Tolerant mode from Ogg and WebM, fed
// a byte at a time and read in odd sizes. Each lost packet must come out as
// a libopus application decodes it: from the next packet's in-band FEC with
// opus_decode_float(next, decode_fec = 1) for the lost duration when the
// next packet carries LBRR data, concealed otherwise. The 48 kHz output must
// have the SHA-256 of libopus's generic build for the same losses
// (opus/testdata/decode-logs, the "lbrr" cases; see exact for other
// architectures) and equal tolerantReplay, the 16 kHz output its
// resampling, and the timeline, the retained packets and the retained
// native PCM must be those of the stream.
func TestTolerantRecoversFromFEC(t *testing.T) {
	for _, c := range tolerantFECCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			stream := decodeLogStream(t, c)
			if links, err := stream.links(true); err != nil || len(links) != 1 || links[0].preSkip != c.Skip || links[0].trim != c.Trim {
				t.Fatalf("stream links %+v, the reference has pre-skip %d and trimming %d (%v)", links, c.Skip, c.Trim, err)
			}
			fec := 0
			drop := c.dropped(t)
			for i := range drop {
				if i+1 < len(stream.Packets) && !drop[i+1] && opus.HasLBRR(stream.Packets[i+1].Data) {
					fec++
				}
				invalid := invalidHybridPacket(t, stream.Packets[i].Coded())
				if opus.HasLBRR(invalid) {
					t.Fatal("the undecodable packet carries LBRR data")
				}
				stream.Packets[i].Data = invalid
			}
			if fec != c.FECRecords || fec == 0 {
				t.Fatalf("%d losses before LBRR data, the reference decoded %d with FEC", fec, c.FECRecords)
			}
			want := tolerantReplay(t, stream)
			if got := pcmSHA256(want); got != c.PCMSHA256 {
				if c.exact() {
					t.Fatalf("expected PCM SHA-256 %s, libopus %s", got, c.PCMSHA256)
				}
				t.Logf("expected PCM differs from libopus's on %s", runtime.GOARCH)
			}
			want16 := resampleTo16k(want, c.Channels)
			for _, format := range []Format{FormatOgg, FormatWebM} {
				var encoded bytes.Buffer
				if err := stream.Write(&encoded, format); err != nil {
					t.Fatal(err)
				}
				strict, err := NewDecoder(bytes.NewReader(encoded.Bytes()), DecodeOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := readAll(strict, make([]float32, 4096)); errors.Is(err, io.EOF) {
					t.Fatalf("%v: the strict decoder accepted the lost packets", format)
				}
				d, err := NewDecoder(&chunkReader{data: encoded.Bytes(), size: 1}, DecodeOptions{Tolerant: true, KeepNative: true})
				if err != nil {
					t.Fatal(err)
				}
				got, err := readAll(d, make([]float32, 113*c.Channels))
				if !errors.Is(err, io.EOF) {
					t.Fatal(err)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%v: Tolerant output differs from FEC recovery", format)
				}
				if !slices.Equal(d.Source().Native, want) {
					t.Fatalf("%v: retained native PCM differs", format)
				}
				packets := d.Source().Packets
				if len(packets) != len(stream.Packets) {
					t.Fatalf("%v: %d retained packets, want %d", format, len(packets), len(stream.Packets))
				}
				for i, p := range packets {
					w := stream.Packets[i]
					if p.Start != w.Start || p.Duration != w.Duration || p.Skip != w.Skip || p.Trim != w.Trim || !bytes.Equal(p.Data, w.Data) {
						t.Fatalf("%v: retained packet %d is %+v, want %+v", format, i, p, w)
					}
				}
				if d.Position() != stream.Duration() {
					t.Fatalf("%v: position %v, want %v", format, d.Position(), stream.Duration())
				}
				got16, _ := decodeAll(t, &chunkReader{data: encoded.Bytes(), size: 7}, DecodeOptions{Tolerant: true, SampleRate: 16000})
				if !slices.Equal(got16, want16) {
					t.Fatalf("%v: 16 kHz Tolerant output is not the resampled 48 kHz output", format)
				}
			}
		})
	}
}

// TestTolerantFECStaysInItsLink loses the last packet of an Ogg link that is
// followed by a link whose first packet carries LBRR data. The next link is
// another stream, decoded after a reset, so the lost packet is concealed:
// the chain decodes as its links do alone.
func TestTolerantFECStaysInItsLink(t *testing.T) {
	packets := fixturePackets(t, "ogg-silk-wb-fec.opus")
	first := packetStream(t, 1, packets[:30])
	second := packetStream(t, 1, packets[30:60])
	if !opus.HasLBRR(second.Packets[0].Data) {
		t.Fatal("the second link starts without LBRR data")
	}
	first.Packets[29].Data = invalidHybridPacket(t, first.Packets[29].Coded())
	var a, b bytes.Buffer
	if err := first.WriteOgg(&a); err != nil {
		t.Fatal(err)
	}
	if err := second.WriteOgg(&b); err != nil {
		t.Fatal(err)
	}
	alone, _ := decodeAll(t, bytes.NewReader(a.Bytes()), DecodeOptions{Tolerant: true})
	if !slices.Equal(alone, tolerantReplay(t, first)) {
		t.Fatal("a lost last packet is not concealed")
	}
	next, _ := decodeAll(t, bytes.NewReader(b.Bytes()), DecodeOptions{})
	chain, _ := decodeAll(t, &chunkReader{data: append(a.Bytes(), b.Bytes()...), size: 1}, DecodeOptions{Tolerant: true})
	if !slices.Equal(chain, append(alone, next...)) {
		t.Fatal("the lost packet used the next link's FEC")
	}
}

// TestTolerantReportsErrorAfterRecoveredPacket ends an Ogg stream inside the
// page after a lost packet. The lost packet is concealed, since no next
// packet arrives, and delivered before the truncation error.
func TestTolerantReportsErrorAfterRecoveredPacket(t *testing.T) {
	s := packetStream(t, 1, fixturePackets(t, "ogg-silk-wb-fec.opus")[:100])
	s.Packets[49].Data = invalidHybridPacket(t, s.Packets[49].Coded())
	var encoded bytes.Buffer
	if err := s.WriteOgg(&encoded); err != nil {
		t.Fatal(err)
	}
	// Pages hold about a second, 50 packets: cut the second page short.
	data := encoded.Bytes()
	pages := oggPages(t, data)
	if len(pages) < 4 {
		t.Fatalf("%d pages", len(pages))
	}
	cut := data[:pages[3].start+pages[3].size/2]
	d, err := NewDecoder(&chunkReader{data: cut, size: 3}, DecodeOptions{Tolerant: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := readAll(d, make([]float32, 1000))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	want := tolerantReplay(t, &Stream{Header: s.Header, Packets: s.Packets[:50]})
	if !slices.Equal(got, want) {
		t.Fatalf("got %d samples, want the %d of the first 50 packets with the last concealed", len(got), len(want))
	}
}
