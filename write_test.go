package puropus

import (
	"bytes"
	"errors"
	"testing"
)

func TestWriteOggAndWebMRoundTripThroughDecoder(t *testing.T) {
	for _, name := range fixtures {
		data := readFixture(t, name)
		src := sourceFor(t, name)
		direct, _ := decodeAll(t, bytes.NewReader(data), DecodeOptions{})
		out, err := Cut(src, Edit{Keep: []Interval{{0, src.Duration()}}})
		if err != nil {
			t.Fatal(err)
		}
		if out.Header.PreSkip != src.Header.PreSkip {
			t.Fatalf("%s: whole-stream cut has pre-skip %d, source %d", name, out.Header.PreSkip, src.Header.PreSkip)
		}
		for _, format := range []Format{FormatOgg, FormatWebM} {
			var buf bytes.Buffer
			if err := out.Write(&buf, format); err != nil {
				t.Fatal(err)
			}
			re, err := ReadPackets(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("%s as %v: %v", name, format, err)
			}
			if re.Header.Format != format || re.Header.PreSkip != src.Header.PreSkip || re.Header.OutputGain != src.Header.OutputGain {
				t.Fatalf("%s as %v: header %+v", name, format, re.Header)
			}
			if len(re.Packets) != len(src.Packets) {
				t.Fatalf("%s as %v: %d packets, want %d", name, format, len(re.Packets), len(src.Packets))
			}
			for i := range re.Packets {
				if a, b := re.Packets[i], src.Packets[i]; !bytes.Equal(a.Data, b.Data) || a.Start != b.Start || a.Duration != b.Duration || a.Skip != b.Skip || a.Trim != b.Trim {
					t.Fatalf("%s as %v: packet %d is %+v, want %+v", name, format, i, a, b)
				}
			}
			// The written file decodes to exactly the source's audio: the
			// pre-skip and the end trimming survive the round trip.
			got, _ := decodeAll(t, bytes.NewReader(buf.Bytes()), DecodeOptions{})
			if len(got) != len(direct) {
				t.Fatalf("%s as %v: decodes to %d samples, source %d", name, format, len(got), len(direct))
			}
			for i := range got {
				if got[i] != direct[i] {
					t.Fatalf("%s as %v: sample %d differs", name, format, i)
				}
			}
		}
	}
}

// A Stream with no packets has no valid Ogg or WebM form (this package could
// not even read the WebM one back), so writing it is an error.
func TestWriteRefusesEmptyStream(t *testing.T) {
	src := sourceFor(t, "ogg-celt-fb.opus")
	out, err := Cut(src, Edit{Keep: []Interval{{10000 * ms, 11000 * ms}}}) // past the end
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packets) != 0 {
		t.Fatalf("expected an empty selection, got %d packets", len(out.Packets))
	}
	for _, format := range []Format{FormatOgg, FormatWebM} {
		var buf bytes.Buffer
		err := out.Write(&buf, format)
		if !errors.Is(err, ErrEmptyStream) {
			t.Fatalf("%v: Write of an empty stream returned %v, want ErrEmptyStream", format, err)
		}
	}
}
