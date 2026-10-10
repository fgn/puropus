package opus

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"slices"
	"testing"
)

// primePLCDecoder decodes the twelve packets before the first loss of a
// transition vector on a mono decoder and returns it with the packet that
// follows the loss.
func primePLCDecoder(t *testing.T, name string) (*Decoder, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/transition-vectors/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var vector transitionVectorCase
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(1)
	if err != nil {
		t.Fatal(err)
	}
	var out [MaxPacketSamples]float32
	for _, record := range vector.Records[:12] {
		packet, err := hex.DecodeString(record.PacketHex)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Decode(packet, out[:]); err != nil {
			t.Fatal(err)
		}
	}
	next, err := hex.DecodeString(vector.Records[13].PacketHex)
	if err != nil {
		t.Fatal(err)
	}
	return d, next
}

// TestConcealValidationAndReset checks that Conceal rejects invalid durations
// and short buffers without touching the state or the buffer, that a loss
// leaves no final range, and that Reset leaves a decoder equal to a fresh
// one, concealing silence.
func TestConcealValidationAndReset(t *testing.T) {
	for _, name := range []string{"loss-silk", "loss-hybrid", "loss-celt"} {
		t.Run(name, func(t *testing.T) {
			d, next := primePLCDecoder(t, name)
			before := *d
			buf := make([]float32, MaxPacketSamples+8)
			for i := range buf {
				buf[i] = 12345
			}
			for _, n := range []int{-120, 0, 1, 119, 121, MaxPacketSamples + 120} {
				if _, err := d.Conceal(n, buf[:MaxPacketSamples]); err == nil {
					t.Fatalf("accepted invalid duration %d", n)
				}
				if !reflect.DeepEqual(*d, before) {
					t.Fatal("invalid concealment changed decoder state")
				}
				for _, v := range buf {
					if v != 12345 {
						t.Fatal("invalid concealment wrote output")
					}
				}
			}
			if _, err := d.Conceal(960, buf[:959]); err == nil {
				t.Fatal("accepted a short output buffer")
			}
			if !reflect.DeepEqual(*d, before) {
				t.Fatal("short buffer changed decoder state")
			}
			if n, err := d.Conceal(MaxPacketSamples, buf[:MaxPacketSamples]); err != nil || n != MaxPacketSamples {
				t.Fatalf("conceal = %d,%v", n, err)
			}
			for _, v := range buf[MaxPacketSamples:] {
				if v != 12345 {
					t.Fatal("concealment overran output")
				}
			}
			if d.FinalRange() != 0 {
				t.Fatal("loss has a nonzero final entropy range")
			}
			d.Reset()
			if n, err := d.Conceal(360, buf); err != nil || n != 360 {
				t.Fatalf("cold conceal = %d,%v", n, err)
			}
			for _, v := range buf[:360] {
				if v != 0 {
					t.Fatal("reset retained concealed audio")
				}
			}
			fresh, _ := NewDecoder(1)
			var want [MaxPacketSamples]float32
			n, err := d.Decode(next, buf)
			if err != nil {
				t.Fatal(err)
			}
			m, err := fresh.Decode(next, want[:])
			if err != nil || n != m || !slices.Equal(buf[:n], want[:m]) {
				t.Fatal("reset retained codec state")
			}
		})
	}
}

// TestDTXUsesPreviousModeAndDoesNotDecodeEmptyEntropy checks that frames of
// at most one byte, in code 0 and code 3 packets, conceal exactly as Conceal
// does in the previous mode.
func TestDTXUsesPreviousModeAndDoesNotDecodeEmptyEntropy(t *testing.T) {
	for _, name := range []string{"loss-silk", "loss-hybrid", "loss-celt"} {
		for _, packet := range [][]byte{{0xf8}, {0xf8, 0}, {0x4b, 3}} {
			d, _ := primePLCDecoder(t, name)
			reference := *d
			duration := 960
			if len(packet) == 2 && packet[0]&3 == 3 {
				duration = 2880
			}
			var want, got [MaxPacketSamples]float32
			if _, err := reference.Conceal(duration, want[:]); err != nil {
				t.Fatal(err)
			}
			n, err := d.Decode(packet, got[:])
			if err != nil || n != duration || !slices.Equal(got[:n], want[:duration]) {
				t.Fatalf("%s %x: DTX did not conceal in the previous mode: %d,%v", name, packet, n, err)
			}
		}
	}
}

// TestFailedDecodePreservesRecoveryState checks that malformed packets, forged
// Packet metadata and short buffers leave the decoder state unchanged, so the
// next good packet decodes as if they never arrived.
func TestFailedDecodePreservesRecoveryState(t *testing.T) {
	d, next := primePLCDecoder(t, "loss-hybrid")
	before := *d
	var buf [MaxPacketSamples]float32
	if _, err := d.Decode([]byte{3, 0}, buf[:]); err == nil {
		t.Fatal("accepted malformed packet")
	}
	parsed, err := Parse(next, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Packet){
		func(p *Packet) { p.FrameOffset[0] = -1 },
		func(p *Packet) { p.FrameSize[0] = int(^uint(0) >> 1) },
		func(p *Packet) { p.FrameCount = MaxFrames + 1 },
		func(p *Packet) { p.Mode = ModeSILK },
		func(p *Packet) { p.Bandwidth = BandwidthNarrowband },
	} {
		bad := parsed
		mutate(&bad)
		if _, err := d.DecodeParsed(bad, next, buf[:]); err == nil {
			t.Fatal("accepted forged packet metadata")
		}
		if !reflect.DeepEqual(*d, before) {
			t.Fatal("failed packet changed prediction state")
		}
	}
	if _, err := d.Decode(next, buf[:parsed.Duration48()-1]); err == nil {
		t.Fatal("accepted short output buffer")
	}
	if !reflect.DeepEqual(*d, before) {
		t.Fatal("short buffer changed prediction state")
	}
	var want [MaxPacketSamples]float32
	n, err := d.Decode(next, buf[:])
	if err != nil {
		t.Fatal(err)
	}
	m, err := before.Decode(next, want[:])
	if err != nil || m != n || !slices.Equal(buf[:n], want[:m]) {
		t.Fatal("error corrupted recovery PCM")
	}
}

// TestConcealLongBurstRemainsFinite conceals five seconds after each kind of
// stream and checks that the output stays finite and within full scale, and
// that the next packet decodes to finite PCM.
func TestConcealLongBurstRemainsFinite(t *testing.T) {
	for _, name := range []string{"loss-silk-nb10", "loss-silk-mb10", "loss-silk", "loss-hybrid", "loss-celt-2p5ms", "loss-celt"} {
		d, next := primePLCDecoder(t, name)
		var out [MaxPacketSamples]float32
		// Five seconds forces pitch attenuation, comfort noise and noise fallback.
		for i := 0; i < 250; i++ {
			if _, err := d.Conceal(960, out[:]); err != nil {
				t.Fatal(err)
			}
			for _, v := range out[:960] {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 1 {
					t.Fatalf("%s: unstable loss sample %g", name, v)
				}
			}
		}
		n, err := d.Decode(next, out[:])
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range out[:n] {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				t.Fatalf("%s: non-finite recovery", name)
			}
		}
	}
}
