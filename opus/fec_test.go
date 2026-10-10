package opus

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// TestDecodeFECRejectsWithoutChangingState checks DecodeFEC's rejection
// contract after SILK, hybrid and CELT packets, on mono and stereo decoders:
// a next packet that does not parse, an invalid duration and a short output
// buffer fail without touching the decoder or the buffer, so concealing the
// interval instead gives what a decoder that never saw the call gives.
func TestDecodeFECRejectsWithoutChangingState(t *testing.T) {
	invalid := map[string][]byte{
		"nil":                   nil,
		"empty":                 {},
		"code 1 of odd length":  {9<<3 | 1, 0x01},
		"code 2 length overrun": {9<<3 | 2, 0xfc},
		"code 3 without count":  {9<<3 | 3},
		"code 3 of zero frames": {9<<3 | 3, 0x00},
		"code 3 over 120 ms":    {11<<3 | 3, 0x03},
	}
	for _, name := range []string{"ogg-silk-wb-fec.opus", "ogg-hybrid-fb-fec.opus", "ogg-celt-fb.opus"} {
		s := readContainerStream(t, filepath.Join("..", "testdata", name))
		valid := readContainerStream(t, filepath.Join("..", "testdata", "ogg-silk-wb-fec.opus")).packets[31]
		for _, channels := range []int{1, 2} {
			d, err := NewDecoder(channels)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]float32, MaxPacketSamples*channels+8)
			for _, p := range s.packets[:30] {
				if _, err := d.Decode(p, out); err != nil {
					t.Fatal(err)
				}
			}
			before := *d
			for i := range out {
				out[i] = 12345
			}
			unchanged := func(what string) {
				t.Helper()
				if !reflect.DeepEqual(*d, before) {
					t.Fatalf("%s, %d channels: %s changed the decoder state", name, channels, what)
				}
				for _, v := range out {
					if v != 12345 {
						t.Fatalf("%s, %d channels: %s wrote output", name, channels, what)
					}
				}
			}
			for what, next := range invalid {
				if _, err := Parse(next, false); err == nil {
					t.Fatalf("%s parses", what)
				}
				if _, err := d.DecodeFEC(next, 960, out[:MaxPacketSamples*channels]); err == nil {
					t.Fatalf("%s, %d channels: DecodeFEC accepted %s", name, channels, what)
				}
				unchanged(what)
			}
			for _, samples := range []int{-960, 0, 1, 119, 121, 1000, MaxPacketSamples + 120} {
				if _, err := d.DecodeFEC(valid, samples, out[:MaxPacketSamples*channels]); err == nil {
					t.Fatalf("%s, %d channels: DecodeFEC accepted %d samples", name, channels, samples)
				}
				unchanged("an invalid duration")
			}
			if _, err := d.DecodeFEC(valid, 960, out[:960*channels-1]); err == nil {
				t.Fatalf("%s, %d channels: DecodeFEC accepted a short buffer", name, channels)
			}
			unchanged("a short buffer")

			// The interval then conceals, and the next packet decodes, as on
			// a decoder that never saw the rejected calls.
			reference := before
			want := make([]float32, MaxPacketSamples*channels)
			n, err1 := d.Conceal(960, out)
			m, err2 := reference.Conceal(960, want)
			if err1 != nil || err2 != nil || n != m || !slices.Equal(out[:n*channels], want[:m*channels]) {
				t.Fatalf("%s, %d channels: concealment after a rejected DecodeFEC differs", name, channels)
			}
			n, err1 = d.DecodeFEC(valid, 1920, out)
			m, err2 = reference.DecodeFEC(valid, 1920, want)
			if err1 != nil || err2 != nil || n != 1920 || n != m || !slices.Equal(out[:n*channels], want[:m*channels]) ||
				d.FinalRange() != reference.FinalRange() {
				t.Fatalf("%s, %d channels: DecodeFEC after a rejected DecodeFEC differs", name, channels)
			}
		}
	}
}

// TestDecodeFECConcealsWhereNoFECApplies checks the cases in which
// opus_decode_native runs plain concealment for decode_fec: a CELT-only next
// packet, a last decoded packet that was CELT-only, and a lost interval
// shorter than the next packet's frames. DecodeFEC must then be Conceal, in
// output, final range and state.
func TestDecodeFECConcealsWhereNoFECApplies(t *testing.T) {
	silk := readContainerStream(t, filepath.Join("..", "testdata", "ogg-silk-wb-fec.opus")).packets
	silk60 := readContainerStream(t, filepath.Join("..", "testdata", "ogg-silk-wb-60ms-fec.opus")).packets
	hybrid := readContainerStream(t, filepath.Join("..", "testdata", "ogg-hybrid-fb-fec.opus")).packets
	celt := readContainerStream(t, filepath.Join("..", "testdata", "ogg-celt-fb.opus")).packets
	cases := []struct {
		name    string
		prime   [][]byte
		next    []byte
		samples int
	}{
		{"CELT next after SILK", silk[:20], celt[20], 960},
		{"CELT next after hybrid", hybrid[:20], celt[20], 1920},
		{"SILK next after CELT", celt[:20], silk[21], 960},
		{"hybrid next after CELT", celt[:20], hybrid[21], 2880},
		{"60 ms next after 20 ms", silk[:20], silk60[5], 960},
		{"20 ms next after hybrid, 10 ms lost", hybrid[:20], silk[21], 480},
	}
	for _, c := range cases {
		if !HasLBRR(c.next) && tocMode(c.next[0]) != ModeCELT {
			t.Fatalf("%s: the next packet carries no LBRR data", c.name)
		}
		for _, channels := range []int{1, 2} {
			d, err := NewDecoder(channels)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]float32, MaxPacketSamples*channels)
			want := make([]float32, MaxPacketSamples*channels)
			for _, p := range c.prime {
				if _, err := d.Decode(p, out); err != nil {
					t.Fatal(err)
				}
			}
			reference := *d
			n, err1 := d.DecodeFEC(c.next, c.samples, out)
			m, err2 := reference.Conceal(c.samples, want)
			if err1 != nil || err2 != nil || n != c.samples || n != m || !slices.Equal(out[:n*channels], want[:m*channels]) {
				t.Fatalf("%s, %d channels: DecodeFEC differs from Conceal", c.name, channels)
			}
			if d.FinalRange() != 0 || !reflect.DeepEqual(*d, reference) {
				t.Fatalf("%s, %d channels: DecodeFEC left another state than Conceal", c.name, channels)
			}
		}
	}
}

// TestDecodeFECUsesLBRR checks that DecodeFEC does decode in-band FEC where
// it applies, so the decode logs' FEC cases cannot pass by concealing: on a
// SILK and a hybrid stream, decoding a lost packet from the next packet's
// LBRR data gives other audio than concealment, a nonzero final range, and
// takes the next packet's configuration.
func TestDecodeFECUsesLBRR(t *testing.T) {
	for _, name := range []string{"ogg-silk-wb-fec.opus", "ogg-hybrid-fb-fec.opus", "ogg-silk-wb-60ms-fec.opus"} {
		s := readContainerStream(t, filepath.Join("..", "testdata", name)).packets
		next := s[11]
		parsed, err := Parse(next, false)
		if err != nil || !HasLBRR(next) {
			t.Fatalf("%s: packet 11 carries no LBRR data", name)
		}
		d, _ := NewDecoder(1)
		out := make([]float32, MaxPacketSamples)
		want := make([]float32, MaxPacketSamples)
		for _, p := range s[:10] {
			if _, err := d.Decode(p, out); err != nil {
				t.Fatal(err)
			}
		}
		concealed := *d
		lost := parsed.Duration48()
		if n, err := d.DecodeFEC(next, lost, out); err != nil || n != lost {
			t.Fatalf("%s: DecodeFEC = %d, %v", name, n, err)
		}
		if _, err := concealed.Conceal(lost, want); err != nil {
			t.Fatal(err)
		}
		if slices.Equal(out[:lost], want[:lost]) || d.FinalRange() == 0 {
			t.Fatalf("%s: DecodeFEC concealed instead of decoding LBRR data", name)
		}
		if d.mode != parsed.Mode || d.bandwidth != parsed.Bandwidth || d.frameSize != parsed.FrameDuration || d.lastPacketDuration != lost {
			t.Fatalf("%s: DecodeFEC did not take the next packet's configuration", name)
		}
	}
}
