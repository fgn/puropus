package opus

import "testing"

// Packets for the parser benchmarks: one SILK frame, one CELT frame, and a
// code 3 packet of two 78-byte CBR SILK frames.
var (
	benchSILKPacket  = append([]byte{0x48}, make([]byte, 78)...)
	benchCELTPacket  = append([]byte{0xb8}, make([]byte, 126)...)
	benchCode3Packet = append([]byte{0x4b, 0x02}, make([]byte, 2*78)...)
)

func BenchmarkOpusPacketParseSingleFrameSILK(b *testing.B) {
	benchmarkParsePacket(b, benchSILKPacket)
}

func BenchmarkOpusPacketParseSingleFrameCELT(b *testing.B) {
	benchmarkParsePacket(b, benchCELTPacket)
}

func BenchmarkOpusPacketParseCode3(b *testing.B) {
	benchmarkParsePacket(b, benchCode3Packet)
}

func benchmarkParsePacket(b *testing.B, packet []byte) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(packet)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parsed, err := Parse(packet, false)
		if err != nil {
			b.Fatal(err)
		}
		if parsed.FrameCount == 0 {
			b.Fatal("parsed packet has no frames")
		}
	}
}
