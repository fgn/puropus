package opus

import (
	"fmt"
	"time"
)

// Silence returns Opus packets that decode to digital silence for the given
// duration, which must be a multiple of 2.5 ms: packets of up to six 20 ms
// frames, then single 10, 5 and 2.5 ms frames for the remainder. No encoder
// is needed: each frame is a CELT frame whose only coded symbol is the
// silence flag, which decoders render as zero coefficients, so the output is
// digital silence once the previous frame's overlap and de-emphasis tail has
// decayed (a few milliseconds): exactly zero as 16-bit PCM, and below 1e-33
// as float, where libopus's float decoder, which Decoder follows, keeps a
// constant offset of that size in its de-emphasis filter to stay clear of
// denormal numbers. bandwidth selects the CELT configuration so
// the frames match the surrounding stream; BandwidthMediumband maps to
// wideband because CELT has no MB mode.
func Silence(channels int, bandwidth Bandwidth, duration time.Duration) ([][]byte, error) {
	if channels < 1 || channels > 2 {
		return nil, fmt.Errorf("opus: unsupported channel count %d", channels)
	}
	if duration < 0 || duration%(2500*time.Microsecond) != 0 {
		return nil, fmt.Errorf("opus: silence duration %v is not a multiple of 2.5 ms", duration)
	}
	base := 28 // fullband CELT-only configs 28..31
	switch bandwidth {
	case BandwidthNarrowband:
		base = 16
	case BandwidthMediumband, BandwidthWideband:
		base = 20
	case BandwidthSuperWideband:
		base = 24
	}
	stereo := byte(0)
	if channels == 2 {
		stereo = 0x04
	}
	units := int(duration / (2500 * time.Microsecond))
	var packets [][]byte
	for units >= 8 {
		frames := min(units/8, 6) // up to six 20 ms frames per packet
		toc := byte(base+3)<<3 | stereo
		if frames == 1 {
			packets = append(packets, append([]byte{toc}, silenceFrame...))
		} else {
			packet := make([]byte, 0, 2+frames*len(silenceFrame))
			packet = append(packet, toc|3, byte(frames))
			for i := 0; i < frames; i++ {
				packet = append(packet, silenceFrame...)
			}
			packets = append(packets, packet)
		}
		units -= frames * 8
	}
	for _, step := range []struct{ units, size int }{{4, 2}, {2, 1}, {1, 0}} {
		if units >= step.units {
			toc := byte(base+step.size)<<3 | stereo
			packets = append(packets, append([]byte{toc}, silenceFrame...))
			units -= step.units
		}
	}
	return packets, nil
}

// silenceFrame is the shortest CELT frame whose first range-coded symbol,
// the silence flag (probability 2^-15), decodes as 1. Bytes past the end of a
// frame read as zeros; the complemented value register therefore fills with
// ones, so a single byte cannot pull the coder value low enough. libopus uses
// FF FF; FF FE is equally valid. The decoder then treats the remaining bits
// as consumed.
var silenceFrame = []byte{0xFF, 0xFE}
