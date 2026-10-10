package opus

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestParseMatchesLibopus checks Parse and HasLBRR against libopus 1.5.2 on
// the packets of opus/testdata/packet-parse/libopus.txt, written by
// tools/libopus-ref/packet_ref.c: every TOC byte, built packets of every
// framing code (well formed, truncated, extended and corrupted) and random
// bytes, each parsed with and without self-delimited framing.
func TestParseMatchesLibopus(t *testing.T) {
	raw, err := os.ReadFile("testdata/packet-parse/libopus.txt")
	if err != nil {
		t.Fatal(err)
	}
	var (
		buf                                []byte
		first                              Packet
		firstErr                           error
		tocs, packets, valid, invalid, lbr int
	)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for line := 1; sc.Scan(); line++ {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		nums := make([]int, 0, len(fields))
		for _, s := range fields[1:] {
			if v, err := strconv.Atoi(s); err == nil {
				nums = append(nums, v)
			}
		}
		switch fields[0] {
		case "t":
			toc := byte(nums[0])
			if got, want := tocBandwidth(toc), Bandwidth(nums[1]-1101); got != want {
				t.Errorf("TOC %#02x: bandwidth %d, libopus %d", toc, got, want)
			}
			if got := tocSamplesPerFrame(toc); got != nums[2] {
				t.Errorf("TOC %#02x: %d samples per frame, libopus %d", toc, got, nums[2])
			}
			p, err := Parse([]byte{toc &^ 3}, false)
			if err != nil || p.Stereo != (nums[3] == 2) {
				t.Errorf("TOC %#02x: stereo %v (%v), libopus %d channels", toc, p.Stereo, err, nums[3])
			}
			tocs++
		case "p":
			n := nums[0]
			prefix := ""
			if len(fields) > 2 {
				prefix = fields[2]
			}
			b, err := hex.DecodeString(prefix)
			if err != nil {
				t.Fatalf("line %d: %v", line, err)
			}
			buf = make([]byte, n)
			copy(buf, b)
			packets++
		case "r":
			sd, ret := nums[0] == 1, nums[1]
			p, err := Parse(buf, sd)
			if !sd {
				first, firstErr = p, err
			}
			if ret < 0 {
				if err == nil {
					t.Fatalf("line %d: accepted %x (self-delimited %v), libopus error %d", line, buf, sd, ret)
				}
				invalid++
				continue
			}
			if err != nil {
				t.Fatalf("line %d: %v for %x (self-delimited %v), libopus parses %d frames", line, err, buf, sd, ret)
			}
			checkParsed(t, line, buf, p, nums[1:])
			valid++
		case "l":
			if firstErr != nil || first.FrameSize[0] <= 1 {
				// libopus reports errors as negative values and reads the
				// first byte of a frame of 0 or 1 bytes, which decoding
				// treats as lost; HasLBRR is false for both.
				if HasLBRR(buf) {
					t.Fatalf("line %d: HasLBRR true for %x", line, buf)
				}
				continue
			}
			if got := HasLBRR(buf); got != (nums[0] == 1) {
				t.Fatalf("line %d: HasLBRR %v for %x, libopus %d", line, got, buf, nums[0])
			}
			lbr++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if tocs != 256 || packets < 3000 || valid < 3000 || invalid < 3000 || lbr < 1000 {
		t.Fatalf("incomplete vectors: %d TOCs, %d packets, %d parsed, %d rejected, %d LBRR checks", tocs, packets, valid, invalid, lbr)
	}
	t.Logf("%d packets: %d parses match, %d rejections match, %d LBRR flags match", packets, valid, invalid, lbr)
}

// checkParsed compares a parsed packet with libopus's frame count, payload
// offset, packet offset, padding length and frame sizes.
func checkParsed(t *testing.T, line int, buf []byte, p Packet, want []int) {
	t.Helper()
	count, payload, packetOffset, sizes := want[0], want[1], want[2], want[4:]
	if p.FrameCount != count || len(sizes) != count {
		t.Fatalf("line %d: %d frames, libopus %d", line, p.FrameCount, count)
	}
	toc := buf[0]
	vbr := toc&3 == 2 || toc&3 == 3 && buf[1]&0x80 != 0
	if p.Code != int(toc&3) || p.Config != int(toc>>3) || p.Stereo != (toc&4 != 0) || p.VBR != vbr ||
		p.Mode != tocMode(toc) || p.Bandwidth != tocBandwidth(toc) || p.FrameDuration != tocSamplesPerFrame(toc) {
		t.Fatalf("line %d: TOC fields %+v of %x", line, p, buf[:min(len(buf), 2)])
	}
	at, total := payload, 0
	for i, size := range sizes {
		if p.FrameOffset[i] != at || p.FrameSize[i] != size {
			t.Fatalf("line %d: frame %d at %d size %d, libopus at %d size %d", line, i, p.FrameOffset[i], p.FrameSize[i], at, size)
		}
		at += size
		total += size
	}
	if p.PacketSize != packetOffset || p.DataSize != total {
		t.Fatalf("line %d: packet size %d data size %d, libopus %d and %d", line, p.PacketSize, p.DataSize, packetOffset, total)
	}
}

// TestTOCConfigurations checks the mode, bandwidth and frame duration of
// every configuration against the table in RFC 6716 §3.1.
func TestTOCConfigurations(t *testing.T) {
	type entry struct {
		mode      Mode
		bandwidth Bandwidth
		durations []int // in 1/10 ms
	}
	table := []entry{
		{ModeSILK, BandwidthNarrowband, []int{100, 200, 400, 600}},
		{ModeSILK, BandwidthMediumband, []int{100, 200, 400, 600}},
		{ModeSILK, BandwidthWideband, []int{100, 200, 400, 600}},
		{ModeHybrid, BandwidthSuperWideband, []int{100, 200}},
		{ModeHybrid, BandwidthFullband, []int{100, 200}},
		{ModeCELT, BandwidthNarrowband, []int{25, 50, 100, 200}},
		{ModeCELT, BandwidthWideband, []int{25, 50, 100, 200}},
		{ModeCELT, BandwidthSuperWideband, []int{25, 50, 100, 200}},
		{ModeCELT, BandwidthFullband, []int{25, 50, 100, 200}},
	}
	config := 0
	for _, e := range table {
		for _, d := range e.durations {
			for _, stereo := range []byte{0, 4} {
				p, err := Parse([]byte{byte(config<<3) | stereo, 0xaa}, false)
				if err != nil {
					t.Fatal(err)
				}
				if p.Config != config || p.Mode != e.mode || p.Bandwidth != e.bandwidth ||
					p.FrameDuration != d*48/10 || p.Duration48() != d*48/10 || p.Stereo != (stereo != 0) {
					t.Errorf("config %d: %+v, want mode %d bandwidth %d duration %d", config, p, e.mode, e.bandwidth, d*48/10)
				}
			}
			config++
		}
	}
	if config != 32 {
		t.Fatalf("table covers %d configurations", config)
	}
}

// FuzzParse checks that Parse never panics and that every frame it reports
// lies inside the packet.
func FuzzParse(f *testing.F) {
	for _, seed := range [][]byte{{0x00, 0xaa}, {0x01, 1, 2}, {0x02, 1, 1, 2}, {0x03, 0x42, 1, 1, 2, 0}, {0x4b, 0x83, 1, 2, 1, 2, 3, 4}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, sd := range []bool{false, true} {
			p, err := Parse(data, sd)
			if err != nil {
				continue
			}
			if p.FrameCount < 1 || p.FrameCount > MaxFrames || p.Duration48() > MaxPacketDuration48 || p.PacketSize > len(data) {
				t.Fatalf("%x: %+v", data, p)
			}
			for i := 0; i < p.FrameCount; i++ {
				if p.FrameSize[i] < 0 || p.FrameSize[i] > MaxFrameSize || p.FrameOffset[i]+p.FrameSize[i] > p.PacketSize {
					t.Fatalf("%x: frame %d at %d size %d", data, i, p.FrameOffset[i], p.FrameSize[i])
				}
			}
			_ = HasLBRR(data)
		}
	})
}
