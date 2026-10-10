//go:build ignore

// Command packets extracts the Opus packets of an Ogg Opus or WebM file into
// the opus_demo packet list that tools/libopus-ref/decode_float reads, and
// prints the decode_float options that make its output what the puropus
// Decoder returns for the file at 48 kHz: the channel count, the pre-skip and
// the end trimming.
//
//	go run tools/packets/main.go -o packets.bit testdata/ogg-celt-fb.opus
//	tools/libopus-ref/build/decode_float $(that output) packets.bit out.f32le
//
// Each record is a 4-byte big-endian payload length, a 4-byte big-endian
// final range (0: unknown) and the packet. The pre-skip comes from OpusHead
// (or WebM CodecDelay without one); the end trimming from the final granule
// position of an Ogg stream (RFC 7845 §4.4) or the DiscardPadding of the last
// WebM block, computed as the root package does. Chained Ogg links, trimming
// before the last packet and unparseable packets are rejected.
//
// It lives behind the ignore build tag so that go build ./... and go vet ./...
// skip it.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fgn/puropus/internal/ogg"
	"github.com/fgn/puropus/internal/webm"
	"github.com/fgn/puropus/opus"
)

// stream is the content of a file: its packets and how much of their decoded
// audio the root package's Decoder keeps.
type stream struct {
	channels int
	preSkip  int
	trim     int // samples dropped from the end of the last packet
	packets  [][]byte
}

func main() {
	out := flag.String("o", "", "write the packet list to this file (default: stdout)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: go run tools/packets/main.go [-o packets.bit] input.opus|input.webm")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *out); err != nil {
		fmt.Fprintln(os.Stderr, "packets:", err)
		os.Exit(1)
	}
}

func run(input, output string) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var s stream
	switch {
	case bytes.HasPrefix(data, []byte("OggS")):
		s, err = readOgg(data)
	case bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		s, err = readWebM(data)
	default:
		err = errors.New("not an Ogg or WebM file")
	}
	if err != nil {
		return fmt.Errorf("%s: %w", input, err)
	}
	total := 0
	for i, p := range s.packets {
		parsed, err := opus.Parse(p, false)
		if err != nil {
			return fmt.Errorf("%s: packet %d: %w", input, i, err)
		}
		total += parsed.Duration48()
	}
	if s.preSkip+s.trim > total {
		return fmt.Errorf("%s: pre-skip %d and trimming %d exceed the %d coded samples", input, s.preSkip, s.trim, total)
	}
	w := io.Writer(os.Stdout)
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	for _, p := range s.packets {
		var head [8]byte
		binary.BigEndian.PutUint32(head[:], uint32(len(p)))
		bw.Write(head[:])
		bw.Write(p)
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if output != "" {
		fmt.Printf("-c %d -skip %d -trim %d\n", s.channels, s.preSkip, s.trim)
	}
	fmt.Fprintf(os.Stderr, "%s: %d packets, %d coded samples, pre-skip %d, trimming %d, %d channel(s)\n",
		input, len(s.packets), total, s.preSkip, s.trim, s.channels)
	return nil
}

func readOgg(data []byte) (stream, error) {
	r := ogg.NewReader(bytes.NewReader(data))
	r.SetBOSSelector(func(p []byte) bool { return bytes.HasPrefix(p, []byte("OpusHead")) })
	first, err := r.Next()
	if err != nil {
		return stream{}, err
	}
	head, err := opus.ParseHead(first.Data)
	if err != nil {
		return stream{}, err
	}
	tags, err := r.Next()
	if err != nil {
		return stream{}, err
	}
	if !opus.IsTags(tags.Data) {
		return stream{}, errors.New("second packet is not OpusTags")
	}
	s := stream{channels: head.Channels, preSkip: head.PreSkip}
	// pageDuration counts the samples since the last page with a granule
	// position, previous is that position.
	var pageDuration, previous int64
	haveGranule := false
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stream{}, err
		}
		if p.BOS && bytes.HasPrefix(p.Data, []byte("OpusHead")) {
			return stream{}, errors.New("chained Ogg links are not supported")
		}
		if opus.IsTags(p.Data) {
			continue
		}
		if s.trim > 0 {
			return stream{}, errors.New("end trimming before the last packet")
		}
		parsed, err := opus.Parse(p.Data, false)
		if err != nil {
			return stream{}, fmt.Errorf("packet %d: %w", len(s.packets), err)
		}
		duration := int64(parsed.Duration48())
		pageDuration += duration
		if p.GranuleKnown && p.Granule >= 0 {
			if p.EOS {
				// The stream ends where the previous page's granule plus
				// this page's packets land; anything beyond the final
				// granule is padding to drop (RFC 7845 §4.4).
				expected := pageDuration
				if haveGranule {
					expected += previous
				}
				if expected > p.Granule {
					s.trim = int(min(expected-p.Granule, duration))
				}
			}
			previous = p.Granule
			haveGranule = true
			pageDuration = 0
		}
		s.packets = append(s.packets, p.Data)
	}
	return s, nil
}

func readWebM(data []byte) (stream, error) {
	r, err := webm.NewReader(bytes.NewReader(data))
	if err != nil {
		return stream{}, err
	}
	track := r.Track()
	s := stream{channels: track.Channels, preSkip: int(durationToSamples(track.CodecDelay))}
	if len(track.CodecPrivate) >= 19 {
		head, err := opus.ParseHead(track.CodecPrivate)
		if err != nil {
			return stream{}, err
		}
		s.channels = head.Channels
		s.preSkip = head.PreSkip
	}
	for {
		b, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stream{}, err
		}
		if s.trim > 0 {
			return stream{}, errors.New("end trimming before the last packet")
		}
		parsed, err := opus.Parse(b.Data, false)
		if err != nil {
			return stream{}, fmt.Errorf("packet %d: %w", len(s.packets), err)
		}
		if b.DiscardPadding > 0 {
			s.trim = int(min(durationToSamples(b.DiscardPadding), int64(parsed.Duration48())))
		}
		s.packets = append(s.packets, b.Data)
	}
	return s, nil
}

// durationToSamples converts a duration to the nearest 48 kHz sample count,
// rounding half away from zero, as the root package does.
func durationToSamples(d time.Duration) int64 {
	samples := int64(d/time.Second) * 48000
	n := (d % time.Second) * 48000
	if n >= 0 {
		return samples + int64((n+time.Second/2)/time.Second)
	}
	return samples - int64((-n+time.Second/2)/time.Second)
}
