package opus

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// decodeLogCase is one case of testdata/decode-logs/manifest.json: a packet
// list, the records lost from it and how libopus decoded them
// (tools/libopus-ref/decode-logs.sh).
type decodeLogCase struct {
	Name   string `json:"name"`
	Source struct {
		Fixture  string `json:"fixture"`
		Vector   string `json:"vector"`
		Splice   string `json:"splice"`
		Segments []struct {
			Fixture string `json:"fixture"`
			First   int    `json:"first"`
			Count   int    `json:"count"`
		} `json:"segments"`
	} `json:"source"`
	Channels    int    `json:"channels"`
	Skip        int    `json:"skip"`
	Trim        int    `json:"trim"`
	Drop        string `json:"drop"`
	FEC         string `json:"fec"`
	Records     int    `json:"records"`
	LostRecords int    `json:"lost_records"`
	FECRecords  int    `json:"fec_records"`
	LogSHA256   string `json:"log_sha256"`
	PCMSHA256   string `json:"pcm_sha256"`
	// DefaultBuildDiffers counts the records whose PCM libopus's default
	// build decodes differently from its generic build.
	DefaultBuildDiffers int `json:"default_build_pcm_differs"`
}

// Exact reports whether the case's PCM must equal libopus's bit for bit
// here: on amd64 and 386, where Go's float32 arithmetic and math functions
// round as the generic build's do on these streams (as for the CELT
// package's frame logs), and on every architecture for the cases that the
// two libopus builds decode alike, whose PCM is integer SILK output.
// Elsewhere other cases only report their bit-exact records.
func (c decodeLogCase) Exact() bool {
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "386" || c.DefaultBuildDiffers == 0
}

// decodeLogRecord is a record of a case's packet list: a packet, or a loss
// of duration samples (an empty record of a sequence, or a dropped packet).
type decodeLogRecord struct {
	packet   []byte
	lost     bool
	duration int
}

// decodeLogLine is one line of a case's log: how libopus decoded a record.
type decodeLogLine struct {
	kind    string // packet, lost or fec
	bytes   int
	samples int
	rng     uint32
	fnv     uint64
}

const decodeLogsDir = "testdata/decode-logs"

func readDecodeLogManifest(t *testing.T) []decodeLogCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(decodeLogsDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Libopus string          `json:"libopus"`
		Cases   []decodeLogCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Libopus != "libopus 1.5.2" || len(manifest.Cases) == 0 {
		t.Fatal("unexpected decode-logs manifest")
	}
	return manifest.Cases
}

// decodeLogRecords builds the packet list of a case as decode-logs.sh does.
func decodeLogRecords(t *testing.T, c decodeLogCase) []decodeLogRecord {
	t.Helper()
	var recs []decodeLogRecord
	packets := func(fixture string) [][]byte {
		return readContainerStream(t, filepath.Join("..", "testdata", fixture)).packets
	}
	switch {
	case c.Source.Fixture != "":
		for _, p := range packets(c.Source.Fixture) {
			recs = append(recs, decodeLogRecord{packet: p})
		}
	case c.Source.Vector != "":
		raw, err := os.ReadFile(filepath.Join("testdata", c.Source.Vector+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Records []struct {
				Missing   bool   `json:"missing"`
				PacketHex string `json:"packet_hex"`
				FrameSize int    `json:"frame_size"`
				Result    int    `json:"result"`
			} `json:"records"`
		}
		if err := json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		for _, r := range vector.Records {
			switch {
			case r.Result < 0:
				// libopus rejects it, leaving its state unchanged.
			case r.Missing:
				if r.FrameSize <= 0 {
					t.Fatal("lost record without a duration")
				}
				recs = append(recs, decodeLogRecord{lost: true, duration: r.FrameSize})
			default:
				p, err := hex.DecodeString(r.PacketHex)
				if err != nil {
					t.Fatal(err)
				}
				recs = append(recs, decodeLogRecord{packet: p})
			}
		}
	case c.Source.Splice != "":
		for _, s := range c.Source.Segments {
			all := packets(s.Fixture)
			if s.First < 0 || s.Count <= 0 || s.First+s.Count > len(all) {
				t.Fatalf("splice segment %+v outside %s", s, s.Fixture)
			}
			for _, p := range all[s.First : s.First+s.Count] {
				recs = append(recs, decodeLogRecord{packet: p})
			}
		}
	default:
		t.Fatalf("case %s has no source", c.Name)
	}
	for _, field := range strings.Split(c.Drop, ",") {
		if field == "" {
			continue
		}
		first, last, ok := strings.Cut(field, "-")
		a, err := strconv.Atoi(first)
		if err != nil {
			t.Fatal(err)
		}
		b := a
		if ok {
			if b, err = strconv.Atoi(last); err != nil {
				t.Fatal(err)
			}
		}
		for i := a; i <= b; i++ {
			if i >= len(recs) || recs[i].lost {
				t.Fatalf("dropped record %d is not a packet", i)
			}
			parsed, err := Parse(recs[i].packet, false)
			if err != nil {
				t.Fatal(err)
			}
			recs[i].lost = true
			recs[i].duration = parsed.Duration48()
		}
	}
	return recs
}

// readDecodeLog reads a case's log after checking its SHA-256.
func readDecodeLog(t *testing.T, c decodeLogCase) []decodeLogLine {
	t.Helper()
	raw := readPinnedFile(t, filepath.Join(decodeLogsDir, c.Name+".txt"), c.LogSHA256)
	var lines []decodeLogLine
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		text := scanner.Text()
		if strings.HasPrefix(text, "#") {
			continue
		}
		var (
			index int
			l     decodeLogLine
		)
		if _, err := fmt.Sscanf(text, "%d %s %d %d %x %x", &index, &l.kind, &l.bytes, &l.samples, &l.rng, &l.fnv); err != nil || index != len(lines) {
			t.Fatalf("%s: bad log line %q", c.Name, text)
		}
		lines = append(lines, l)
	}
	return lines
}

// fnv1a64 is the 64-bit FNV-1a hash of PCM as float32 little-endian bytes,
// as decode_float's log computes it.
func fnv1a64(pcm []float32) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, v := range pcm {
		u := math.Float32bits(v)
		for b := 0; b < 4; b++ {
			h ^= uint64(u>>(8*b)) & 0xff
			h *= 0x100000001b3
		}
	}
	return h
}

// TestDecodeLogs replays every case of testdata/decode-logs, the record by
// record decode of libopus 1.5.2's generic build: the SILK, hybrid and
// SILK/CELT fixtures without loss, and fixtures, sequences and a splice that
// switches modes, bandwidths, frame durations and coded channels with
// records lost. A lost record is concealed with Conceal, or, where the case's
// policy says libopus decoded it with decode_fec = 1, decoded with
// DecodeFEC from the next record: "lbrr" when HasLBRR reports in-band FEC in
// the next packet (what the puropus Decoder does), "always" whenever the
// next record is a packet. Every record must have libopus's kind, sample
// count and final range, and its PCM must equal libopus's bit for bit (see
// Exact for other architectures); the whole decode, with the case's pre-skip
// and end trimming, must have the SHA-256 that the root package's tests
// compare against.
func TestDecodeLogs(t *testing.T) {
	cases := readDecodeLogManifest(t)
	var total, bitExact, recovered, fromLBRR, fecCases int
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			recs := decodeLogRecords(t, c)
			log := readDecodeLog(t, c)
			if len(recs) != c.Records || len(log) != c.Records {
				t.Fatalf("%d records, %d log lines, manifest %d", len(recs), len(log), c.Records)
			}
			d, err := NewDecoder(c.Channels)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]float32, MaxPacketSamples*c.Channels)
			var pcm []float32
			lost, fec, exact := 0, 0, 0
			for i, r := range recs {
				var n int
				kind := "packet"
				switch {
				case r.lost:
					next := i+1 < len(recs) && !recs[i+1].lost
					if next && (c.FEC == "always" || c.FEC == "lbrr" && HasLBRR(recs[i+1].packet)) {
						kind = "fec"
						n, err = d.DecodeFEC(recs[i+1].packet, r.duration, out)
						fec++
					} else {
						kind = "lost"
						n, err = d.Conceal(r.duration, out)
						lost++
					}
				default:
					n, err = d.Decode(r.packet, out)
				}
				if err != nil {
					t.Fatalf("record %d (%s): %v", i, kind, err)
				}
				want := log[i]
				got := decodeLogLine{kind: kind, bytes: len(r.packet), samples: n, rng: d.FinalRange(), fnv: fnv1a64(out[:n*c.Channels])}
				if got.fnv == want.fnv {
					exact++
				} else if !c.Exact() {
					got.fnv = want.fnv
				}
				if got != want {
					t.Fatalf("record %d: got %s %d bytes, %d samples, range %08x, PCM hash %016x; libopus %s %d bytes, %d samples, range %08x, PCM hash %016x",
						i, got.kind, got.bytes, got.samples, got.rng, got.fnv, want.kind, want.bytes, want.samples, want.rng, want.fnv)
				}
				if kind == "fec" && want.rng != 0 {
					fromLBRR++
				}
				pcm = append(pcm, out[:n*c.Channels]...)
			}
			if lost != c.LostRecords || fec != c.FECRecords {
				t.Fatalf("%d lost and %d FEC records, manifest %d and %d", lost, fec, c.LostRecords, c.FECRecords)
			}
			if c.Skip+c.Trim > len(pcm)/c.Channels {
				t.Fatal("pre-skip and trimming exceed the decode")
			}
			trimmed := pcm[c.Skip*c.Channels : len(pcm)-c.Trim*c.Channels]
			raw := make([]byte, 4*len(trimmed))
			for i, v := range trimmed {
				binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(v))
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != c.PCMSHA256 && c.Exact() {
				t.Fatalf("PCM SHA-256 %s, manifest %s", got, c.PCMSHA256)
			}
			total += len(recs)
			bitExact += exact
			recovered += fec
			if c.FEC != "off" {
				fecCases++
			}
			t.Logf("%d of %d records bit-exact, %d concealed, %d decoded with FEC", exact, len(recs), lost, fec)
		})
	}
	t.Logf("%d cases, %d with losses: %d of %d records bit-exact, %d decoded with FEC, %d of them from LBRR data",
		len(cases), fecCases, bitExact, total, recovered, fromLBRR)
	if fecCases < 25 || recovered < 200 || fromLBRR < 150 {
		t.Fatal("the decode logs no longer cover FEC")
	}
}
