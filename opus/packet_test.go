package opus

import "testing"

func TestParseCode0OneFrame(t *testing.T) {
	packet, err := Parse([]byte{0x00, 0xaa, 0xbb}, false)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Code != 0 || packet.FrameCount != 1 {
		t.Fatalf("code=%d frame_count=%d", packet.Code, packet.FrameCount)
	}
	if packet.FrameOffset[0] != 1 || packet.FrameSize[0] != 2 {
		t.Fatalf("frame offset/size=%d/%d", packet.FrameOffset[0], packet.FrameSize[0])
	}
	if packet.Duration48() != 480 {
		t.Fatalf("duration=%d, want 480", packet.Duration48())
	}
	if packet.Mode != ModeSILK || packet.Bandwidth != BandwidthNarrowband {
		t.Fatalf("mode/bandwidth=%d/%d", packet.Mode, packet.Bandwidth)
	}
}

func TestParseCode1TwoEqualFrames(t *testing.T) {
	packet, err := Parse([]byte{0x01, 0xaa, 0xbb, 0xcc, 0xdd}, false)
	if err != nil {
		t.Fatal(err)
	}
	if packet.FrameCount != 2 || packet.VBR {
		t.Fatalf("frame_count=%d vbr=%v", packet.FrameCount, packet.VBR)
	}
	if packet.FrameSize[0] != 2 || packet.FrameSize[1] != 2 {
		t.Fatalf("frame sizes=%v", packet.FrameSize)
	}
	if packet.Duration48() != 960 {
		t.Fatalf("duration=%d, want 960", packet.Duration48())
	}
}

func TestParseCode2TwoDifferentFrames(t *testing.T) {
	packet, err := Parse([]byte{0x02, 0x02, 0xaa, 0xbb, 0xcc}, false)
	if err != nil {
		t.Fatal(err)
	}
	if packet.FrameCount != 2 || !packet.VBR {
		t.Fatalf("frame_count=%d vbr=%v", packet.FrameCount, packet.VBR)
	}
	if packet.FrameOffset[0] != 2 || packet.FrameOffset[1] != 4 {
		t.Fatalf("frame offsets=%v", packet.FrameOffset)
	}
	if packet.FrameSize[0] != 2 || packet.FrameSize[1] != 1 {
		t.Fatalf("frame sizes=%v", packet.FrameSize)
	}
}

func TestParseCode3CBRWithPadding(t *testing.T) {
	packet, err := Parse([]byte{0x03, 0x42, 0x01, 0xaa, 0xbb, 0xcc, 0xdd, 0x00}, false)
	if err != nil {
		t.Fatal(err)
	}
	if packet.FrameCount != 2 || packet.VBR {
		t.Fatalf("frame_count=%d vbr=%v", packet.FrameCount, packet.VBR)
	}
	if packet.FrameSize[0] != 2 || packet.FrameSize[1] != 2 {
		t.Fatalf("frame sizes=%v", packet.FrameSize)
	}
}

func TestParseCode3VBR(t *testing.T) {
	packet, err := Parse([]byte{0x03, 0x83, 0x01, 0x02, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, false)
	if err != nil {
		t.Fatal(err)
	}
	if packet.FrameCount != 3 || !packet.VBR {
		t.Fatalf("frame_count=%d vbr=%v", packet.FrameCount, packet.VBR)
	}
	wantSizes := []int{1, 2, 3}
	for i, want := range wantSizes {
		if packet.FrameSize[i] != want {
			t.Fatalf("frame size[%d]=%d, want %d", i, packet.FrameSize[i], want)
		}
	}
}

func TestParseRejectsMalformedPackets(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "code1 odd payload", data: []byte{0x01, 0xaa}},
		{name: "code2 missing length", data: []byte{0x02}},
		{name: "code2 overrun", data: []byte{0x02, 0x03, 0xaa}},
		{name: "code3 missing count", data: []byte{0x03}},
		{name: "code3 zero frames", data: []byte{0x03, 0x00}},
		{name: "code3 duration over 120ms", data: []byte{0x1b, 0x03, 0xaa, 0xbb, 0xcc}},
		{name: "code3 padding overrun", data: []byte{0x03, 0x41, 0x04, 0xaa}},
		{name: "code0 frame over 1275 bytes", data: make([]byte, 1+1276)},
		{name: "code1 frames over 1275 bytes", data: append([]byte{0x01}, make([]byte, 2*1276)...)},
		{name: "code2 second frame over 1275 bytes", data: append([]byte{0x02, 0x01, 0xaa}, make([]byte, 1276)...)},
		{name: "code3 CBR frames over 1275 bytes", data: append([]byte{0x03, 0x02}, make([]byte, 2*1276)...)},
		{name: "code3 VBR last frame over 1275 bytes", data: append([]byte{0x03, 0x82, 0x01, 0xaa}, make([]byte, 1276)...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.data, false); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseSelfDelimitedCode0(t *testing.T) {
	packet, err := Parse([]byte{0x00, 0x02, 0xaa, 0xbb, 0xcc}, true)
	if err != nil {
		t.Fatal(err)
	}
	if packet.PacketSize != 4 {
		t.Fatalf("packet_size=%d, want 4", packet.PacketSize)
	}
	if packet.FrameSize[0] != 2 {
		t.Fatalf("frame size=%d, want 2", packet.FrameSize[0])
	}
}
