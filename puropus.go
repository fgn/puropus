// Package puropus decodes Opus audio from Ogg and WebM containers to PCM in
// pure Go, and edits those containers on the compressed-packet level: keeping
// intervals (as a voice activity detector would choose), extending them with
// context, and inserting synthesized silence, without decoding or re-encoding
// the audio that is kept.
//
// Decoding is streaming: a Decoder reads from an io.Reader as data arrives
// and hands out PCM through Read. The packets it has seen are available for
// editing with Cut, whose result can be written back as Ogg or WebM.
package puropus

import (
	"errors"
	"fmt"
	"time"
)

// Format identifies a container.
type Format int

// Containers this package reads and writes.
const (
	FormatUnknown Format = iota
	FormatOgg
	FormatWebM
)

// String returns the container name.
func (f Format) String() string {
	switch f {
	case FormatOgg:
		return "ogg"
	case FormatWebM:
		return "webm"
	}
	return "unknown"
}

// SampleRate is the Opus clock. Packet positions and durations are counted in
// samples at this rate.
const SampleRate = 48000

// Header describes a stream.
type Header struct {
	Format   Format
	Channels int
	// PreSkip is the number of 48 kHz samples the container drops from the
	// start of the decoded audio: the OpusHead pre-skip, or WebM CodecDelay
	// when there is no OpusHead. In the Links of a chained Ogg stream it is
	// each link's own. On a Stream it is the pre-skip of the output link,
	// the Skip of the link's leading packets.
	PreSkip int
	// OutputGain is the Q7.8 dB gain from the stream header, applied to
	// decoded PCM.
	OutputGain int16
}

// Packet is one Opus packet on the decoded timeline, in 48 kHz samples.
// Start and Duration are the samples the packet contributes to that
// timeline; Skip and Trim are the coded samples at its edges that the
// container discards: the stream's (or link's) pre-skip and a WebM block's
// negative DiscardPadding at the start, the end trimming of an Ogg final
// granule position or a positive DiscardPadding at the end. Packets tile the
// timeline, so each one starts where the previous one ends, and a packet
// that lies entirely inside the pre-skip contributes nothing.
type Packet struct {
	Data     []byte
	Start    int64
	Duration int
	Skip     int
	Trim     int
	// Link numbers the chained Ogg link the packet belongs to: it indexes
	// the Links of its Source or Stream, and is 0 for every packet of a
	// stream of one link. Where it changes, a new link starts, which a
	// decoder decodes afresh with that link's header.
	Link int
}

// End returns the sample position just past the packet's contribution.
func (p Packet) End() int64 { return p.Start + int64(p.Duration) }

// Coded returns the packet's duration as coded, Skip+Duration+Trim.
func (p Packet) Coded() int { return p.Skip + p.Duration + p.Trim }

// StartTime returns Start as a duration.
func (p Packet) StartTime() time.Duration { return samplesToDuration(p.Start) }

// Length returns the duration the packet contributes to the timeline.
func (p Packet) Length() time.Duration { return samplesToDuration(int64(p.Duration)) }

// Interval is a half-open time range on the decoded timeline.
type Interval struct {
	Start time.Duration
	End   time.Duration
}

// Source is what Cut operates on: a stream's header and packets, optionally
// with the decoded 48 kHz PCM for exact boundaries.
type Source struct {
	// Header is the stream's: its format and channels, and the pre-skip and
	// output gain of its first link.
	Header Header
	// Links holds the header of each link of a chained Ogg stream, in order
	// and from the first, for Packet.Link to index: Decoder.Source and
	// ReadPackets fill it for every stream, with one entry when there is
	// one link. A Source with no Links has one link, with Header.
	Links   []Header
	Packets []Packet
	// Native holds the decoded 48 kHz interleaved PCM of Packets before
	// output gain, starting at Packets[0].Start and covering every packet.
	// It is only required for SnapExact. It is nil unless the Decoder was
	// asked to keep it, and then not nil even when the packets decode to
	// nothing.
	Native []float32
}

// Duration returns the end of the decoded timeline covered by the packets.
func (s Source) Duration() time.Duration {
	if len(s.Packets) == 0 {
		return 0
	}
	return samplesToDuration(s.Packets[len(s.Packets)-1].End())
}

var (
	// ErrUnsupported reports a stream feature outside what this package
	// decodes, such as an unknown container or channel mapping family.
	ErrUnsupported = errors.New("puropus: unsupported stream")
	// ErrTruncated reports input that ended inside a container structure.
	// Decoders return everything decodable before it.
	ErrTruncated = errors.New("puropus: truncated input")
	// ErrEmptyStream reports an attempt to write a stream with no packets.
	ErrEmptyStream = errors.New("puropus: cannot write an empty stream")
	// ErrNativeRequired reports a Cut with SnapExact on a Source whose
	// Native PCM is missing or does not cover its packets.
	ErrNativeRequired = errors.New("puropus: exact cuts need Source.Native covering the packets (decode with KeepNative)")
	// ErrEncoderRequired reports a Cut with SnapExact but no Encoder.
	ErrEncoderRequired = errors.New("puropus: exact cuts need an Encoder")
)

// samplesToDuration converts a 48 kHz sample count to a duration, truncated
// to whole nanoseconds.
func samplesToDuration(samples int64) time.Duration {
	return samplesAtRateToDuration(samples, SampleRate)
}

// samplesAtRateToDuration converts a sample count at rate to a duration. It
// splits off whole seconds so that samples*time.Second cannot overflow on
// long streams.
func samplesAtRateToDuration(samples, rate int64) time.Duration {
	seconds := samples / rate
	remainder := samples % rate
	return time.Duration(seconds)*time.Second + time.Duration(remainder)*time.Second/time.Duration(rate)
}

// durationToSamples converts a duration to the nearest 48 kHz sample count,
// rounding half away from zero, so it inverts samplesToDuration.
func durationToSamples(d time.Duration) int64 {
	samples := int64(d/time.Second) * SampleRate
	n := (d % time.Second) * SampleRate
	if n >= 0 {
		return samples + int64((n+time.Second/2)/time.Second)
	}
	return samples - int64((-n+time.Second/2)/time.Second)
}

// unsupported returns an ErrUnsupported error naming the feature.
func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, args...))
}
