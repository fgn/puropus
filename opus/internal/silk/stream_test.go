package silk_test

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// repoTestdata is the repository's testdata directory.
const repoTestdata = "../../../testdata"

// TestOggFixtures decodes every SILK-only fixture of the repository frame by
// frame through package silk (configs 0-11, 10 to 60 ms, in-band FEC, a
// bandwidth switch) and requires libopus's final range after every packet
// and libopus's 48 kHz float output, bit for bit. The goldens are
// opus_decode_float output after the pre-skip and end trimming
// (testdata/manifest.json); the ranges come from tools/libopus-ref/
// gen-silk-ranges.sh, which checks that decode_float reproduces the goldens.
//
// libopus's encoder smooths SILK bandwidth switches with CELT redundancy
// frames, which the CELT decoder handles: for those packets the final range
// (which XORs in the CELT range) and the 2.5 or 5 ms that libopus mixes with
// the CELT audio are left out, and the test logs how many.
func TestOggFixtures(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join(repoTestdata, "ogg-silk-*.opus"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no SILK fixtures: %v", err)
	}
	for _, fixture := range fixtures {
		stem := strings.TrimSuffix(filepath.Base(fixture), ".opus")
		t.Run(stem, func(t *testing.T) {
			golden := filepath.Join(repoTestdata, stem+".48k.f32le")
			if _, err := os.Stat(golden); err != nil {
				golden = filepath.Join(repoTestdata, stem+".libopus48.f32le")
			}
			want := readF32(t, golden)
			ranges := readRanges(t, filepath.Join("testdata", "ranges", stem+".txt"))
			s := readOggOpus(t, fixture)
			if len(ranges) != len(s.packets) {
				t.Fatalf("%d packets, %d reference ranges", len(s.packets), len(ranges))
			}

			d := newSilkOpus(s.channels)
			var (
				pcm  []float32
				celt int // packets with CELT redundancy
			)
			excluded := make([][2]int, 0, 16) // CELT redundancy samples, interleaved
			out := make([]float32, 5760*s.channels)
			rangeErrors := 0
			for i, p := range s.packets {
				n, err := d.decode(p, 5760, out)
				if err != nil {
					t.Fatalf("packet %d: %v", i, err)
				}
				if n != ranges[i].samples {
					t.Fatalf("packet %d: %d samples, libopus %d", i, n, ranges[i].samples)
				}
				for _, r := range d.celtSamples {
					base := len(pcm)
					excluded = append(excluded, [2]int{base + r[0]*s.channels, base + r[1]*s.channels})
				}
				if d.celtRange {
					celt++
				} else if d.rangeFinal != ranges[i].rng {
					if rangeErrors < 5 {
						t.Errorf("packet %d: final range %08x, libopus %08x", i, d.rangeFinal, ranges[i].rng)
					}
					rangeErrors++
				}
				pcm = append(pcm, out[:n*s.channels]...)
			}
			if rangeErrors > 0 {
				t.Errorf("%d of %d final ranges differ", rangeErrors, len(s.packets))
			}
			skip := s.preSkip * s.channels
			if skip+len(want) > len(pcm) {
				t.Fatalf("decoded %d samples, the golden needs %d after a pre-skip of %d", len(pcm), len(want), skip)
			}
			got := pcm[skip : skip+len(want)]
			// Copy the reference into the samples libopus mixes with CELT.
			got = append([]float32(nil), got...)
			nExcluded := 0
			for _, r := range excluded {
				for i := max(r[0]-skip, 0); i < min(r[1]-skip, len(want)); i++ {
					got[i] = want[i]
					nExcluded++
				}
			}
			if msg := pcmMismatch(got, want); msg != "" {
				t.Errorf("PCM: %s", msg)
			}
			if celt > 0 {
				t.Logf("%d packets with CELT redundancy: their final ranges and %d samples mixed with CELT audio not compared", celt, nExcluded)
			}
		})
	}
}

// TestFixtureLoss decodes SILK fixtures (10, 20, 40 and 60 ms, NB to WB,
// with and without in-band FEC) with records lost as
// tools/libopus-ref/silk_ref.c ("loss") loses them for libopus: single
// losses every ninth packet and a run of four. Lost records are concealed
// for the last packet duration, as opus_decode_float(st, NULL, 0, pcm,
// OPUS_GET_LAST_PACKET_DURATION, 0) does. Every record's samples (by an
// FNV-1a hash of the float output), sample count and final range must match
// libopus.
func TestFixtureLoss(t *testing.T) {
	testFixtureLoss(t, "loss")
}

// TestFixtureFEC is TestFixtureLoss on the fixtures with in-band FEC, where
// a lost record followed by a received one is decoded from the LBRR data of
// the next packet as opus_decode_float(st, next, len, pcm, duration, 1)
// does (silk_Decode with FLAG_DECODE_LBRR).
func TestFixtureFEC(t *testing.T) {
	testFixtureLoss(t, "fec")
}

func testFixtureLoss(t *testing.T, kind string) {
	raw, err := os.ReadFile(filepath.Join("testdata", kind+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	type lossRecord struct {
		lost    bool
		samples int
		rng     uint32
		fnv     uint64
	}
	streams := map[string][]lossRecord{}
	var names []string
	for line := range strings.Lines(string(raw)) {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if len(f) != 7 || f[0] != kind {
			t.Fatalf("bad line %q", line)
		}
		index, err1 := strconv.Atoi(f[2])
		samples, err2 := strconv.Atoi(f[4])
		rng, err3 := strconv.ParseUint(f[5], 10, 32)
		fnv, err4 := strconv.ParseUint(f[6], 16, 64)
		if err := errors.Join(err1, err2, err3, err4); err != nil || index != len(streams[f[1]]) {
			t.Fatalf("bad line %q: %v", line, err)
		}
		if index == 0 {
			names = append(names, f[1])
		}
		streams[f[1]] = append(streams[f[1]], lossRecord{f[3] == "1", samples, uint32(rng), fnv})
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			recs := streams[name]
			s := readOggOpus(t, filepath.Join(repoTestdata, name+".opus"))
			if len(s.packets) != len(recs) {
				t.Fatalf("%d packets, %d records", len(s.packets), len(recs))
			}
			d := newSilkOpus(1)
			out := make([]float32, 5760)
			lost, fec := 0, 0
			for i, r := range recs {
				var n int
				var err error
				if r.lost {
					lost++
					duration := d.lastPacketDuration
					if duration == 0 {
						duration = 960
					}
					if kind == "fec" && i+1 < len(recs) && !recs[i+1].lost {
						fec++
						n, err = d.decodeFEC(s.packets[i+1], duration, out)
					} else {
						n, err = d.decode(nil, duration, out)
					}
				} else {
					n, err = d.decode(s.packets[i], 5760, out)
				}
				if err != nil {
					t.Fatalf("record %d: %v", i, err)
				}
				if n != r.samples {
					t.Fatalf("record %d: %d samples, libopus %d", i, n, r.samples)
				}
				if d.rangeFinal != r.rng {
					t.Errorf("record %d: final range %08x, libopus %08x", i, d.rangeFinal, r.rng)
				}
				h := fnv.New64a()
				for _, x := range out[:n] {
					var b [4]byte
					binary.LittleEndian.PutUint32(b[:], math.Float32bits(x))
					h.Write(b[:])
				}
				if h.Sum64() != r.fnv {
					t.Fatalf("record %d (lost %v): output differs from libopus", i, r.lost)
				}
			}
			t.Logf("%d records, %d lost, %d of them decoded from FEC", len(recs), lost, fec)
		})
	}
}
