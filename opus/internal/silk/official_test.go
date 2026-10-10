package silk_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// officialOutput is a line of testdata/official-vectors.txt: libopus's
// decode of an RFC 8251 vector.
type officialOutput struct {
	samples int
	sha256  string
}

// readOfficialOutputs reads testdata/official-vectors.txt (written by
// tools/libopus-ref/gen-silk-ranges.sh): name, channels, samples per channel
// and the SHA-256 of decode_float's float32 output.
func readOfficialOutputs(t *testing.T) map[string]officialOutput {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "official-vectors.txt"))
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]officialOutput{}
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		n, err := strconv.Atoi(fields[2])
		if len(fields) != 4 || err != nil {
			t.Fatalf("official-vectors.txt: bad line %q", line)
		}
		outputs[fields[0]+"/"+fields[1]+"ch"] = officialOutput{samples: n, sha256: fields[3]}
	}
	return outputs
}

// TestOfficialVectors decodes the SILK-only RFC 8251 test vectors (02, 03
// and 04: narrowband, mediumband and wideband, 10 to 60 ms, mono and stereo
// packets) through package silk with stereo and mono output. It requires the
// final range of every packet as opus_demo checks it, libopus's float output
// bit for bit (by its SHA-256, testdata/official-vectors.txt), and for
// stereo output the reference decoder output (testvectorNN.dec, identical to
// testvectorNNm.dec for these vectors) sample for sample. Set
// OPUS_TEST_VECTORS to the directory of the vectors (opus_newvectors); the
// test is skipped without it.
func TestOfficialVectors(t *testing.T) {
	dir := os.Getenv("OPUS_TEST_VECTORS")
	if dir == "" {
		t.Skip("OPUS_TEST_VECTORS is not set")
	}
	outputs := readOfficialOutputs(t)
	for _, n := range []int{2, 3, 4} {
		name := fmt.Sprintf("testvector%02d", n)
		recs := readBitFile(t, filepath.Join(dir, name+".bit"))
		for _, channels := range []int{2, 1} {
			id := fmt.Sprintf("%s/%dch", name, channels)
			t.Run(id, func(t *testing.T) {
				ref, ok := outputs[id]
				if !ok {
					t.Fatal("no libopus reference")
				}
				d := newSilkOpus(channels)
				out := make([]float32, 5760*channels)
				var pcm []float32
				rangeErrors, ranges := 0, 0
				for i, r := range recs {
					var data []byte
					if len(r.packet) > 0 {
						data = r.packet
					}
					n, err := d.decode(data, 5760, out)
					if err != nil {
						t.Fatalf("packet %d: %v", i, err)
					}
					if len(d.celtSamples) > 0 || d.celtRange {
						t.Fatalf("packet %d: CELT redundancy", i)
					}
					if r.rng != 0 && data != nil {
						ranges++
						if d.rangeFinal != r.rng {
							if rangeErrors < 5 {
								t.Errorf("packet %d: final range %08x, want %08x", i, d.rangeFinal, r.rng)
							}
							rangeErrors++
						}
					}
					pcm = append(pcm, out[:n*channels]...)
				}
				if rangeErrors > 0 {
					t.Errorf("%d of %d final ranges differ", rangeErrors, ranges)
				}

				if len(pcm) != ref.samples*channels {
					t.Fatalf("%d samples per channel, libopus %d", len(pcm)/channels, ref.samples)
				}
				raw := make([]byte, 4*len(pcm))
				for i, x := range pcm {
					binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(x))
				}
				sum := sha256.Sum256(raw)
				if got := hex.EncodeToString(sum[:]); got != ref.sha256 {
					t.Errorf("float output SHA-256 %s, libopus %s", got, ref.sha256)
				}

				if channels == 2 {
					want := readS16(t, filepath.Join(dir, name+".dec"))
					if len(want) != len(pcm) {
						t.Fatalf("%d samples, reference decoder %d", len(pcm), len(want))
					}
					diffs, first := 0, -1
					for i := range want {
						// The float output is int16/32768, so this is exact.
						if int16(pcm[i]*32768) != want[i] {
							if first < 0 {
								first = i
							}
							diffs++
						}
					}
					if diffs > 0 {
						t.Errorf("%d of %d samples differ from the reference decoder, first at %d", diffs, len(want), first)
					}
				}
				t.Logf("%d packets, %d final ranges checked", len(recs), ranges)
			})
		}
	}
}
