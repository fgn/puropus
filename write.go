package puropus

import (
	"fmt"
	"hash/fnv"
	"io"

	"github.com/fgn/puropus/internal/ogg"
	"github.com/fgn/puropus/internal/webm"
	"github.com/fgn/puropus/opus"
)

// vendor names the writer in OpusTags and as the WebM writing application.
const vendor = "puropus"

// streamLink is one link of a Stream as the writers lay it out.
type streamLink struct {
	header  Header
	packets []Packet
	leading int  // its leading packets: up to the first that contributes samples
	preSkip int  // their Skip
	trim    int  // the Trim of its last packet
	audible bool // a packet of it contributes samples
}

// links splits the stream into its links. With strict set it checks that
// each link carries Skip only on its leading packets and Trim only on its
// last, which is what an Ogg link can express: its OpusHead pre-skip and its
// final granule position. Otherwise preSkip and trim are those of the
// link's leading and last packets, whatever the others carry.
func (s *Stream) links(strict bool) ([]streamLink, error) {
	if len(s.Packets) == 0 {
		return nil, ErrEmptyStream
	}
	var links []streamLink
	for i, p := range s.Packets {
		if i == 0 || p.Link != s.Packets[i-1].Link {
			h := s.Header
			if p.Link < 0 || p.Link >= max(len(s.Links), 1) {
				return nil, fmt.Errorf("puropus: packet %d belongs to link %d of %d", i, p.Link, max(len(s.Links), 1))
			}
			if p.Link < len(s.Links) {
				h = s.Links[p.Link]
			}
			links = append(links, streamLink{header: h})
		}
		l := &links[len(links)-1]
		if strict && p.Skip > 0 && l.audible {
			return nil, fmt.Errorf("puropus: packet %d skips %d samples inside its link", i, p.Skip)
		}
		if strict && l.trim > 0 {
			return nil, fmt.Errorf("puropus: packet %d trims %d samples inside its link", i-1, l.trim)
		}
		if !l.audible {
			l.preSkip += p.Skip
			l.leading++
		}
		l.audible = l.audible || p.Duration > 0
		l.trim = p.Trim
		l.packets = append(l.packets, p)
	}
	return links, nil
}

// WriteOgg writes the stream as an Ogg Opus file, its links as the links of
// a chained stream. Each link has an OpusHead with its pre-skip and output
// gain, a minimal OpusTags, and audio pages of about one second, the last
// one's granule position trimming the link's end. The serial numbers are
// derived from the packet data, one per link, so identical streams produce
// identical files.
func (s *Stream) WriteOgg(w io.Writer) error {
	links, err := s.links(true)
	if err != nil {
		return err
	}
	used := map[uint32]bool{}
	for k, link := range links {
		serial := linkSerial(link.packets, k)
		for used[serial] {
			serial++ // RFC 3533: the links of a chain have distinct serials
		}
		used[serial] = true
		if err := writeOggLink(w, serial, s.head(link.header, link.preSkip), link); err != nil {
			return err
		}
	}
	return nil
}

// writeOggLink writes one link: OpusHead alone on the first page and OpusTags
// finishing its page before any audio (RFC 7845 §3).
func writeOggLink(w io.Writer, serial uint32, head opus.Head, link streamLink) error {
	ow := ogg.NewWriter(w, serial)
	if err := ow.WritePacket(head.Marshal(), 0, true); err != nil {
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
	for i, p := range link.packets {
		granule += int64(p.Coded())
		if i == len(link.packets)-1 {
			granule -= int64(link.trim)
		}
		if err := ow.WritePacket(p.Data, granule, false); err != nil {
			return err
		}
		if granule-pageStart >= SampleRate && i+1 < len(link.packets) {
			if err := ow.Flush(); err != nil {
				return err
			}
			pageStart = granule
		}
	}
	return ow.Close()
}

// WriteWebM writes the stream as a single-track WebM file in the profile
// browsers produce: the first link's pre-skip as CodecDelay and in the
// OpusHead, the total duration in the Segment Info, and every other Skip or
// Trim, wherever it is, as DiscardPadding on its block (negative for a
// Skip); a block can carry only one, so a packet with both after the
// pre-skip is an error. WebM has one header and no decoder reset, so the
// links must share their output gain, and the decoder then decodes a later
// link's first packets with the state the link before left.
func (s *Stream) WriteWebM(w io.Writer) error {
	links, err := s.links(false)
	if err != nil {
		return err
	}
	first := links[0]
	for k, link := range links[1:] {
		if link.header.OutputGain != first.header.OutputGain {
			return unsupported("WebM output of links with different output gains (%d and %d, link %d)", first.header.OutputGain, link.header.OutputGain, k+1)
		}
	}
	ww, err := webm.NewWriter(w, webm.WriterOptions{
		Channels:     s.Header.Channels,
		CodecPrivate: s.head(first.header, first.preSkip).Marshal(),
		CodecDelay:   samplesToDuration(int64(first.preSkip)),
		Duration:     s.Duration(),
		App:          vendor,
	})
	if err != nil {
		return err
	}
	for i, p := range s.Packets {
		skip := p.Skip
		if i < first.leading {
			skip = 0 // the first link's pre-skip, in CodecDelay
		}
		discard := p.Trim
		if skip > 0 {
			if p.Trim > 0 {
				return unsupported("WebM output of packet %d, which discards samples at both ends", i)
			}
			discard = -skip
		}
		if err := ww.WriteBlock(p.Data, p.Coded(), discard); err != nil {
			return err
		}
	}
	return ww.Close()
}

// head returns the OpusHead a link is written with.
func (s *Stream) head(h Header, preSkip int) opus.Head {
	return opus.Head{Version: 1, Channels: s.Header.Channels, PreSkip: preSkip, InputSampleRate: SampleRate, OutputGain: h.OutputGain}
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

// linkSerial derives the Ogg serial number of link k from its packet data.
func linkSerial(packets []Packet, k int) uint32 {
	h := fnv.New32a()
	for _, p := range packets {
		h.Write(p.Data)
	}
	return h.Sum32() + uint32(k)
}
