package opus

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// officialVectors are the RFC 8251 test vectors: name, SHA-256, packets and
// samples per channel.
var officialVectors = []struct {
	name, sha256     string
	packets, samples int
}{
	{"testvector01.bit", "00a4d34e12a2a32a047bfcaf762f663430b2232bc76a1c21e3b598f060f99b7e", 2147, 1415040},
	{"testvector02.bit", "285deaf78e6b1ebdde7dc6a76af279a08bf0fa7a13e3a84e87ac3f76213626a6", 1185, 1201440},
	{"testvector03.bit", "811b3d28483c781e4d1a0f8008bf73c3882c6c9cf8dedc65c0c14fa4239bb9d9", 998, 1015680},
	{"testvector04.bit", "874b838699a471acd97bd13e11224a3ef71c95b018b85a9c93a41459a7185565", 1265, 1278240},
	{"testvector05.bit", "9b13f677c61cfad9a94460f986401b843e07e3245265067580539d9d8f1ec230", 2037, 1304160},
	{"testvector06.bit", "65a779507949842232cfb6119db3ec8c056c05b80d65abb7a3ae9b06b443e33f", 1876, 1200960},
	{"testvector07.bit", "ae7ae8f569807d4d964c574d7b1556e8b930307adfa9928942fe44f5e2bcc5f1", 4186, 1085040},
	{"testvector08.bit", "4de258de02ccc08c805b3d41414c3a0f2f607e57ea3a7a5cb9bef26c2f01eb01", 1247, 1310160},
	{"testvector09.bit", "91a798186cd2483e47a6b3db44eb3f8fb7f5584c3ed2f2c8158ae8f9172a6b31", 1337, 1323600},
	{"testvector10.bit", "7288deb8d0e9e49f7f8ae50d6b4f2221294faa222b9db69161951b928e62435a", 1912, 1536480},
	{"testvector11.bit", "ee3cd3c6f420d803f563af6518851316696fd121932a191045349a615df7a249", 553, 1440960},
	{"testvector12.bit", "48df85083178e8fc9c31af3fa6d5b38377f0eb84da4931a8886af503b17a0218", 1332, 1278720},
}

// TestOfficialOpusVectors decodes the twelve RFC 8251 test vectors, which
// cover all 32 configurations, with mono and stereo output at 48 kHz. Every
// packet's sample count and final range must match and all PCM must be
// finite. Set OPUS_TEST_VECTORS to the extracted opus_newvectors directory
// (https://www.rfc-editor.org/rfc/rfc8251#section-11). Waveform conformance
// needs the official opus_compare tool and is not checked here.
func TestOfficialOpusVectors(t *testing.T) {
	root, enabled := os.LookupEnv("OPUS_TEST_VECTORS")
	if !enabled {
		t.Skip("set OPUS_TEST_VECTORS to the official RFC 8251 corpus")
	}
	if root == "" {
		t.Fatal("OPUS_TEST_VECTORS is set but empty")
	}
	for _, tc := range officialVectors {
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, channels), func(t *testing.T) {
				data := readPinnedFile(t, filepath.Join(root, tc.name), tc.sha256)
				d, err := NewDecoder(channels)
				if err != nil {
					t.Fatal(err)
				}
				out := make([]float32, MaxPacketSamples*channels)
				offset, count, samples := 0, 0, 0
				for offset < len(data) {
					if len(data)-offset < 8 {
						t.Fatalf("record %d: truncated header", count)
					}
					size := int(binary.BigEndian.Uint32(data[offset:]))
					expectedRange := binary.BigEndian.Uint32(data[offset+4:])
					offset += 8
					if size <= 0 || size > len(data)-offset {
						t.Fatalf("record %d: invalid payload size %d", count, size)
					}
					payload := data[offset : offset+size]
					offset += size
					packet, err := Parse(payload, false)
					if err != nil {
						t.Fatalf("record %d: %v", count, err)
					}
					n, err := d.Decode(payload, out)
					if err != nil {
						t.Fatalf("record %d: %v", count, err)
					}
					if n != packet.Duration48() {
						t.Fatalf("record %d: sample count %d, want %d", count, n, packet.Duration48())
					}
					if got := d.FinalRange(); got != expectedRange {
						t.Fatalf("record %d: final range %#x, want %#x", count, got, expectedRange)
					}
					for i, value := range out[:n*channels] {
						if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
							t.Fatalf("record %d sample %d: non-finite PCM", count, i)
						}
					}
					count++
					samples += n
				}
				if count != tc.packets || samples != tc.samples {
					t.Fatalf("decoded %d packets / %d samples, want %d / %d", count, samples, tc.packets, tc.samples)
				}
			})
		}
	}
}

// TestOfficialOpusVectorsFEC decodes the RFC 8251 vectors with three of every
// seven packets lost (records i with i % 7 in {2, 4, 5}: single losses and
// pairs), each loss followed by a packet decoded with DecodeFEC from it and
// the others concealed, as tools/libopus-ref/decode-logs.sh has libopus's
// generic build do it (decode_float -fec always). No packet of the vectors
// carries LBRR data, so this covers DecodeFEC's choices and its concealment
// at the next packet's configuration in every configuration, frame size and
// mode switch. The decode log, one line per record with its kind, sample
// count, final range and PCM hash, must have the SHA-256 that
// testdata/decode-logs/official.txt records; on architectures other than
// amd64 and 386 the log without its PCM hashes must.
func TestOfficialOpusVectorsFEC(t *testing.T) {
	root, enabled := os.LookupEnv("OPUS_TEST_VECTORS")
	if !enabled {
		t.Skip("set OPUS_TEST_VECTORS to the official RFC 8251 corpus")
	}
	raw, err := os.ReadFile(filepath.Join(decodeLogsDir, "official.txt"))
	if err != nil {
		t.Fatal(err)
	}
	sums := map[string][2]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if len(f) != 7 {
			t.Fatalf("bad line %q", line)
		}
		sums[f[0]+"/"+f[1]] = [2]string{f[5], f[6]}
	}
	if len(sums) != 2*len(officialVectors) {
		t.Fatalf("%d official FEC references", len(sums))
	}
	exact := runtime.GOARCH == "amd64" || runtime.GOARCH == "386"
	for _, tc := range officialVectors {
		data := readPinnedFile(t, filepath.Join(root, tc.name), tc.sha256)
		var packets [][]byte
		for offset := 0; offset < len(data); {
			size := int(binary.BigEndian.Uint32(data[offset:]))
			packets = append(packets, data[offset+8:offset+8+size])
			offset += 8 + size
		}
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, channels), func(t *testing.T) {
				lost := func(i int) bool { return i%7 == 2 || i%7 == 4 || i%7 == 5 }
				d, err := NewDecoder(channels)
				if err != nil {
					t.Fatal(err)
				}
				out := make([]float32, MaxPacketSamples*channels)
				full, head := sha256.New(), sha256.New()
				fec := 0
				for i, p := range packets {
					var n int
					kind := "packet"
					switch {
					case !lost(i):
						n, err = d.Decode(p, out)
					case i+1 < len(packets) && !lost(i+1):
						kind = "fec"
						fec++
						parsed, _ := Parse(p, false)
						n, err = d.DecodeFEC(packets[i+1], parsed.Duration48(), out)
					default:
						kind = "lost"
						parsed, _ := Parse(p, false)
						n, err = d.Conceal(parsed.Duration48(), out)
					}
					if err != nil {
						t.Fatalf("record %d (%s): %v", i, kind, err)
					}
					line := fmt.Sprintf("%d %s %d %d %08x", i, kind, len(p), n, d.FinalRange())
					head.Write([]byte(line + "\n"))
					full.Write(fmt.Appendf(nil, "%s %016x\n", line, fnv1a64(out[:n*channels])))
				}
				want := sums[fmt.Sprintf("%s/%d", tc.name, channels)]
				gotHead := fmt.Sprintf("%x", head.Sum(nil))
				if gotHead != want[1] {
					t.Fatalf("decode log without PCM hashes: SHA-256 %s, libopus %s", gotHead, want[1])
				}
				if gotFull := fmt.Sprintf("%x", full.Sum(nil)); gotFull != want[0] {
					if exact {
						t.Fatalf("decode log: SHA-256 %s, libopus %s", gotFull, want[0])
					}
					t.Logf("PCM differs from libopus's generic build on %s", runtime.GOARCH)
				}
				t.Logf("%d records, %d decoded with FEC", len(packets), fec)
			})
		}
	}
}
