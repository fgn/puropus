package silk

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fgn/puropus/internal/ogg"
	"github.com/fgn/puropus/opus/internal/entcode"
)

// TestEntropyTraces follows the SILK decoder frame by frame through two
// fixtures with libopus 1.5.2 traces of every SILK frame
// (testdata/<fixture>.entropy.json at the repository root): the range
// decoder position and range after the frame, the gain index, the decoded
// NLSFs, the LPC coefficients of the second half of the frame and the
// synthesis as silk_Decode hands it to the resampler. A mismatch names the
// first frame and stage that differs.
func TestEntropyTraces(t *testing.T) {
	for _, name := range []string{"ogg-silk-wb-60ms-fec", "ogg-silk-bandwidth-switch"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join("..", "..", "..", "testdata")
			raw, err := os.ReadFile(filepath.Join(root, name+".entropy.json"))
			if err != nil {
				t.Fatal(err)
			}
			var traces [][]struct {
				Tell  int     `json:"tell"`
				Range uint32  `json:"range"`
				Gain  int8    `json:"gain"`
				NLSF  []int16 `json:"nlsf"`
				LPC   []int16 `json:"lpc"`
				PCM   []int16 `json:"pcm"`
			}
			if err := json.Unmarshal(raw, &traces); err != nil {
				t.Fatal(err)
			}
			packets := oggPackets(t, filepath.Join(root, name+".opus"))
			if len(packets) != len(traces) {
				t.Fatalf("%d packets, %d traces", len(packets), len(traces))
			}

			var d Decoder
			d.Init()
			ctl := DecControl{NChannelsAPI: 1, APISampleRate: 48000}
			var pcm [maxAPIFrameLength]int16
			frames := 0
			for i, p := range packets {
				config := int(p[0] >> 3)
				if config >= 12 || p[0]&3 != 0 {
					t.Fatalf("packet %d is not a SILK-only code 0 packet", i)
				}
				ms := []int{10, 20, 40, 60}[config&3]
				ctl.NChannelsInternal = 1 + int(p[0]>>2&1)
				ctl.InternalSampleRate = []int{8000, 12000, 16000}[config>>2]
				ctl.PayloadSizeMs = ms
				var dec entcode.Decoder
				dec.Init(p[1:])
				nFrames := max(1, ms/20)
				if len(traces[i]) != nFrames {
					t.Fatalf("packet %d: %d traces for %d frames", i, len(traces[i]), nFrames)
				}
				for f, want := range traces[i] {
					if _, err := d.Decode(&ctl, FlagDecodeNormal, f == 0, &dec, pcm[:]); err != nil {
						t.Fatalf("packet %d frame %d: %v", i, f, err)
					}
					st := &d.channelState[0]
					order := st.lpcOrder
					switch {
					case dec.Tell() != want.Tell || dec.Rng() != want.Range:
						t.Fatalf("packet %d frame %d: tell %d range %d, libopus %d %d", i, f, dec.Tell(), dec.Rng(), want.Tell, want.Range)
					case st.lastGainIndex != want.Gain:
						t.Fatalf("packet %d frame %d: gain index %d, libopus %d", i, f, st.lastGainIndex, want.Gain)
					case !slices.Equal(st.prevNLSFQ15[:order], want.NLSF):
						t.Fatalf("packet %d frame %d: NLSFs %v, libopus %v", i, f, st.prevNLSFQ15[:order], want.NLSF)
					case !slices.Equal(st.sPLC.prevLPCQ12[:order], want.LPC):
						t.Fatalf("packet %d frame %d: LPC %v, libopus %v", i, f, st.sPLC.prevLPCQ12[:order], want.LPC)
					case !slices.Equal(d.samplesOut1Tmp[0][1:1+st.frameLength], want.PCM):
						t.Fatalf("packet %d frame %d: synthesis differs from libopus", i, f)
					}
					frames++
				}
			}
			t.Logf("%d packets, %d frames", len(packets), frames)
		})
	}
}

// oggPackets returns the audio packets of an Ogg Opus file.
func oggPackets(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := ogg.NewReader(bytes.NewReader(raw))
	var packets [][]byte
	for i := 0; ; i++ {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			return packets
		}
		if err != nil {
			t.Fatal(err)
		}
		if i >= 2 { // OpusHead and OpusTags
			packets = append(packets, p.Data)
		}
	}
}
