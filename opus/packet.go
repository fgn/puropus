// Copyright (c) 2011 Xiph.Org Foundation, Skype Limited (src/opus.c)
// Copyright (c) 2010 Xiph.Org Foundation, Skype Limited (src/opus_decoder.c)
// Written by Jean-Marc Valin and Koen Vos
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
//
// - Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//
// - Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER
// OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
// PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
// NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
// Ported from libopus 1.5.2 src/opus.c (opus_packet_parse_impl, parse_size,
// opus_packet_get_samples_per_frame) and src/opus_decoder.c
// (opus_packet_get_mode, opus_packet_get_bandwidth).

package opus

import "errors"

// Packet layout limits from RFC 6716.
const (
	MaxFrameSize        = 1275 // bytes in one frame (§3.2.1)
	MaxFrames           = 48   // frames in one packet (§3.2.5)
	MaxPacketDuration48 = 5760 // samples per channel in one packet, 120 ms (§3.2.5)
)

// Mode is the coding mode of a packet.
type Mode int

// Coding modes.
const (
	ModeSILK Mode = iota
	ModeHybrid
	ModeCELT
)

// Bandwidth is the audio bandwidth signalled by a packet.
type Bandwidth int

// Audio bandwidths, narrowest first.
const (
	BandwidthNarrowband Bandwidth = iota
	BandwidthMediumband
	BandwidthWideband
	BandwidthSuperWideband
	BandwidthFullband
)

// Packet is the parsed table of contents of an Opus packet: its mode,
// bandwidth, frame layout, and where each frame lies in the data.
type Packet struct {
	Code          int  // the TOC byte's low two bits, which select one of four framings (RFC 6716 §3.1)
	Stereo        bool // the TOC's stereo flag
	Config        int  // configuration number 0 to 31, the high five bits of the TOC byte
	FrameCount    int  // number of frames, 1 to MaxFrames
	VBR           bool // each frame carries its own length: always in code 2, in code 3 when its v bit is set
	FrameDuration int  // samples per channel of each frame at 48 kHz
	// FrameOffset and FrameSize locate frame i at
	// data[FrameOffset[i]:FrameOffset[i]+FrameSize[i]]. A frame of 0 or 1
	// bytes is a lost or DTX frame for the decoder.
	FrameOffset [MaxFrames]int
	FrameSize   [MaxFrames]int
	// PacketSize counts every byte of the packet, its padding too: len(data),
	// except with self-delimited framing, where the next packet starts at
	// data[PacketSize].
	PacketSize int
	// DataSize is the total size of the frames in bytes: the packet without
	// its TOC byte, frame count byte, frame lengths and padding.
	DataSize  int
	Mode      Mode
	Bandwidth Bandwidth
}

// Duration48 returns the packet duration in samples at 48 kHz.
func (p Packet) Duration48() int {
	return p.FrameCount * p.FrameDuration
}

// Reasons Parse rejects a packet (OPUS_INVALID_PACKET in libopus).
var (
	errEmptyPacket     = errors.New("opus: invalid packet: empty")
	errOddCBR          = errors.New("opus: invalid packet: code 1 payload of odd length")
	errFrameLength     = errors.New("opus: invalid packet: frame length missing or beyond the packet")
	errFrameCount      = errors.New("opus: invalid packet: code 3 frame count missing, zero or above 120 ms")
	errPadding         = errors.New("opus: invalid packet: padding beyond the packet")
	errCBRSize         = errors.New("opus: invalid packet: code 3 CBR payload not a multiple of the frame count")
	errFrameTooLong    = errors.New("opus: invalid packet: frame longer than 1275 bytes")
	errSelfDelimLength = errors.New("opus: invalid packet: self-delimited length beyond the packet")
)

// Parse reads the table of contents of an Opus packet (RFC 6716 section 3)
// without decoding it. selfDelimited selects the self-delimiting framing used
// inside multistream packets, which Appendix B of the RFC defines. It rejects what
// libopus's opus_packet_parse_impl rejects, which are the packets that break
// the requirements of RFC 6716 §3.4.
func Parse(buf []byte, selfDelimited bool) (p Packet, err error) {
	if len(buf) == 0 {
		return Packet{}, errEmptyPacket
	}
	toc := buf[0]
	p.Code = int(toc & 0x3)
	p.Stereo = toc&0x4 != 0
	p.Config = int(toc >> 3)
	p.Mode = tocMode(toc)
	p.Bandwidth = tocBandwidth(toc)
	p.FrameDuration = tocSamplesPerFrame(toc)

	// pos is the offset of the next header byte or frame, n the number of
	// bytes left for frames (padding excluded), lastSize the size of the last
	// frame (of every frame for CBR) when it is not coded explicitly.
	pos, n := 1, len(buf)-1
	lastSize := n
	cbr := false
	pad := 0
	switch p.Code {
	case 0: // one frame
		p.FrameCount = 1
	case 1: // two frames of equal size
		p.FrameCount = 2
		cbr = true
		if !selfDelimited {
			if n&1 != 0 {
				return Packet{}, errOddCBR
			}
			lastSize = n / 2
			p.FrameSize[0] = lastSize
		}
	case 2: // two frames, the first one's size coded
		p.FrameCount = 2
		size, bytes := parseSize(buf[pos : pos+n])
		n -= bytes
		if bytes < 0 || size > n {
			return Packet{}, errFrameLength
		}
		pos += bytes
		p.FrameSize[0] = size
		lastSize = n - size
		p.VBR = true
	default: // code 3: a frame count byte, padding, CBR or VBR frames
		if n < 1 {
			return Packet{}, errFrameCount
		}
		ch := buf[pos]
		pos++
		n--
		p.FrameCount = int(ch & 0x3f)
		if p.FrameCount <= 0 || p.FrameDuration*p.FrameCount > MaxPacketDuration48 {
			return Packet{}, errFrameCount
		}
		if ch&0x40 != 0 { // padding: length bytes of 255 continue
			for {
				if n <= 0 {
					return Packet{}, errPadding
				}
				b := int(buf[pos])
				pos++
				n--
				tmp := b
				if b == 255 {
					tmp = 254
				}
				n -= tmp
				pad += tmp
				if b != 255 {
					break
				}
			}
		}
		if n < 0 {
			return Packet{}, errPadding
		}
		cbr = ch&0x80 == 0
		p.VBR = !cbr
		switch {
		case p.VBR:
			lastSize = n
			for i := 0; i < p.FrameCount-1; i++ {
				size, bytes := parseSize(buf[pos : pos+n])
				n -= bytes
				if bytes < 0 || size > n {
					return Packet{}, errFrameLength
				}
				pos += bytes
				p.FrameSize[i] = size
				lastSize -= bytes + size
			}
			if lastSize < 0 {
				return Packet{}, errFrameLength
			}
		case !selfDelimited:
			lastSize = n / p.FrameCount
			if lastSize*p.FrameCount != n {
				return Packet{}, errCBRSize
			}
			for i := 0; i < p.FrameCount-1; i++ {
				p.FrameSize[i] = lastSize
			}
		}
	}
	last := p.FrameCount - 1
	if selfDelimited {
		// Self-delimited framing codes the size of the last frame too, and
		// for CBR it applies to every frame.
		size, bytes := parseSize(buf[pos : pos+n])
		n -= bytes
		if bytes < 0 || size > n {
			return Packet{}, errSelfDelimLength
		}
		pos += bytes
		p.FrameSize[last] = size
		if cbr {
			if size*p.FrameCount > n {
				return Packet{}, errSelfDelimLength
			}
			for i := 0; i < last; i++ {
				p.FrameSize[i] = size
			}
		} else if bytes+size > lastSize {
			return Packet{}, errSelfDelimLength
		}
	} else {
		// The last frame's size (every frame's, for CBR) is implicit, so it
		// may exceed 1275 bytes; reject that here.
		if lastSize > MaxFrameSize {
			return Packet{}, errFrameTooLong
		}
		p.FrameSize[last] = lastSize
	}
	for i := 0; i < p.FrameCount; i++ {
		p.FrameOffset[i] = pos
		pos += p.FrameSize[i]
		p.DataSize += p.FrameSize[i]
	}
	p.PacketSize = pad + pos
	return p, nil
}

// parseSize decodes a length in the one- or two-byte form of RFC 6716
// §3.2.1, returning the length and how many bytes it took, or -1, -1 when
// data is too short (parse_size).
func parseSize(data []byte) (size, bytes int) {
	switch {
	case len(data) < 1:
		return -1, -1
	case data[0] < 252:
		return int(data[0]), 1
	case len(data) < 2:
		return -1, -1
	default:
		return 4*int(data[1]) + int(data[0]), 2
	}
}

// tocMode returns the coding mode of a TOC byte (opus_packet_get_mode).
func tocMode(toc byte) Mode {
	switch {
	case toc&0x80 != 0:
		return ModeCELT
	case toc&0x60 == 0x60:
		return ModeHybrid
	default:
		return ModeSILK
	}
}

// tocBandwidth returns the audio bandwidth of a TOC byte
// (opus_packet_get_bandwidth). CELT has no medium band, so its
// configurations 16 to 31 step from narrowband straight to wideband.
func tocBandwidth(toc byte) Bandwidth {
	switch {
	case toc&0x80 != 0:
		bw := BandwidthMediumband + Bandwidth((toc>>5)&0x3)
		if bw == BandwidthMediumband {
			bw = BandwidthNarrowband
		}
		return bw
	case toc&0x60 == 0x60:
		if toc&0x10 != 0 {
			return BandwidthFullband
		}
		return BandwidthSuperWideband
	default:
		return BandwidthNarrowband + Bandwidth((toc>>5)&0x3)
	}
}

// tocSamplesPerFrame returns the frame duration of a TOC byte in samples at
// 48 kHz (opus_packet_get_samples_per_frame with Fs = 48000).
func tocSamplesPerFrame(toc byte) int {
	const fs = 48000
	switch {
	case toc&0x80 != 0: // CELT: 2.5, 5, 10 or 20 ms
		return (fs << ((toc >> 3) & 0x3)) / 400
	case toc&0x60 == 0x60: // hybrid: 10 or 20 ms
		if toc&0x08 != 0 {
			return fs / 50
		}
		return fs / 100
	default: // SILK: 10, 20, 40 or 60 ms
		size := (toc >> 3) & 0x3
		if size == 3 {
			return fs * 60 / 1000
		}
		return (fs << size) / 100
	}
}
