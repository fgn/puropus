package webm

import (
	"bytes"
	"testing"
)

// FuzzReader feeds arbitrary bytes to the EBML reader: it must return an
// error or io.EOF, never panic or loop forever, and a reader with
// ReuseBlockData must return the same blocks and error.
func FuzzReader(f *testing.F) {
	f.Add(buildHandmade())
	f.Add([]byte{0x1A, 0x45, 0xDF, 0xA3})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		reuse, err := NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		reuse.ReuseBlockData()
		for i := 0; i < 10000; i++ {
			b, err := r.Next()
			c, err2 := reuse.Next()
			if (err == nil) != (err2 == nil) || err != nil && err.Error() != err2.Error() {
				t.Fatalf("block %d: error %v, with ReuseBlockData %v", i, err, err2)
			}
			if err != nil {
				return
			}
			if !bytes.Equal(b.Data, c.Data) || b.Timestamp != c.Timestamp || b.DiscardPadding != c.DiscardPadding {
				t.Fatalf("block %d differs with ReuseBlockData", i)
			}
		}
	})
}
