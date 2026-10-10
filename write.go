package puropus

import (
	"hash/fnv"
	"io"

	"github.com/fgn/puropus/internal/ogg"
	"github.com/fgn/puropus/internal/webm"
	"github.com/fgn/puropus/opus"
)

// vendor names the writer in OpusTags and as the WebM writing application.
const vendor = "puropus"

// WriteOgg writes the stream as an Ogg Opus file: OpusHead with the stream's
// pre-skip and output gain, a minimal OpusTags, and audio pages of about one
// second each, the last one's granule position trimming the end. The serial
// number is derived from the packet data so identical streams produce
// identical files.
func (s *Stream) WriteOgg(w io.Writer) error {
	preSkip, trim, err := s.checkWritable()
	if err != nil {
		return err
	}
	// RFC 7845 §3: OpusHead alone on the first page, and OpusTags finishing
	// its page before any audio.
	ow := ogg.NewWriter(w, s.serial())
	if err := ow.WritePacket(s.head(preSkip).Marshal(), 0, true); err != nil {
		return err
	}
	if err := ow.Flush(); err != nil {
		return err
	}
	if err := ow.WritePacket(opus.MarshalTags(vendor), 0, false); err != nil {
		return err
	}
	if err := ow.Flush(); err != nil {
		return err
	}
	granule := int64(0)
	pageStart := int64(0)
	for i, p := range s.Packets {
		granule += int64(p.Coded())
		if i == len(s.Packets)-1 {
			granule -= int64(trim)
		}
		if err := ow.WritePacket(p.Data, granule, false); err != nil {
			return err
		}
		if granule-pageStart >= SampleRate && i+1 < len(s.Packets) {
			if err := ow.Flush(); err != nil {
				return err
			}
			pageStart = granule
		}
	}
	return ow.Close()
}

// WriteWebM writes the stream as a single-track WebM file in the profile
// browsers produce: the pre-skip as CodecDelay and in the OpusHead, the end
// trimming as DiscardPadding on the last block, and the total duration in
// the Segment Info.
func (s *Stream) WriteWebM(w io.Writer) error {
	preSkip, trim, err := s.checkWritable()
	if err != nil {
		return err
	}
	ww, err := webm.NewWriter(w, webm.WriterOptions{
		Channels:     s.Header.Channels,
		CodecPrivate: s.head(preSkip).Marshal(),
		CodecDelay:   samplesToDuration(int64(preSkip)),
		Duration:     s.Duration(),
		App:          vendor,
	})
	if err != nil {
		return err
	}
	for i, p := range s.Packets {
		discard := 0
		if i == len(s.Packets)-1 {
			discard = trim
		}
		if err := ww.WriteBlock(p.Data, p.Coded(), discard); err != nil {
			return err
		}
	}
	return ww.Close()
}

// head returns the OpusHead the stream is written with.
func (s *Stream) head(preSkip int) opus.Head {
	return opus.Head{Version: 1, Channels: s.Header.Channels, PreSkip: preSkip, InputSampleRate: SampleRate, OutputGain: s.Header.OutputGain}
}

// checkWritable returns the pre-skip and end trimming to write, or why the
// stream cannot be written.
func (s *Stream) checkWritable() (preSkip, trim int, err error) {
	if len(s.Packets) == 0 {
		return 0, 0, ErrEmptyStream
	}
	return s.edges()
}

// Write writes the stream in the given container format.
func (s *Stream) Write(w io.Writer, format Format) error {
	if len(s.Packets) == 0 {
		return ErrEmptyStream
	}
	switch format {
	case FormatOgg:
		return s.WriteOgg(w)
	case FormatWebM:
		return s.WriteWebM(w)
	}
	return unsupported("output format %v", format)
}

// serial derives the Ogg serial number from the packet data.
func (s *Stream) serial() uint32 {
	h := fnv.New32a()
	for _, p := range s.Packets {
		h.Write(p.Data)
	}
	return h.Sum32()
}
