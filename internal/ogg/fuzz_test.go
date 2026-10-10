package ogg

import (
	"bytes"
	"testing"
)

// FuzzReader feeds arbitrary bytes to the page reader: it must return an
// error or io.EOF, never panic or loop forever, and a reader with
// ReusePacketData must return the same packets and error.
func FuzzReader(f *testing.F) {
	f.Add(singlePacketStream(f, 1, []byte("OpusHead")))
	f.Add([]byte("OggS"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewReader(bytes.NewReader(data))
		reuse := NewReader(bytes.NewReader(data))
		reuse.ReusePacketData()
		for i := 0; i < 10000; i++ {
			p, err := r.Next()
			q, err2 := reuse.Next()
			if (err == nil) != (err2 == nil) || err != nil && err.Error() != err2.Error() {
				t.Fatalf("packet %d: error %v, with ReusePacketData %v", i, err, err2)
			}
			if err != nil {
				return
			}
			if !bytes.Equal(p.Data, q.Data) || p.Granule != q.Granule || p.GranuleKnown != q.GranuleKnown ||
				p.BOS != q.BOS || p.EOS != q.EOS || p.Serial != q.Serial {
				t.Fatalf("packet %d differs with ReusePacketData", i)
			}
		}
	})
}
