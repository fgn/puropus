package silk_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/fgn/puropus/internal/ogg"
	"github.com/fgn/puropus/opus/internal/entcode"
	"github.com/fgn/puropus/opus/internal/silk"
)

// errNotSilk reports a packet the SILK-only harness cannot decode.
var errNotSilk = errors.New("not a SILK-only packet")

// parsePacket splits an Opus packet into its frames (RFC 6716 §3.2, libopus
// opus_packet_parse_impl without self-delimiting). It returns the TOC byte.
func parsePacket(data []byte) (toc byte, frames [][]byte, err error) {
	if len(data) < 1 {
		return 0, nil, errors.New("empty packet")
	}
	toc = data[0]
	rest := data[1:]
	frameLen := func() (int, error) {
		if len(rest) < 1 {
			return 0, errors.New("missing frame length")
		}
		if rest[0] < 252 {
			n := int(rest[0])
			rest = rest[1:]
			return n, nil
		}
		if len(rest) < 2 {
			return 0, errors.New("missing frame length")
		}
		n := 4*int(rest[1]) + int(rest[0])
		rest = rest[2:]
		return n, nil
	}
	switch toc & 3 {
	case 0:
		frames = [][]byte{rest}
	case 1:
		if len(rest)%2 != 0 {
			return 0, nil, errors.New("odd code 1 packet")
		}
		h := len(rest) / 2
		frames = [][]byte{rest[:h], rest[h:]}
	case 2:
		n, err := frameLen()
		if err != nil || n > len(rest) {
			return 0, nil, errors.New("bad code 2 packet")
		}
		frames = [][]byte{rest[:n], rest[n:]}
	case 3:
		if len(rest) < 1 {
			return 0, nil, errors.New("missing frame count")
		}
		ch := rest[0]
		rest = rest[1:]
		count := int(ch & 0x3f)
		if count == 0 || count*frameSamples(toc) > 5760 {
			return 0, nil, errors.New("bad frame count")
		}
		if ch&0x40 != 0 {
			pad := 0
			for {
				if len(rest) < 1 {
					return 0, nil, errors.New("bad padding")
				}
				p := int(rest[0])
				rest = rest[1:]
				if p == 255 {
					pad += 254
					continue
				}
				pad += p
				break
			}
			if pad > len(rest) {
				return 0, nil, errors.New("bad padding")
			}
			rest = rest[:len(rest)-pad]
		}
		if ch&0x80 != 0 {
			sizes := make([]int, count)
			total := 0
			for i := range count - 1 {
				n, err := frameLen()
				if err != nil {
					return 0, nil, err
				}
				sizes[i] = n
				total += n
			}
			if total > len(rest) {
				return 0, nil, errors.New("bad VBR sizes")
			}
			sizes[count-1] = len(rest) - total
			for _, n := range sizes {
				frames = append(frames, rest[:n])
				rest = rest[n:]
			}
		} else {
			if len(rest)%count != 0 {
				return 0, nil, errors.New("bad CBR size")
			}
			n := len(rest) / count
			for range count {
				frames = append(frames, rest[:n])
				rest = rest[n:]
			}
		}
	}
	for _, f := range frames {
		if len(f) > 1275 {
			return 0, nil, errors.New("frame too large")
		}
	}
	return toc, frames, nil
}

// frameSamples is the duration of one frame of a packet at 48 kHz
// (opus_packet_get_samples_per_frame).
func frameSamples(toc byte) int {
	config := int(toc >> 3)
	switch {
	case config < 12: // SILK-only: 10, 20, 40, 60 ms
		return []int{480, 960, 1920, 2880}[config&3]
	case config < 16: // hybrid: 10, 20 ms
		return []int{480, 960}[config&1]
	default: // CELT-only: 2.5, 5, 10, 20 ms
		return 120 << (config & 3)
	}
}

// silkOpus decodes SILK-only Opus streams through package silk the way
// libopus's opus_decode_native and opus_decode_frame drive silk_Decode with
// decode_fec = 0 (src/opus_decoder.c), and converts the output to float as
// the float build does (x/32768). It rejects packets that are not SILK-only.
//
// A SILK frame may be followed by a CELT redundancy frame (libopus's encoder
// adds one to smooth SILK bandwidth switches). The harness parses its header
// as opus_decode_frame does, which the SILK decoder's final range includes,
// but cannot decode it: it records the samples libopus cross-fades with the
// CELT audio in celtSamples and sets celtRange, since libopus's final range
// also holds the CELT range.
type silkOpus struct {
	dec      silk.Decoder
	ctl      silk.DecControl
	channels int

	// The OpusDecoder fields the SILK path uses.
	prevMode       int // 0 before the first packet, else SILK
	prevRedundancy bool
	frameSize      int // st->frame_size
	streamChannels int
	bandwidthRate  int // internal rate of the last packet
	rangeFinal     uint32
	// lastPacketDuration is OPUS_GET_LAST_PACKET_DURATION.
	lastPacketDuration int

	// celtSamples lists the [start, end) sample ranges, per channel and
	// relative to the output of the last decode call, that libopus mixes
	// with CELT redundancy audio. celtRange is set when the last packet's
	// final range includes a CELT range.
	celtSamples [][2]int
	celtRange   bool

	pcm16 []int16
}

func newSilkOpus(channels int) *silkOpus {
	// opus_decoder_init sets st->frame_size to Fs/400.
	s := &silkOpus{channels: channels, frameSize: 120, pcm16: make([]int16, 5760*channels)}
	s.dec.Init()
	s.ctl = silk.DecControl{NChannelsAPI: channels, APISampleRate: 48000}
	return s
}

// decode is opus_decode_float(st, data, len(data), out, frameSize, 0) for a
// SILK-only stream; data == nil is a lost packet. It returns the samples per
// channel written to out.
func (s *silkOpus) decode(data []byte, frameSize int, out []float32) (int, error) {
	s.celtSamples = s.celtSamples[:0]
	s.celtRange = false
	if len(data) == 0 {
		// opus_decode_native's PLC loop.
		count := 0
		for count < frameSize {
			n, err := s.decodeFrame(nil, frameSize-count, out[count*s.channels:], false)
			if err != nil {
				return 0, err
			}
			count += n
		}
		s.lastPacketDuration = count
		return count, nil
	}
	toc, frames, err := parsePacket(data)
	if err != nil {
		return 0, err
	}
	if toc>>3 >= 12 {
		return 0, errNotSilk
	}
	fs := frameSamples(toc)
	if len(frames)*fs > frameSize {
		return 0, errors.New("buffer too small")
	}
	s.frameSize = fs
	s.streamChannels = 1 + int(toc>>2&1)
	s.bandwidthRate = []int{8000, 12000, 16000}[toc>>5]
	count := 0
	for _, f := range frames {
		first := len(s.celtSamples)
		n, err := s.decodeFrame(f, frameSize-count, out[count*s.channels:], false)
		if err != nil {
			return 0, err
		}
		for i := first; i < len(s.celtSamples); i++ {
			s.celtSamples[i][0] += count
			s.celtSamples[i][1] += count
		}
		count += n
	}
	s.lastPacketDuration = count
	return count, nil
}

// decodeFEC is opus_decode_float(st, data, len(data), out, frameSize, 1)
// for a SILK-only stream: it conceals the frames lost before data and
// decodes the in-band FEC (LBRR) copy of the last of them from data's first
// frame.
func (s *silkOpus) decodeFEC(data []byte, frameSize int, out []float32) (int, error) {
	toc, frames, err := parsePacket(data)
	if err != nil {
		return 0, err
	}
	if toc>>3 >= 12 {
		return 0, errNotSilk
	}
	s.celtSamples = s.celtSamples[:0]
	s.celtRange = false
	packetFrameSize := frameSamples(toc)
	// If no FEC can be present, run the PLC.
	if frameSize < packetFrameSize {
		return s.decode(nil, frameSize, out)
	}
	// Otherwise, run the PLC on everything except the size for which we
	// might have FEC.
	if frameSize-packetFrameSize != 0 {
		durationCopy := s.lastPacketDuration
		if _, err := s.decode(nil, frameSize-packetFrameSize, out); err != nil {
			s.lastPacketDuration = durationCopy
			return 0, err
		}
	}
	// Complete with FEC.
	s.frameSize = packetFrameSize
	s.streamChannels = 1 + int(toc>>2&1)
	s.bandwidthRate = []int{8000, 12000, 16000}[toc>>5]
	if _, err := s.decodeFrame(frames[0], packetFrameSize, out[(frameSize-packetFrameSize)*s.channels:], true); err != nil {
		return 0, err
	}
	s.lastPacketDuration = frameSize
	return frameSize, nil
}

// decodeFrame is opus_decode_frame for SILK-only frames, decoding the LBRR
// copy of the frame when fec is set (decode_fec).
func (s *silkOpus) decodeFrame(data []byte, frameSize int, out []float32, fec bool) (int, error) {
	const f20, f10 = 960, 480
	frameSize = min(frameSize, 5760)
	if len(data) <= 1 {
		data = nil
		// In that case, don't conceal more than what the ToC says.
		frameSize = min(frameSize, s.frameSize)
	}
	var (
		audiosize int
		dec       entcode.Decoder
	)
	if data != nil {
		audiosize = s.frameSize
		dec.Init(data)
	} else {
		audiosize = frameSize
		if s.prevRedundancy {
			return 0, errors.New("loss after CELT redundancy needs the CELT PLC")
		}
		if s.prevMode == 0 {
			// No packet yet: zeros.
			clear(out[:audiosize*s.channels])
			return audiosize, nil
		}
		if audiosize > f20 {
			for audiosize > 0 {
				n, err := s.decodeFrame(nil, min(audiosize, f20), out, false)
				if err != nil {
					return 0, err
				}
				out = out[n*s.channels:]
				audiosize -= n
			}
			return frameSize, nil
		} else if audiosize < f20 && audiosize > f10 {
			audiosize = f10
		}
	}
	if audiosize > frameSize {
		return 0, errors.New("bad frame size")
	}
	frameSize = audiosize

	// The SILK PLC cannot produce frames of less than 10 ms.
	s.ctl.PayloadSizeMs = max(10, 1000*audiosize/48000)
	if data != nil {
		s.ctl.NChannelsInternal = s.streamChannels
		s.ctl.InternalSampleRate = s.bandwidthRate
	}
	lostFlag := silk.FlagDecodeNormal
	switch {
	case data == nil:
		lostFlag = silk.FlagPacketLost
	case fec:
		lostFlag = silk.FlagDecodeLBRR
	}
	decoded := 0
	for decoded < frameSize {
		n, err := s.dec.Decode(&s.ctl, lostFlag, decoded == 0, &dec, s.pcm16[decoded*s.channels:])
		if err != nil {
			if lostFlag != silk.FlagPacketLost {
				return 0, err
			}
			// PLC failure should not be fatal.
			n = frameSize
			clear(s.pcm16[decoded*s.channels : (decoded+n)*s.channels])
		}
		decoded += n
	}
	redundancy, celtToSilk := false, false
	if !fec && data != nil && dec.Tell()+17 <= 8*len(data) {
		// A SILK-only frame with room for a redundant 0-8 kHz band.
		redundancy = true
		celtToSilk = dec.DecBitLogp(1) == 1
		redundancyBytes := len(data) - (dec.Tell()+7)>>3
		if (len(data)-redundancyBytes)*8 < dec.Tell() {
			redundancy, celtToSilk = false, false
			redundancyBytes = 0
		}
		dec.SetStorage(dec.Storage() - redundancyBytes)
	}
	for i, x := range s.pcm16[:frameSize*s.channels] {
		out[i] = float32(1.0/32768) * float32(x)
	}
	if redundancy {
		const f2p5 = 120
		if !celtToSilk {
			s.celtSamples = append(s.celtSamples, [2]int{frameSize - f2p5, frameSize})
		} else if s.prevMode != 1 || s.prevRedundancy {
			s.celtSamples = append(s.celtSamples, [2]int{0, 2 * f2p5})
		}
		s.celtRange = true
	}
	s.prevMode = 1
	s.prevRedundancy = redundancy && !celtToSilk
	if data != nil {
		s.rangeFinal = dec.Rng()
	} else {
		s.rangeFinal = 0
	}
	return frameSize, nil
}

// bitRecord is one record of an opus_demo bitstream file.
type bitRecord struct {
	packet []byte
	rng    uint32
}

// readBitFile reads an opus_demo bitstream (4-byte big-endian length, 4-byte
// big-endian final range, payload).
func readBitFile(t testing.TB, path string) []bitRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recs []bitRecord
	for len(raw) > 0 {
		if len(raw) < 8 {
			t.Fatalf("%s: truncated record", path)
		}
		n := int(binary.BigEndian.Uint32(raw))
		rng := binary.BigEndian.Uint32(raw[4:])
		raw = raw[8:]
		if n > len(raw) {
			t.Fatalf("%s: truncated payload", path)
		}
		recs = append(recs, bitRecord{packet: raw[:n:n], rng: rng})
		raw = raw[n:]
	}
	return recs
}

// oggStream is the audio packets and pre-skip of an Ogg Opus file.
type oggStream struct {
	channels int
	preSkip  int
	packets  [][]byte
}

func readOggOpus(t testing.TB, path string) oggStream {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := ogg.NewReader(bytes.NewReader(raw))
	var s oggStream
	for i := 0; ; i++ {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			if !bytes.HasPrefix(p.Data, []byte("OpusHead")) || len(p.Data) < 19 {
				t.Fatalf("%s: no OpusHead", path)
			}
			s.channels = int(p.Data[9])
			s.preSkip = int(binary.LittleEndian.Uint16(p.Data[10:]))
		case 1:
			if !bytes.HasPrefix(p.Data, []byte("OpusTags")) {
				t.Fatalf("%s: no OpusTags", path)
			}
		default:
			s.packets = append(s.packets, p.Data)
		}
	}
	return s
}

// rangeRecord is one line of a decode_float ranges file.
type rangeRecord struct {
	bytes, samples int
	rng            uint32
}

// readRanges reads a decode_float ranges file (index, payload bytes,
// samples per channel, final range in hex), skipping '#' lines.
func readRanges(t testing.TB, path string) []rangeRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recs []rangeRecord
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) != 4 {
			t.Fatalf("%s: bad line %q", path, sc.Text())
		}
		b, err1 := strconv.Atoi(fields[1])
		n, err2 := strconv.Atoi(fields[2])
		r, err3 := strconv.ParseUint(fields[3], 16, 32)
		if err := errors.Join(err1, err2, err3); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		recs = append(recs, rangeRecord{bytes: b, samples: n, rng: uint32(r)})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

// readF32 reads a little-endian float32 file.
func readF32(t testing.TB, path string) []float32 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%4 != 0 {
		t.Fatalf("%s: size %d is not a multiple of 4", path, len(raw))
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return out
}

// readS16 reads a little-endian int16 file.
func readS16(t testing.TB, path string) []int16 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]int16, len(raw)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return out
}

// pcmMismatch describes the first difference between got and want, or
// returns "" when they are bit-identical.
func pcmMismatch(got, want []float32) string {
	if len(got) != len(want) {
		return fmt.Sprintf("%d samples, want %d", len(got), len(want))
	}
	diffs, worst := 0, 0.0
	first := -1
	for i := range want {
		if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
			if first < 0 {
				first = i
			}
			diffs++
			worst = max(worst, math.Abs(float64(got[i])-float64(want[i])))
		}
	}
	if diffs == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d samples differ, first at %d (%v, want %v), largest difference %.3g",
		diffs, len(want), first, got[first], want[first], worst)
}
