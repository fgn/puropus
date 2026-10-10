package celt

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// This file drives the CELT decoder over whole CELT-only Opus streams the
// way libopus's opus_decode_native and opus_decode_frame drive the CELT
// layer, as tools/libopus-ref/celt_decode.c does in C: packets are split
// into frames (RFC 6716 §3.2) here because the tests cannot import package
// opus, which imports this package.

// record is one entry of an opus_demo packet list: a packet, or a loss when
// packet is empty, and the final range field.
type record struct {
	packet []byte
	rng    uint32
}

// readBit reads an opus_demo packet list (tools/libopus-ref/README.md).
func readBit(t testing.TB, path string) []record {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recs []record
	for len(raw) > 0 {
		if len(raw) < 8 {
			t.Fatalf("%s: truncated record header", path)
		}
		n := int(binary.BigEndian.Uint32(raw))
		rng := binary.BigEndian.Uint32(raw[4:])
		raw = raw[8:]
		if n > len(raw) {
			t.Fatalf("%s: truncated record", path)
		}
		recs = append(recs, record{packet: raw[:n:n], rng: rng})
		raw = raw[n:]
	}
	return recs
}

// parseFrames splits an Opus packet into its frames (RFC 6716 §3.2).
func parseFrames(p []byte) ([][]byte, error) {
	if len(p) < 1 {
		return nil, errors.New("empty packet")
	}
	data := p[1:]
	// frameLength reads a frame length coded in one or two bytes.
	frameLength := func() (int, error) {
		if len(data) < 1 {
			return 0, errors.New("missing frame length")
		}
		if data[0] < 252 {
			n := int(data[0])
			data = data[1:]
			return n, nil
		}
		if len(data) < 2 {
			return 0, errors.New("missing frame length")
		}
		n := int(data[0]) + 4*int(data[1])
		data = data[2:]
		return n, nil
	}
	switch p[0] & 3 {
	case 0:
		return [][]byte{data}, nil
	case 1:
		if len(data)%2 != 0 {
			return nil, errors.New("odd code 1 packet")
		}
		h := len(data) / 2
		return [][]byte{data[:h], data[h:]}, nil
	case 2:
		n, err := frameLength()
		if err != nil {
			return nil, err
		}
		if n > len(data) {
			return nil, errors.New("code 2 frame too long")
		}
		return [][]byte{data[:n], data[n:]}, nil
	}
	if len(data) < 1 {
		return nil, errors.New("missing frame count")
	}
	vbr := data[0]&0x80 != 0
	padded := data[0]&0x40 != 0
	count := int(data[0] & 0x3f)
	data = data[1:]
	if count == 0 {
		return nil, errors.New("no frames")
	}
	padding := 0
	for padded {
		if len(data) < 1 {
			return nil, errors.New("missing padding length")
		}
		b := int(data[0])
		data = data[1:]
		if b == 255 {
			padding += 254
		} else {
			padding += b
			padded = false
		}
	}
	sizes := make([]int, count)
	if vbr {
		for i := 0; i < count-1; i++ {
			n, err := frameLength()
			if err != nil {
				return nil, err
			}
			sizes[i] = n
		}
	}
	if padding > len(data) {
		return nil, errors.New("padding exceeds the packet")
	}
	data = data[:len(data)-padding]
	if vbr {
		last := len(data)
		for _, n := range sizes[:count-1] {
			last -= n
		}
		if last < 0 {
			return nil, errors.New("frames exceed the packet")
		}
		sizes[count-1] = last
	} else {
		if len(data)%count != 0 {
			return nil, errors.New("uneven CBR frames")
		}
		for i := range sizes {
			sizes[i] = len(data) / count
		}
	}
	frames := make([][]byte, count)
	for i, n := range sizes {
		if n > 1275 {
			return nil, errors.New("frame too long")
		}
		frames[i] = data[:n:n]
		data = data[n:]
	}
	return frames, nil
}

// frameLog is one CELT call or zero fill of a stream decode, as
// tools/libopus-ref/celt_decode.c prints it.
type frameLog struct {
	record, frame  int
	kind           string // "frame", "plc" or "zeros"
	bytes, samples int
	endBand        int
	streamChannels int
	rng            uint32
	// hash is the FNV-1a hash of the frame's output PCM bytes.
	hash uint32
}

func (f frameLog) String() string {
	return fmt.Sprintf("%d %d %s %d %d %d %d %08x %08x", f.record, f.frame, f.kind, f.bytes, f.samples, f.endBand,
		f.streamChannels, f.rng, f.hash)
}

// pcmHash returns the 32-bit FNV-1a hash of the little-endian bytes of pcm,
// as celt_decode computes it.
func pcmHash(pcm []float32) uint32 {
	h := uint32(2166136261)
	for _, v := range pcm {
		u := math.Float32bits(v)
		for k := 0; k < 4; k++ {
			h = (h ^ (u >> (8 * k) & 0xff)) * 16777619
		}
	}
	return h
}

// readFrameLog reads a frame log written by celt_decode.
func readFrameLog(t testing.TB, path string) []frameLog {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var logs []frameLog
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for line := 1; sc.Scan(); line++ {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if len(f) != 9 {
			t.Fatalf("%s:%d: malformed line", path, line)
		}
		var l frameLog
		var ints [6]int
		for i, k := range []int{0, 1, 3, 4, 5, 6} {
			if ints[i], err = strconv.Atoi(f[k]); err != nil {
				t.Fatalf("%s:%d: %v", path, line, err)
			}
		}
		l.record, l.frame, l.bytes, l.samples, l.endBand, l.streamChannels = ints[0], ints[1], ints[2], ints[3], ints[4], ints[5]
		l.kind = f[2]
		rng, err := strconv.ParseUint(f[7], 16, 32)
		if err != nil {
			t.Fatalf("%s:%d: %v", path, line, err)
		}
		l.rng = uint32(rng)
		hash, err := strconv.ParseUint(f[8], 16, 32)
		if err != nil {
			t.Fatalf("%s:%d: %v", path, line, err)
		}
		l.hash = uint32(hash)
		logs = append(logs, l)
	}
	return logs
}

// celtStream is the Opus-level state that opus_decode_frame keeps for a
// CELT-only stream, around a CELT decoder.
type celtStream struct {
	dec                *Decoder
	fs                 int
	startBand          int // 0, or 17 to decode as the CELT layer of hybrid frames
	channels           int
	prevMode           bool
	endBand            int
	streamChannels     int
	packetFrameSize    int
	lastPacketDuration int
	record             int
	pcm                []float32 // decoded samples, interleaved
	logs               []frameLog
	// pitchPLC marks the logs of pitch-based concealment.
	pitchPLC []bool
	buf      []float32
}

func newCeltStream(t testing.TB, fs, channels int) *celtStream {
	t.Helper()
	d, err := NewDecoder(fs, channels)
	if err != nil {
		t.Fatal(err)
	}
	return &celtStream{
		dec:             d,
		fs:              fs,
		channels:        channels,
		endBand:         21,
		streamChannels:  channels,
		packetFrameSize: fs / 400,
		buf:             make([]float32, 960*channels),
	}
}

// endBandOf returns the CELT end band for the bandwidth of a CELT-only TOC
// (opus_decode_frame).
func endBandOf(toc byte) int {
	switch config := toc >> 3; {
	case config < 20:
		return 13 // narrowband
	case config < 24:
		return 17 // wideband
	case config < 28:
		return 19 // super-wideband
	default:
		return 21 // fullband
	}
}

// decodeFrame is opus_decode_frame for a CELT-only stream; it returns the
// samples per channel decoded.
func (s *celtStream) decodeFrame(frame int, data []byte, frameSize int) (int, error) {
	F20 := s.fs / 50
	F10 := F20 >> 1
	F5 := F10 >> 1
	var audiosize int
	if len(data) <= 1 {
		data = nil
		frameSize = min(frameSize, s.packetFrameSize)
	}
	if data != nil {
		audiosize = s.packetFrameSize
	} else {
		audiosize = frameSize
		if !s.prevMode {
			zeros := make([]float32, audiosize*s.channels)
			s.pcm = append(s.pcm, zeros...)
			s.logs = append(s.logs, frameLog{
				s.record, frame, "zeros", 0, audiosize, s.endBand, s.streamChannels, 0,
				pcmHash(zeros),
			})
			s.pitchPLC = append(s.pitchPLC, false)
			return audiosize, nil
		}
		if audiosize > F20 {
			done := 0
			for done < frameSize {
				n, err := s.decodeFrame(frame, nil, min(frameSize-done, F20))
				if err != nil {
					return 0, err
				}
				done += n
			}
			return frameSize, nil
		} else if audiosize < F20 {
			if audiosize > F10 {
				audiosize = F10
			} else if audiosize > F5 && audiosize < F10 {
				audiosize = F5
			}
		}
	}
	if data != nil {
		if err := s.dec.SetEndBand(s.endBand); err != nil {
			return 0, err
		}
	}
	if err := s.dec.SetStreamChannels(s.streamChannels); err != nil {
		return 0, err
	}
	if err := s.dec.SetStartBand(s.startBand); err != nil {
		return 0, err
	}
	// celt_decode_lost's choice between noise and pitch concealment.
	noiseBased := s.dec.lossDuration >= 40 || s.dec.start != 0 || s.dec.skipPLC
	pitch := data == nil && !noiseBased
	n, err := s.dec.DecodeWithEC(data, s.buf, min(audiosize, F20), nil)
	if err != nil {
		return 0, err
	}
	if n != audiosize {
		return 0, fmt.Errorf("decoded %d samples, want %d", n, audiosize)
	}
	s.prevMode = true
	s.pcm = append(s.pcm, s.buf[:n*s.channels]...)
	kind := "plc"
	if data != nil {
		kind = "frame"
	}
	s.logs = append(s.logs, frameLog{
		s.record, frame, kind, len(data), n, s.endBand, s.streamChannels, s.dec.FinalRange(),
		pcmHash(s.buf[:n*s.channels]),
	})
	s.pitchPLC = append(s.pitchPLC, pitch)
	return n, nil
}

// decodePacket is opus_decode_native for a CELT-only packet.
func (s *celtStream) decodePacket(p []byte) error {
	if p[0]>>3 < 16 {
		return errors.New("not a CELT-only packet")
	}
	frames, err := parseFrames(p)
	if err != nil {
		return err
	}
	s.endBand = endBandOf(p[0])
	s.packetFrameSize = (s.fs / 400) << (p[0] >> 3 & 3)
	s.streamChannels = 1 + int(p[0]>>2&1)
	for i, f := range frames {
		if _, err := s.decodeFrame(i, f, s.packetFrameSize); err != nil {
			return err
		}
	}
	s.lastPacketDuration = len(frames) * s.packetFrameSize
	return nil
}

// conceal is opus_decode_native with data == NULL for duration samples.
func (s *celtStream) conceal(duration int) error {
	if duration == 0 {
		duration = s.lastPacketDuration
	}
	if duration == 0 {
		duration = 960 * s.fs / 48000
	}
	for done := 0; done < duration; {
		n, err := s.decodeFrame(0, nil, duration-done)
		if err != nil {
			return err
		}
		done += n
	}
	s.lastPacketDuration = duration
	return nil
}

// decodeRecords decodes a packet list, concealing the empty records.
func (s *celtStream) decodeRecords(recs []record) error {
	for i, r := range recs {
		s.record = i
		var err error
		if len(r.packet) == 0 {
			err = s.conceal(int(r.rng))
		} else {
			err = s.decodePacket(r.packet)
		}
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
	}
	return nil
}

// readF32 reads little-endian float32 samples.
func readF32(t testing.TB, path string) []float32 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%4 != 0 {
		t.Fatalf("%s: size %d is not a multiple of 4", path, len(raw))
	}
	v := make([]float32, len(raw)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return v
}

// pcmError returns the RMS and peak difference of got and want.
func pcmError(got, want []float32) (rms, peak float64) {
	var sum float64
	for i := range got {
		d := math.Abs(float64(got[i]) - float64(want[i]))
		sum += d * d
		peak = max(peak, d)
	}
	if len(got) > 0 {
		rms = math.Sqrt(sum / float64(len(got)))
	}
	return rms, peak
}
