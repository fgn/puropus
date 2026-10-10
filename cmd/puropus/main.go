// Command puropus inspects, decodes, and cuts Ogg Opus and WebM/Opus files.
//
//	puropus inspect [-packets] input.webm
//	puropus decode [-rate 16000] [-tolerant] -o out.f32le input.opus
//	puropus cut -keep intervals.txt [-extend 40ms] [-silence 120ms] [-inward] -o out.webm input.webm
//
// Intervals are "start end" seconds per line; # starts a comment.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fgn/puropus"
	"github.com/fgn/puropus/opus"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "inspect":
		err = inspect(os.Args[2:])
	case "decode":
		err = decode(os.Args[2:])
	case "cut":
		err = cut(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, formatError(err))
		var usageErr *usageError
		if errors.As(err, &usageErr) {
			usage()
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// usageError reports a malformed command line; main follows it with the
// usage text and exit status 2.
type usageError struct {
	message string
}

func (e *usageError) Error() string { return e.message }

// formatError prefixes the message with the command name unless the library
// already did.
func formatError(err error) string {
	message := err.Error()
	if strings.HasPrefix(message, "puropus: ") {
		return message
	}
	return "puropus: " + message
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  puropus inspect [-packets] INPUT
  puropus decode [-rate 48000|16000] [-tolerant] [-o OUTPUT.f32le] INPUT
  puropus cut -keep INTERVALS [-extend DUR] [-before DUR] [-after DUR]
              [-silence DUR] [-leading DUR] [-trailing DUR] [-inward]
              [-format ogg|webm] -o OUTPUT INPUT
INPUT may be - for stdin. Decoded output is interleaved little-endian float32.
`)
}

func open(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

func inspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	packets := fs.Bool("packets", false, "list every packet")
	if err := fs.Parse(args); err != nil {
		return err
	}
	input := fs.Arg(0)
	if input == "" {
		return &usageError{message: "inspect needs INPUT"}
	}
	in, err := open(input)
	if err != nil {
		return err
	}
	defer closeQuietly(in)
	src, err := puropus.ReadPackets(bufio.NewReader(in))
	if err != nil && len(src.Packets) == 0 {
		return err
	}
	modes := map[opus.Mode]int{}
	bandwidths := map[opus.Bandwidth]int{}
	durations := map[int]int{}
	bytes := 0
	for _, p := range src.Packets {
		parsed, perr := opus.Parse(p.Data, false)
		if perr == nil {
			modes[parsed.Mode]++
			bandwidths[parsed.Bandwidth]++
		}
		durations[p.Coded()]++
		bytes += len(p.Data)
	}
	duration := src.Duration()
	fmt.Printf("format: %v\nchannels: %d\npre-skip: %d samples\noutput gain: %d (Q7.8 dB)\npackets: %d\nduration: %v\nbitrate: %s\n",
		src.Header.Format, src.Header.Channels, src.Header.PreSkip, src.Header.OutputGain, len(src.Packets), duration,
		formatBitrate(bytes, duration))
	fmt.Printf("modes: silk=%d hybrid=%d celt=%d\n", modes[opus.ModeSILK], modes[opus.ModeHybrid], modes[opus.ModeCELT])
	fmt.Printf("bandwidths: nb=%d mb=%d wb=%d swb=%d fb=%d\n", bandwidths[opus.BandwidthNarrowband], bandwidths[opus.BandwidthMediumband],
		bandwidths[opus.BandwidthWideband], bandwidths[opus.BandwidthSuperWideband], bandwidths[opus.BandwidthFullband])
	fmt.Printf("packet durations:%s\n", formatPacketDurations(durations))
	if *packets {
		for i, p := range src.Packets {
			fmt.Printf("%6d %10v %6.1fms %5d bytes%s\n", i, p.StartTime(), float64(p.Coded())/48, len(p.Data), formatEdges(p))
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	return nil
}

func decode(args []string) (err error) {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	rate := fs.Int("rate", 48000, "output sample rate: 48000 or 16000")
	tolerant := fs.Bool("tolerant", false, "recover undecodable packets from the next packet's in-band FEC, or conceal them, instead of failing")
	output := fs.String("o", "-", "output file (f32le), - for stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	input := fs.Arg(0)
	if input == "" {
		return &usageError{message: "decode needs INPUT"}
	}
	if *rate != 48000 && *rate != 16000 {
		return fmt.Errorf("-rate must be 48000 or 16000")
	}
	in, err := open(input)
	if err != nil {
		return err
	}
	defer closeQuietly(in)
	out := os.Stdout
	if *output != "-" {
		f, cerr := os.Create(*output)
		if cerr != nil {
			return cerr
		}
		defer func() {
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}()
		out = f
	}
	dec, err := puropus.NewDecoder(in, puropus.DecodeOptions{SampleRate: *rate, Tolerant: *tolerant})
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 1<<16)
	pcm := make([]float32, 4096)
	raw := make([]byte, len(pcm)*4)
	for {
		n, rerr := dec.Read(pcm)
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(pcm[i]))
		}
		if _, err := w.Write(raw[:n*4]); err != nil {
			return err
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_ = w.Flush()
			return rerr
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "decoded %v at %d Hz\n", dec.Position(), dec.SampleRate())
	return nil
}

func cut(args []string) error {
	fs := flag.NewFlagSet("cut", flag.ExitOnError)
	keepPath := fs.String("keep", "", "intervals file: start end seconds per line")
	extend := fs.Duration("extend", 0, "extend every interval by this much on both sides")
	before := fs.Duration("before", 0, "extend every interval by this much before")
	after := fs.Duration("after", 0, "extend every interval by this much after")
	between := fs.Duration("silence", 0, "silence inserted between kept regions")
	leading := fs.Duration("leading", 0, "silence inserted before the first region")
	trailing := fs.Duration("trailing", 0, "silence inserted after the last region")
	inward := fs.Bool("inward", false, "keep only packets fully inside an interval")
	format := fs.String("format", "", "output container: ogg or webm (default: from the output name, else the input format)")
	output := fs.String("o", "", "output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	input := fs.Arg(0)
	if input == "" {
		return &usageError{message: "cut needs INPUT"}
	}
	if *keepPath == "" || *output == "" {
		return fmt.Errorf("cut needs -keep and -o")
	}
	intervals, err := readIntervals(*keepPath)
	if err != nil {
		return err
	}
	in, err := open(input)
	if err != nil {
		return err
	}
	defer closeQuietly(in)
	src, err := puropus.ReadPackets(bufio.NewReader(in))
	if err != nil {
		return err
	}
	edit := puropus.Edit{
		Keep:    intervals,
		Extend:  puropus.Margin{Before: *extend + *before, After: *extend + *after},
		Silence: puropus.Silence{Leading: *leading, Between: *between, Trailing: *trailing},
	}
	if *inward {
		edit.Snap = puropus.SnapInward
	}
	stream, err := puropus.Cut(src, edit)
	if err != nil {
		return err
	}
	// Fail before creating the output file.
	if len(stream.Packets) == 0 {
		return puropus.ErrEmptyStream
	}
	target := src.Header.Format
	switch strings.ToLower(*format) {
	case "ogg", "opus":
		target = puropus.FormatOgg
	case "webm":
		target = puropus.FormatWebM
	case "":
		switch strings.ToLower(filepath.Ext(*output)) {
		case ".ogg", ".opus", ".oga":
			target = puropus.FormatOgg
		case ".webm", ".mkv", ".mka":
			target = puropus.FormatWebM
		}
	default:
		return fmt.Errorf("unknown format %q", *format)
	}
	f, err := os.Create(*output)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := stream.Write(w, target); err != nil {
		closeQuietly(f)
		return err
	}
	if err := w.Flush(); err != nil {
		closeQuietly(f)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	synthetic := 0
	for _, s := range stream.Spans {
		if s.Synthetic {
			synthetic++
		}
	}
	fmt.Fprintf(os.Stderr, "wrote %d packets, %v (%d spans, %d silence spans) to %s\n",
		len(stream.Packets), stream.Duration(), len(stream.Spans)-synthetic, synthetic, *output)
	return nil
}

// formatEdges notes the samples of a packet that the container discards.
func formatEdges(p puropus.Packet) string {
	var notes []string
	if p.Skip > 0 {
		notes = append(notes, fmt.Sprintf("skip %d", p.Skip))
	}
	if p.Trim > 0 {
		notes = append(notes, fmt.Sprintf("trim %d", p.Trim))
	}
	if len(notes) == 0 {
		return ""
	}
	return "  (" + strings.Join(notes, ", ") + ")"
}

func readIntervals(path string) ([]puropus.Interval, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(f)
	var out []puropus.Interval
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: expected START END seconds", path, line)
		}
		start, err1 := strconv.ParseFloat(fields[0], 64)
		end, err2 := strconv.ParseFloat(fields[1], 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s:%d: expected START END seconds", path, line)
		}
		if math.IsInf(start, 0) || math.IsNaN(start) || math.IsInf(end, 0) || math.IsNaN(end) {
			return nil, fmt.Errorf("%s:%d: START and END must be finite", path, line)
		}
		if end <= start {
			return nil, fmt.Errorf("%s:%d: END must be greater than START", path, line)
		}
		out = append(out, puropus.Interval{Start: seconds(start), End: seconds(end)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no intervals", path)
	}
	return out, nil
}

func seconds(v float64) time.Duration {
	return time.Duration(math.Round(v * float64(time.Second)))
}

func formatBitrate(bytes int, duration time.Duration) string {
	if duration <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f kb/s", float64(bytes)*8/duration.Seconds()/1000)
}

func formatPacketDurations(counts map[int]int) string {
	durations := make([]int, 0, len(counts))
	for duration := range counts {
		durations = append(durations, duration)
	}
	sort.Ints(durations)

	var result strings.Builder
	for _, duration := range durations {
		fmt.Fprintf(&result, " %gms×%d", float64(duration)/48, counts[duration])
	}
	return result.String()
}

// closeQuietly closes read-side files whose close errors carry no information.
func closeQuietly(c io.Closer) { _ = c.Close() }
