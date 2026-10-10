package silk_test

import (
	"path/filepath"
	"testing"
)

// FuzzDecode feeds arbitrary SILK-only packets, losses and FEC decodes to
// the decoder on mono and stereo outputs: it must never panic. The input is
// split into packets by a length byte each; its first byte selects the
// output channels, and the top bits of each length byte turn a packet into a
// loss or an FEC decode. The TOC is forced to a SILK-only configuration.
func FuzzDecode(f *testing.F) {
	for _, name := range []string{"ogg-silk-wb", "ogg-silk-nb-10ms", "ogg-silk-mb-60ms", "ogg-silk-wb-fec"} {
		s := readOggOpus(f, filepath.Join(repoTestdata, name+".opus"))
		seed := []byte{0}
		for _, p := range s.packets[:8] {
			if len(p) > 63 {
				p = p[:63]
			}
			seed = append(seed, byte(len(p)))
			seed = append(seed, p...)
		}
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 {
			return
		}
		channels := 1 + int(data[0]&1)
		data = data[1:]
		d := newSilkOpus(channels)
		out := make([]float32, 5760*channels)
		for len(data) > 0 {
			n := int(data[0] & 63)
			mode := data[0] >> 6
			data = data[1:]
			n = min(n, len(data))
			p := append([]byte(nil), data[:n]...)
			data = data[n:]
			if len(p) > 0 && p[0]>>3 >= 12 {
				p[0] &= 0x5f // a SILK-only configuration
			}
			switch {
			case mode == 1:
				d.decode(nil, 960, out) //nolint:errcheck // errors are fine, panics are not
			case mode == 2 && len(p) > 0:
				d.decodeFEC(p, 960, out) //nolint:errcheck // errors are fine, panics are not
			case len(p) > 0:
				d.decode(p, 5760, out) //nolint:errcheck // errors are fine, panics are not
			}
		}
	})
}
