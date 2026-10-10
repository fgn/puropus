package entcode_test

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/fgn/puropus/opus/internal/entcode"
)

// referenceStep is one decoder operation of testdata/entdec.txt with
// libopus's result and the decoder state after it.
type referenceStep struct {
	line     int
	op       string
	a, b, c  uint32
	result   uint32
	tell     int
	tellFrac int
	rng      uint32
	err      bool
}

type referenceCase struct {
	kind  string
	data  []byte
	steps []referenceStep
}

type referenceVectors struct {
	icdf8  map[uint32][]uint8
	icdf16 map[uint32][]uint16
	cases  []referenceCase
}

func parseUint32(t *testing.T, line int, s string) uint32 {
	t.Helper()
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		t.Fatalf("line %d: %v", line, err)
	}
	return uint32(v)
}

// readReference parses the vectors written by
// tools/libopus-ref/entcode_ref.c; the format is documented there.
func readReference(t *testing.T) referenceVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/entdec.txt")
	if err != nil {
		t.Fatal(err)
	}
	ref := referenceVectors{icdf8: map[uint32][]uint8{}, icdf16: map[uint32][]uint16{}}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for line := 1; sc.Scan(); line++ {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "t8", "t16":
			id := parseUint32(t, line, fields[1])
			n := int(parseUint32(t, line, fields[3]))
			if len(fields) != 4+n {
				t.Fatalf("line %d: table has %d values, want %d", line, len(fields)-4, n)
			}
			for _, s := range fields[4:] {
				v := parseUint32(t, line, s)
				if fields[0] == "t8" {
					ref.icdf8[id] = append(ref.icdf8[id], uint8(v))
				} else {
					ref.icdf16[id] = append(ref.icdf16[id], uint16(v))
				}
			}
		case "case":
			if len(fields) != 4 {
				t.Fatalf("line %d: malformed case", line)
			}
			c := referenceCase{kind: fields[1]}
			if fields[3] != "-" {
				c.data, err = hex.DecodeString(fields[3])
				if err != nil {
					t.Fatalf("line %d: %v", line, err)
				}
			}
			if len(c.data) != int(parseUint32(t, line, fields[2])) {
				t.Fatalf("line %d: storage does not match the data", line)
			}
			ref.cases = append(ref.cases, c)
		default:
			if len(fields) != 9 || len(ref.cases) == 0 {
				t.Fatalf("line %d: malformed step", line)
			}
			s := referenceStep{line: line, op: fields[0]}
			s.a = parseUint32(t, line, fields[1])
			s.b = parseUint32(t, line, fields[2])
			s.c = parseUint32(t, line, fields[3])
			s.result = parseUint32(t, line, fields[4])
			s.tell = int(parseUint32(t, line, fields[5]))
			s.tellFrac = int(parseUint32(t, line, fields[6]))
			s.rng = parseUint32(t, line, fields[7])
			s.err = parseUint32(t, line, fields[8]) != 0
			last := &ref.cases[len(ref.cases)-1]
			last.steps = append(last.steps, s)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return ref
}

// TestDecoderMatchesLibopus replays every operation of the libopus vectors
// and requires the same decoded value, Tell, TellFrac, Rng and error flag
// after each one: encoded sequences of every symbol kind, the same decoded
// from truncated buffers and past their end, and random bytes with random
// operations, including SetStorage and SetTell.
func TestDecoderMatchesLibopus(t *testing.T) {
	ref := readReference(t)
	kinds := map[string]int{}
	ops := map[string]int{}
	for _, c := range ref.cases {
		kinds[c.kind]++
		// Bytes beyond the frame must never be read.
		buf := append(append([]byte(nil), c.data...), 0x5a, 0xa5, 0xff)[:len(c.data)]
		var d entcode.Decoder
		for _, s := range c.steps {
			ops[s.op]++
			var got uint32
			switch s.op {
			case "init":
				d.Init(buf)
			case "dec":
				got = d.Decode(s.a)
				d.DecUpdate(s.b, s.c, s.a)
			case "bin":
				got = d.DecodeBin(uint(s.a))
				d.DecUpdate(s.b, s.c, 1<<s.a)
			case "logp":
				got = uint32(d.DecBitLogp(uint(s.a)))
			case "icdf":
				got = uint32(d.DecICDF(ref.icdf8[s.a], uint(s.b)))
			case "icdf16":
				got = uint32(d.DecICDF16(ref.icdf16[s.a], uint(s.b)))
			case "uint":
				got = d.DecUint(s.a)
			case "bits":
				got = d.DecBits(uint(s.a))
			case "shrink":
				d.SetStorage(d.Storage() - int(s.a))
				got = uint32(d.Storage())
			case "settell":
				d.SetTell(int(s.a))
			default:
				t.Fatalf("line %d: unknown operation %q", s.line, s.op)
			}
			if got != s.result || d.Tell() != s.tell || d.TellFrac() != s.tellFrac || d.Rng() != s.rng || d.HasError() != s.err {
				t.Fatalf("line %d: %s(%d, %d, %d) = %d, tell %d, tell_frac %d, rng %d, error %v; libopus: %d, %d, %d, %d, %v",
					s.line, s.op, s.a, s.b, s.c, got, d.Tell(), d.TellFrac(), d.Rng(), d.HasError(),
					s.result, s.tell, s.tellFrac, s.rng, s.err)
			}
		}
		if !bytes.Equal(buf, c.data) {
			t.Fatal("the decoder modified its input")
		}
	}
	// Guard the vectors themselves: every kind of case and operation, in
	// numbers.
	for kind, least := range map[string]int{"valid": 50, "truncated": 10, "random": 30} {
		if kinds[kind] < least {
			t.Errorf("%d %s cases, want at least %d", kinds[kind], kind, least)
		}
	}
	for _, op := range []string{"dec", "bin", "logp", "icdf", "icdf16", "uint", "bits"} {
		if ops[op] < 500 {
			t.Errorf("%d %s operations, want at least 500", ops[op], op)
		}
	}
	for _, op := range []string{"shrink", "settell"} {
		if ops[op] < 20 {
			t.Errorf("%d %s operations, want at least 20", ops[op], op)
		}
	}
	t.Logf("%d cases %v, operations %v", len(ref.cases), kinds, ops)
}

// TestDecICDF2MatchesDecICDF decodes random bytes with random two-entry
// tables through DecICDF2 and through DecICDF, which the libopus vectors
// check, interleaved with other symbols, and requires the same symbols and
// state.
func TestDecICDF2MatchesDecICDF(t *testing.T) {
	r := rand.New(rand.NewSource(17))
	for range 2000 {
		buf := make([]byte, 1+r.Intn(64))
		r.Read(buf)
		var a, b entcode.Decoder
		a.Init(buf)
		b.Init(buf)
		for i := range 400 {
			if i%5 == 4 {
				ft := uint32(2 + r.Intn(300))
				if x, y := a.DecUint(ft), b.DecUint(ft); x != y {
					t.Fatal("DecUint diverged")
				}
				continue
			}
			icdf0 := uint8(r.Intn(256))
			ftb := uint(8)
			if r.Intn(4) == 0 {
				ftb = uint(1 + r.Intn(8))
				icdf0 &= uint8(1<<ftb - 1)
			}
			x := a.DecICDF2(icdf0, ftb)
			y := b.DecICDF([]uint8{icdf0, 0}, ftb)
			if x != y || a.Rng() != b.Rng() || a.Tell() != b.Tell() || a.TellFrac() != b.TellFrac() {
				t.Fatalf("symbol %d: DecICDF2 %d rng %#x, DecICDF %d rng %#x", i, x, a.Rng(), y, b.Rng())
			}
		}
	}
}

// TestDecUintMatchesDecodeUpdate decodes random bytes with DecUint and with
// the steps of ec_dec_uint (Decode, DecUpdate, DecBits), which the libopus
// vectors check, for every alphabet size up to 2^9 and others, and requires
// the same values and state.
func TestDecUintMatchesDecodeUpdate(t *testing.T) {
	r := rand.New(rand.NewSource(19))
	decUint := func(d *entcode.Decoder, ft uint32) uint32 {
		ft--
		ftb := entcode.ILog(ft)
		if ftb > 8 {
			ftb -= 8
			top := ft>>uint(ftb) + 1
			s := d.Decode(top)
			d.DecUpdate(s, s+1, top)
			v := s<<uint(ftb) | d.DecBits(uint(ftb))
			if v <= ft {
				return v
			}
			return ft
		}
		s := d.Decode(ft + 1)
		d.DecUpdate(s, s+1, ft+1)
		return s
	}
	for iter := range 4000 {
		buf := make([]byte, 1+r.Intn(64))
		r.Read(buf)
		var a, b entcode.Decoder
		a.Init(buf)
		b.Init(buf)
		for i := range 200 {
			ft := uint32(2 + (iter*200+i)%511)
			if i%3 == 0 {
				ft = 2 + uint32(r.Int63n(1<<32-2))
			}
			if x, y := a.DecUint(ft), decUint(&b, ft); x != y || a.Rng() != b.Rng() || a.Tell() != b.Tell() || a.TellFrac() != b.TellFrac() {
				t.Fatalf("ft %d: DecUint %d rng %#x, steps %d rng %#x", ft, x, a.Rng(), y, b.Rng())
			}
		}
	}
}

func TestILog(t *testing.T) {
	for _, tc := range []struct {
		v    uint32
		want int
	}{{0, 0}, {1, 1}, {2, 2}, {3, 2}, {255, 8}, {256, 9}, {1 << 31, 32}, {0xffffffff, 32}} {
		if got := entcode.ILog(tc.v); got != tc.want {
			t.Errorf("ILog(%d) = %d, want %d", tc.v, got, tc.want)
		}
	}
}

// icdfTable is a SILK-like 8-bit inverse CDF.
var icdfTable = []uint8{224, 192, 160, 128, 96, 64, 32, 0}

// decodeMix decodes a fixed mix of symbol kinds from d until it has read
// the whole frame.
func decodeMix(d *entcode.Decoder) uint32 {
	var sum uint32
	for d.Tell() < 8*d.Storage() {
		sum += uint32(d.DecICDF(icdfTable, 8))
		sum += uint32(d.DecBitLogp(3))
		sum += d.DecUint(100)
		sum += d.DecBits(5)
		fl := d.DecodeBin(15)
		d.DecUpdate(fl, fl+1, 1<<15)
	}
	return sum
}

func TestDecoderDoesNotAllocate(t *testing.T) {
	data := make([]byte, 1275)
	rand.New(rand.NewSource(1)).Read(data)
	var d entcode.Decoder
	allocs := testing.AllocsPerRun(100, func() {
		d.Init(data)
		decodeMix(&d)
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per frame", allocs)
	}
}

func benchmarkFrame(b *testing.B) []byte {
	b.Helper()
	data := make([]byte, 160)
	rand.New(rand.NewSource(2)).Read(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	return data
}

func BenchmarkDecICDF(b *testing.B) {
	data := benchmarkFrame(b)
	var d entcode.Decoder
	for b.Loop() {
		d.Init(data)
		for d.Tell() < 8*len(data) {
			d.DecICDF(icdfTable, 8)
		}
	}
}

func BenchmarkDecBitLogp(b *testing.B) {
	data := benchmarkFrame(b)
	var d entcode.Decoder
	for b.Loop() {
		d.Init(data)
		for d.Tell() < 8*len(data) {
			d.DecBitLogp(1)
		}
	}
}

func BenchmarkDecodeBinUpdate(b *testing.B) {
	data := benchmarkFrame(b)
	var d entcode.Decoder
	for b.Loop() {
		d.Init(data)
		for d.Tell() < 8*len(data) {
			fl := d.DecodeBin(15)
			d.DecUpdate(fl, fl+1, 1<<15)
		}
	}
}

func BenchmarkDecUint(b *testing.B) {
	data := benchmarkFrame(b)
	var d entcode.Decoder
	for b.Loop() {
		d.Init(data)
		for d.Tell() < 8*len(data) {
			d.DecUint(1 << 20)
		}
	}
}

func BenchmarkDecodeMix(b *testing.B) {
	data := benchmarkFrame(b)
	var d entcode.Decoder
	for b.Loop() {
		d.Init(data)
		decodeMix(&d)
	}
}
