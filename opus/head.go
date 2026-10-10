package opus

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Head is the identification header shared by Ogg Opus ("OpusHead" packet,
// RFC 7845) and Matroska/WebM (CodecPrivate).
type Head struct {
	Version         int    // encapsulation version; RFC 7845 defines 1
	Channels        int    // output channel count
	PreSkip         int    // samples at 48 kHz to discard from the start of the decoded stream
	InputSampleRate uint32 // informational original sample rate
	OutputGain      int16  // Q7.8 dB gain to apply to the decoded output
	MappingFamily   int    // channel mapping family; only 0 is supported
}

// ErrNotOpusHead reports data that does not start with the OpusHead magic.
var ErrNotOpusHead = errors.New("opus: not an OpusHead header")

// ParseHead decodes an OpusHead header. Only channel mapping family 0 (mono
// and stereo) is accepted.
func ParseHead(data []byte) (Head, error) {
	if len(data) < 8 || string(data[:8]) != "OpusHead" {
		return Head{}, ErrNotOpusHead
	}
	if len(data) < 19 {
		return Head{}, fmt.Errorf("opus: OpusHead is %d bytes, need at least 19", len(data))
	}
	head := Head{
		Version:         int(data[8]),
		Channels:        int(data[9]),
		PreSkip:         int(binary.LittleEndian.Uint16(data[10:12])),
		InputSampleRate: binary.LittleEndian.Uint32(data[12:16]),
		OutputGain:      int16(binary.LittleEndian.Uint16(data[16:18])),
		MappingFamily:   int(data[18]),
	}
	if head.Version>>4 != 0 {
		return Head{}, fmt.Errorf("opus: unsupported OpusHead version %d", head.Version)
	}
	if head.MappingFamily != 0 {
		return Head{}, fmt.Errorf("opus: unsupported channel mapping family %d", head.MappingFamily)
	}
	if head.Channels < 1 || head.Channels > 2 {
		return Head{}, fmt.Errorf("opus: mapping family 0 requires 1 or 2 channels, got %d", head.Channels)
	}
	return head, nil
}

// MaxPreSkip is the largest pre-skip an OpusHead can carry in its 16 bits.
const MaxPreSkip = 0xFFFF

// Marshal encodes the header as a 19-byte OpusHead packet, writing a zero
// Version as 1. The fields must fit their OpusHead widths, PreSkip up to
// MaxPreSkip and Version and Channels up to 255: Marshal keeps only their low
// bits, so a writer checks them first.
func (h Head) Marshal() []byte {
	out := make([]byte, 19)
	copy(out, "OpusHead")
	out[8] = byte(h.Version)
	if h.Version == 0 {
		out[8] = 1
	}
	out[9] = byte(h.Channels)
	binary.LittleEndian.PutUint16(out[10:], uint16(h.PreSkip))
	binary.LittleEndian.PutUint32(out[12:], h.InputSampleRate)
	binary.LittleEndian.PutUint16(out[16:], uint16(h.OutputGain))
	out[18] = byte(h.MappingFamily)
	return out
}

// IsTags reports whether data is an OpusTags comment header.
func IsTags(data []byte) bool {
	return len(data) >= 8 && string(data[:8]) == "OpusTags"
}

// MarshalTags encodes a minimal OpusTags packet with the given vendor string
// and no user comments.
func MarshalTags(vendor string) []byte {
	out := make([]byte, 0, 16+len(vendor))
	out = append(out, "OpusTags"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(vendor)))
	out = append(out, vendor...)
	out = binary.LittleEndian.AppendUint32(out, 0)
	return out
}
