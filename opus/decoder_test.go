package opus

import (
	"encoding/hex"
	"errors"
	"math/rand"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// TestHybridFirstPacketMatchesLibopus decodes hybridAnchorPacket on a fresh
// decoder and compares the PCM with libopus.
func TestHybridFirstPacketMatchesLibopus(t *testing.T) {
	decoder, err := NewDecoder(1)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, 960)
	samples, err := decoder.Decode(hybridAnchorPacket(), out)
	if err != nil || samples != 960 {
		t.Fatalf("samples=%d err=%v", samples, err)
	}
	want := readSilkUnitReferences(t).Hybrid
	if len(want) != 960 {
		t.Fatal("incomplete hybrid oracle")
	}
	checkFloatVec(t, "hybrid packet", out, want, 1e-8)
}

// hybridAnchorPacket is a 20 ms mono FB hybrid packet (configuration 15)
// whose SILK layer is one inactive WB frame.
func hybridAnchorPacket() []byte {
	return []byte{
		0x78,
		0x0b, 0xe8, 0x3e, 0xcf, 0xb5, 0xce, 0x77, 0xd9,
		0x71, 0x55, 0x20, 0xcb, 0x41, 0x34, 0x7d, 0x80,
		0xc1, 0x9a, 0xdc, 0x78, 0xb0, 0x88, 0x29, 0xb9,
		0xeb, 0x38, 0x44, 0xab, 0xb9, 0x95, 0x62, 0x9b,
		0xae, 0x55, 0xb2, 0x41, 0x99, 0xb6, 0xae, 0x37,
		0x7b, 0x44, 0x87, 0x2b, 0x8a, 0x39, 0x78, 0x52,
		0x9c, 0xa5, 0x89, 0xbf,
	}
}

// TestCELTPacketsDecodeWithoutError pins the invariant DecodeParsed relies on
// to skip its decoder snapshot: once a CELT packet parses, decoding it cannot
// fail, whatever its payload and whatever mode came before. Random CELT
// packets are interleaved with random SILK and hybrid packets, which may
// fail, on mono and stereo decoders.
func TestCELTPacketsDecodeWithoutError(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	out := make([]float32, 2*MaxPacketSamples)
	celt := 0
	for _, channels := range []int{1, 2} {
		d, err := NewDecoder(channels)
		if err != nil {
			t.Fatal(err)
		}
		for iter := 0; iter < 8000; iter++ {
			packet := make([]byte, 1+rng.Intn(400))
			rng.Read(packet)
			parsed, err := Parse(packet, false)
			if err != nil {
				continue
			}
			_, err = d.DecodeParsed(parsed, packet, out)
			if parsed.Mode != ModeCELT {
				continue
			}
			celt++
			if err != nil {
				t.Fatalf("channels=%d packet %x: CELT decode failed: %v", channels, packet, err)
			}
		}
	}
	if celt < 3000 {
		t.Fatalf("only %d CELT packets parsed", celt)
	}
}

// TestSILKPacketsDecodeWithoutError pins the other invariant that lets
// DecodeParsed skip its snapshot: a SILK-only frame cannot reach
// opus_decode_frame's redundancy sanity check (the redundancy takes exactly
// the bytes after the SILK data), and silk_Decode cannot fail on the
// parameters a TOC gives, so once a SILK-only packet parses, decoding it
// cannot fail. Random SILK-only packets are interleaved with random CELT and
// hybrid packets on mono and stereo decoders.
func TestSILKPacketsDecodeWithoutError(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	out := make([]float32, 2*MaxPacketSamples)
	silk := 0
	for _, channels := range []int{1, 2} {
		d, err := NewDecoder(channels)
		if err != nil {
			t.Fatal(err)
		}
		for iter := 0; iter < 8000; iter++ {
			packet := make([]byte, 1+rng.Intn(400))
			rng.Read(packet)
			parsed, err := Parse(packet, false)
			if err != nil {
				continue
			}
			_, err = d.DecodeParsed(parsed, packet, out)
			if parsed.Mode != ModeSILK {
				continue
			}
			silk++
			if err != nil {
				t.Fatalf("channels=%d packet %x: SILK decode failed: %v", channels, packet, err)
			}
		}
	}
	if silk < 2000 {
		t.Fatalf("only %d SILK packets parsed", silk)
	}
}

// invalidHybridFrame is a 10 ms fullband hybrid frame (configuration 14)
// whose redundancy flag is set and whose CELT redundancy, 220 bytes, is
// longer than the frame: it reaches opus_decode_frame's sanity check on mono
// and stereo decoders alike, after its SILK layer has been decoded.
func invalidHybridFrame(t *testing.T) []byte {
	t.Helper()
	frame, err := hex.DecodeString("3fc0d5d41550fd4f9ed4d013e1caaeb47059f3219f26ccd99dd76bc80aa2e84cf66451135f30abc8a958ebbf964b271ac0d785")
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

// stateOf returns everything of d that a later packet depends on.
func stateOf(d *Decoder) snapshot {
	var s snapshot
	s.state = d.decoderState
	s.decControl = d.decControl
	d.silkDec.SaveState(&s.silk)
	d.celtDec.SaveState(&s.celt)
	return s
}

// TestRejectedRedundancyLeavesStateUnchanged decodes hybrid packets with a
// frame that reaches the redundancy sanity check, alone and after valid
// frames of the same packet, after SILK, hybrid and CELT streams (the latter
// resets the SILK decoder before the check), on mono and stereo decoders.
// Each must fail and leave the decoder as it was: same state, same final
// range, and the same output for the next packet as a copy that never saw it.
func TestRejectedRedundancyLeavesStateUnchanged(t *testing.T) {
	bad := invalidHybridFrame(t)
	hybrid := readContainerStream(t, filepath.Join("..", "testdata", "ogg-hybrid-fb-10ms.opus"))
	valid := hybrid.packets[20]
	if valid[0] != 14<<3 || len(valid)-1 >= 252 {
		t.Fatalf("unexpected fixture packet %x", valid[:1])
	}
	frames := func(fs ...[]byte) []byte {
		// A code 3 VBR packet of configuration 14, mono.
		p := []byte{14<<3 | 3, 0x80 | byte(len(fs))}
		for _, f := range fs[:len(fs)-1] {
			p = append(p, byte(len(f)))
		}
		for _, f := range fs {
			p = append(p, f...)
		}
		return p
	}
	packets := map[string][]byte{
		"alone":          append([]byte{14 << 3}, bad...),
		"second of two":  frames(valid[1:], bad),
		"third of three": frames(valid[1:], valid[1:], bad),
		"first of three": frames(bad, valid[1:], valid[1:]),
		"code 1 pair":    append([]byte{14<<3 | 1}, append(append([]byte{}, bad...), bad...)...),
	}
	priming := []packetStream{
		readContainerStream(t, filepath.Join("..", "testdata", "ogg-silk-wb.opus")),
		hybrid,
		readContainerStream(t, filepath.Join("..", "testdata", "ogg-celt-fb.opus")),
		readVectorStream(t, "modes", 2),
	}
	for _, prime := range priming {
		for _, channels := range []int{1, 2} {
			for name, packet := range packets {
				d, err := NewDecoder(channels)
				if err != nil {
					t.Fatal(err)
				}
				out := make([]float32, MaxPacketSamples*channels)
				want := make([]float32, MaxPacketSamples*channels)
				for _, p := range prime.packets[:30] {
					if _, err := d.Decode(p, out); err != nil {
						t.Fatal(err)
					}
				}
				reference := *d
				before := stateOf(d)
				if _, err := d.Decode(packet, out); !errors.Is(err, errRedundancy) {
					t.Fatalf("%s, %d channels, %s: err = %v, want the redundancy error", prime.name, channels, name, err)
				}
				if !reflect.DeepEqual(stateOf(d), before) {
					t.Fatalf("%s, %d channels, %s: rejected packet changed the decoder state", prime.name, channels, name)
				}
				if d.FinalRange() != reference.FinalRange() {
					t.Fatalf("%s, %d channels, %s: rejected packet changed the final range", prime.name, channels, name)
				}
				// The next packet, and a concealment, decode as if the
				// rejected packet had never arrived.
				for _, next := range [][]byte{nil, prime.packets[30], valid} {
					var n, m int
					var err1, err2 error
					if next == nil {
						n, err1 = d.Conceal(960, out)
						m, err2 = reference.Conceal(960, want)
					} else {
						n, err1 = d.Decode(next, out)
						m, err2 = reference.Decode(next, want)
					}
					if err1 != nil || err2 != nil || n != m || !slices.Equal(out[:n*channels], want[:m*channels]) ||
						d.FinalRange() != reference.FinalRange() {
						t.Fatalf("%s, %d channels, %s: decoding after the rejected packet differs", prime.name, channels, name)
					}
				}
			}
		}
	}
}
