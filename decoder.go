package puropus

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/fgn/puropus/internal/ogg"
	"github.com/fgn/puropus/internal/resample"
	"github.com/fgn/puropus/internal/webm"
	"github.com/fgn/puropus/opus"
)

// DecodeOptions configures a Decoder. The zero value decodes at 48 kHz,
// fails on the first undecodable packet, and does not retain packets.
type DecodeOptions struct {
	// SampleRate selects the output rate: 0 or 48000 for native output, or
	// 16000 for the built-in resampler.
	SampleRate int
	// Tolerant conceals undecodable packets with a known duration, preserving
	// codec history for recovery. Unparseable packets are skipped. Corrupt
	// Ogg pages are always dropped, regardless of this setting.
	Tolerant bool
	// KeepPackets retains every packet so Source can hand them to Cut.
	KeepPackets bool
	// KeepNative additionally retains the decoded 48 kHz PCM, which Cut
	// needs for SnapExact. It implies KeepPackets.
	KeepNative bool
}

// Decoder streams PCM out of an Ogg Opus or WebM/Opus input.
type Decoder struct {
	opts       DecodeOptions
	header     Header
	src        packetSource
	codec      *opus.Decoder
	rs         *resample.Resampler48To16
	rsRight    *resample.Resampler48To16
	rsInput    []float32 // one channel of stereo PCM, deinterleaved for the resamplers
	rsRightOut []float32
	gain       float32 // linear output gain of the current link

	rate      int
	channels  int
	preSkip   int   // pre-skip of the current link, in 48 kHz samples
	skipped   int   // pre-skip samples dropped so far in the current link
	timeline  int64 // 48 kHz samples emitted across all links
	delivered int64 // output samples per channel returned by Read

	buf48   []float32
	rsOut   []float32
	out     []float32 // PCM ready for Read, consumed from outPos
	outPos  int
	packets []Packet
	// native is kept in fixed-size blocks so retaining a long stream does not
	// repeatedly regrow and copy one large slice; Source concatenates once.
	nativeBlocks [][]float32
	nativeCache  []float32
	nativeStart  int64 // timeline position of the first retained native sample
	done         bool
	err          error
}

// nativeBlockSamples is one second of 48 kHz audio per channel.
const nativeBlockSamples = SampleRate

// NewDecoder sniffs the container and prepares to decode. It reads only the
// stream headers up front; the first Read decodes the first packet.
func NewDecoder(r io.Reader, opts DecodeOptions) (*Decoder, error) {
	if opts.SampleRate == 0 {
		opts.SampleRate = SampleRate
	}
	if opts.SampleRate != SampleRate && opts.SampleRate != 16000 {
		return nil, unsupported("sample rate %d (48000 or 16000)", opts.SampleRate)
	}
	if opts.KeepNative {
		opts.KeepPackets = true
	}
	br := bufio.NewReaderSize(r, 64<<10)
	src, header, err := openSource(br)
	if err != nil {
		return nil, err
	}
	if header.Channels < 1 || header.Channels > 2 {
		return nil, unsupported("%d channels (1 or 2 required)", header.Channels)
	}
	codec, err := opus.NewDecoder(header.Channels)
	if err != nil {
		return nil, err
	}
	if !opts.KeepPackets {
		// Nothing keeps a packet past its decode, so the container reader
		// can reuse one buffer for them all.
		src.reuseData()
	}
	d := &Decoder{
		opts:     opts,
		header:   header,
		src:      src,
		codec:    codec,
		rate:     opts.SampleRate,
		channels: header.Channels,
		preSkip:  header.PreSkip,
		gain:     gainScale(header.OutputGain),
		buf48:    make([]float32, opus.MaxPacketSamples*header.Channels),
	}
	if d.rate != SampleRate {
		d.rs = &resample.Resampler48To16{}
		d.rsOut = make([]float32, opus.MaxPacketSamples/3+256)
		if d.channels == 2 {
			d.rsRight = &resample.Resampler48To16{}
			d.rsInput = make([]float32, opus.MaxPacketSamples)
			d.rsRightOut = make([]float32, len(d.rsOut))
		}
	}
	return d, nil
}

// Header returns the stream header.
func (d *Decoder) Header() Header { return d.header }

// SampleRate returns the output sample rate.
func (d *Decoder) SampleRate() int { return d.rate }

// Channels returns the number of interleaved output channels.
func (d *Decoder) Channels() int { return d.channels }

// Position returns how much audio Read has delivered so far.
func (d *Decoder) Position() time.Duration {
	return samplesAtRateToDuration(d.delivered, int64(d.rate))
}

// Read fills p with interleaved float32 samples and returns the number of
// values written, always a multiple of Channels. It returns io.EOF after the
// last sample, and ErrTruncated (wrapping the container error) when the input
// ends inside a container structure; everything decodable before that point
// is returned first. A non-empty p shorter than Channels values cannot hold
// a sample and yields io.ErrShortBuffer.
func (d *Decoder) Read(p []float32) (int, error) {
	n := 0
	frame := d.channels
	if len(p) > 0 && len(p) < frame {
		return 0, io.ErrShortBuffer
	}
	p = p[:len(p)/frame*frame]
	for n < len(p) {
		if d.outPos < len(d.out) {
			c := copy(p[n:], d.out[d.outPos:])
			d.outPos += c
			n += c
			continue
		}
		if d.err != nil {
			break
		}
		if err := d.fill(); err != nil {
			d.err = err
			break
		}
	}
	d.delivered += int64(n / frame)
	if n > 0 {
		return n, nil
	}
	return 0, d.err
}

// Source returns the header and the packets seen so far (when KeepPackets is
// set), plus their native PCM (when KeepNative is set). The packets are
// positioned on the same timeline as Position and Read, so intervals decided
// on the decoded audio apply to them directly.
func (d *Decoder) Source() Source {
	return Source{Header: d.header, Packets: d.packets, Native: d.nativePCM()}
}

// nativePCM returns the retained native PCM: nil when it is not kept, and
// non-nil, if empty, when it is, so that Cut can tell packets that decode to
// nothing (all inside the pre-skip, say) from PCM that was never kept.
func (d *Decoder) nativePCM() []float32 {
	if !d.opts.KeepNative {
		return nil
	}
	total := 0
	for _, b := range d.nativeBlocks {
		total += len(b)
	}
	if d.nativeCache != nil && len(d.nativeCache) == total {
		return d.nativeCache
	}
	out := make([]float32, 0, total) // not nil, even when total is 0
	for _, b := range d.nativeBlocks {
		out = append(out, b...)
	}
	d.nativeCache = out
	return out
}

func (d *Decoder) keepNative(pcm []float32) {
	for len(pcm) > 0 {
		n := len(d.nativeBlocks)
		if n == 0 || len(d.nativeBlocks[n-1]) == cap(d.nativeBlocks[n-1]) {
			d.nativeBlocks = append(d.nativeBlocks, make([]float32, 0, nativeBlockSamples*d.channels))
			n++
		}
		block := d.nativeBlocks[n-1]
		take := min(len(pcm), cap(block)-len(block))
		d.nativeBlocks[n-1] = append(block, pcm[:take]...)
		pcm = pcm[take:]
	}
}

// ReleasePackets drops retained packets that end at or before t, and the
// native PCM before the first packet that remains, so a long-running stream
// can hand each finished window to Cut and then bound its memory. Later
// Source calls return only what remains; packet positions are unchanged.
func (d *Decoder) ReleasePackets(t time.Duration) {
	limit := durationToSamples(t)
	keep := 0
	for keep < len(d.packets) && d.packets[keep].End() <= limit {
		keep++
	}
	if keep == 0 {
		return
	}
	d.packets = append([]Packet(nil), d.packets[keep:]...)
	if !d.opts.KeepNative {
		return
	}
	start := d.timeline
	if len(d.packets) > 0 {
		start = d.packets[0].Start
	}
	d.dropNative(start - d.nativeStart)
}

// dropNative discards the first n retained native samples per channel.
func (d *Decoder) dropNative(n int64) {
	for n > 0 && len(d.nativeBlocks) > 0 {
		block := d.nativeBlocks[0]
		take := min(n, int64(len(block)/d.channels))
		if take == int64(len(block)/d.channels) {
			d.nativeBlocks[0] = nil // let the block go before the outer slice is reused
			d.nativeBlocks = d.nativeBlocks[1:]
		} else {
			d.nativeBlocks[0] = block[take*int64(d.channels):]
		}
		n -= take
		d.nativeStart += take
	}
	d.nativeCache = nil
}

// Acceleration reports which kernels the 16 kHz resampler selected on the
// current CPU, for example "scalar" or "simd-amd64-avx512". The codec
// selects its own: AVX2 for CELT, AVX for SILK (see docs/simd.md).
func Acceleration() string { return resample.Acceleration() }

// fill decodes the next packet into d.out.
func (d *Decoder) fill() error {
	d.out = d.out[:0]
	d.outPos = 0
	if d.done {
		return io.EOF
	}
	data, meta, err := d.src.next()
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = fmt.Errorf("%w: %w", ErrTruncated, err)
		}
		return d.finishInput(err)
	}
	if meta.newLink {
		d.codec.Reset()
		d.preSkip = meta.preSkip
		d.gain = gainScale(meta.outputGain)
		d.skipped = 0
	}

	parsed, perr := opus.Parse(data, false)
	samples := 0
	if perr == nil {
		samples, err = d.codec.DecodeParsed(parsed, data, d.buf48)
	} else {
		err = perr
	}
	if err != nil {
		if !d.opts.Tolerant {
			return d.finishInput(fmt.Errorf("puropus: packet at %v: %w", samplesToDuration(d.timeline), err))
		}
		if perr != nil {
			return nil //nolint:nilerr // an unparseable packet has no duration to conceal and is skipped
		}
		samples, err = d.codec.Conceal(parsed.Duration48(), d.buf48)
		if err != nil {
			return d.finishInput(err)
		}
	}
	d.finishPacket(data, meta, d.buf48[:samples*d.channels])
	return nil
}

// finishInput ends decoding with err. The resampler may still hold samples
// of the decoded prefix, so it is flushed first and err is held back in
// d.err until Read has delivered them.
func (d *Decoder) finishInput(err error) error {
	d.done = true
	d.flushResampler()
	if len(d.out) > 0 {
		d.err = err
		return nil
	}
	return err
}

// finishPacket places a decoded packet on the timeline: it drops the
// pre-skip and end trimming the packet carries, retains the packet and its
// native PCM when asked to, and emits the rest.
func (d *Decoder) finishPacket(data []byte, meta packetMeta, decoded []float32) {
	samples := len(decoded) / d.channels
	skip, kept, trim := packetWindow(samples, d.preSkip-d.skipped, meta.trimEnd)
	d.skipped += skip
	pcm := decoded[skip*d.channels : (skip+kept)*d.channels]
	if d.opts.KeepPackets {
		d.packets = append(d.packets, Packet{Data: data, Start: d.timeline, Duration: kept, Skip: skip, Trim: trim})
	}
	if d.opts.KeepNative {
		d.keepNative(pcm)
	}
	d.timeline += int64(len(pcm) / d.channels)
	d.emit(pcm)
}

// emit applies the output gain to pcm in place and appends it to d.out,
// resampled when the output rate is 16 kHz.
func (d *Decoder) emit(pcm []float32) {
	if d.gain != 1 {
		for i := range pcm {
			pcm[i] *= d.gain
		}
	}
	switch {
	case d.rs == nil:
		d.out = append(d.out, pcm...)
	case d.channels == 1:
		n := d.rs.Process(pcm, d.rsOut)
		d.out = append(d.out, d.rsOut[:n]...)
	default:
		frames := len(pcm) / 2
		for i := 0; i < frames; i++ {
			d.rsInput[i] = pcm[2*i]
		}
		n := d.rs.Process(d.rsInput[:frames], d.rsOut)
		for i := 0; i < frames; i++ {
			d.rsInput[i] = pcm[2*i+1]
		}
		d.rsRight.Process(d.rsInput[:frames], d.rsRightOut)
		for i := 0; i < n; i++ {
			d.out = append(d.out, d.rsOut[i], d.rsRightOut[i])
		}
	}
}

// flushResampler appends the samples still held by the resampler to d.out.
func (d *Decoder) flushResampler() {
	if d.rs == nil {
		return
	}
	for {
		n := d.rs.Flush(d.rsOut)
		if d.rsRight != nil {
			d.rsRight.Flush(d.rsRightOut)
		}
		if n == 0 {
			return
		}
		if d.rsRight == nil {
			d.out = append(d.out, d.rsOut[:n]...)
		} else {
			for i := 0; i < n; i++ {
				d.out = append(d.out, d.rsOut[i], d.rsRightOut[i])
			}
		}
	}
}

// gainScale converts a Q7.8 dB output gain to a linear factor (RFC 7845
// §5.1).
func gainScale(gainQ78 int16) float32 {
	if gainQ78 == 0 {
		return 1
	}
	return float32(math.Pow(10, float64(gainQ78)/(20*256)))
}

// packetWindow splits a packet's coded samples into the pre-skip it absorbs
// (up to preSkipLeft), the samples it contributes to the timeline, and the
// end trimming the container asked for. Trimming applies first, so a packet
// inside the pre-skip that is also trimmed contributes nothing.
func packetWindow(coded, preSkipLeft, trimEnd int) (skip, duration, trim int) {
	trim = min(max(trimEnd, 0), coded)
	skip = min(max(preSkipLeft, 0), coded-trim)
	return skip, coded - skip - trim, trim
}

// packetMeta carries container-level information about one packet.
type packetMeta struct {
	// trimEnd is the number of 48 kHz samples to drop from the end of this
	// packet's decoded audio.
	trimEnd int
	// newLink marks the first packet of a chained Ogg link, whose header
	// gives the pre-skip and output gain from here on.
	newLink    bool
	preSkip    int
	outputGain int16
}

// packetSource yields the Opus packets of a container in order, and io.EOF
// after the last one.
type packetSource interface {
	next() ([]byte, packetMeta, error)
	// reuseData lets next return packet data that is valid only until its
	// next call, which saves an allocation per packet.
	reuseData()
}

// openSource identifies the container by its magic bytes and reads its
// headers.
func openSource(br *bufio.Reader) (packetSource, Header, error) {
	magic, err := br.Peek(4)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Header{}, fmt.Errorf("%w: empty input", ErrTruncated)
		}
		return nil, Header{}, err
	}
	switch {
	case string(magic) == "OggS":
		return openOgg(br)
	case magic[0] == 0x1A && magic[1] == 0x45 && magic[2] == 0xDF && magic[3] == 0xA3:
		return openWebM(br)
	}
	return nil, Header{}, unsupported("container (expected Ogg or WebM)")
}

// oggSource reads the Opus stream of an Ogg file, following chained links
// and deriving end trimming from the final granule position.
type oggSource struct {
	r        *ogg.Reader
	channels int // of the first link; chained links must match it
	// The header of a chained link, applied to its first audio packet.
	pendingLink    bool
	pendingPreSkip int
	pendingGain    int16
	needTags       bool // a chained link's OpusTags is the next packet
	// pageDuration counts the samples of the packets since the last page
	// with a granule position; previousGranule is that position.
	pageDuration    int64
	previousGranule int64
	haveGranule     bool
}

func openOgg(br *bufio.Reader) (packetSource, Header, error) {
	r := ogg.NewReader(br)
	r.SetBOSSelector(func(packet []byte) bool {
		return len(packet) >= 8 && string(packet[:8]) == "OpusHead"
	})
	first, err := r.Next()
	if err != nil {
		return nil, Header{}, wrapContainer(err)
	}
	head, err := opus.ParseHead(first.Data)
	if err != nil {
		return nil, Header{}, fmt.Errorf("puropus: first Ogg packet: %w", err)
	}
	tags, err := r.Next()
	if err != nil {
		return nil, Header{}, wrapContainer(err)
	}
	if !opus.IsTags(tags.Data) {
		return nil, Header{}, fmt.Errorf("puropus: second Ogg packet is not OpusTags")
	}
	header := Header{Format: FormatOgg, Channels: head.Channels, PreSkip: head.PreSkip, OutputGain: head.OutputGain}
	return &oggSource{r: r, channels: head.Channels}, header, nil
}

func (s *oggSource) reuseData() { s.r.ReusePacketData() }

func (s *oggSource) next() ([]byte, packetMeta, error) {
	for {
		p, err := s.r.Next()
		if err != nil {
			if s.needTags && errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF // the input ended inside a chained link's headers
			}
			return nil, packetMeta{}, err
		}
		if p.BOS && len(p.Data) >= 8 && string(p.Data[:8]) == "OpusHead" {
			head, err := opus.ParseHead(p.Data)
			if err != nil {
				return nil, packetMeta{}, fmt.Errorf("puropus: chained Ogg OpusHead: %w", err)
			}
			if head.Channels != s.channels {
				return nil, packetMeta{}, unsupported("channel-count change between Ogg links (%d to %d)", s.channels, head.Channels)
			}
			s.pendingLink = true
			s.pendingPreSkip = head.PreSkip
			s.pendingGain = head.OutputGain
			s.needTags = true
			s.pageDuration = 0
			s.haveGranule = false
			continue
		}
		// A chained link whose OpusTags page was lost (dropped as corrupt)
		// goes on to its audio, which is decoded, as ffmpeg n9.0.2 decodes
		// it. openOgg still requires the first link's OpusTags, as ffmpeg
		// does: without it the input may not be Ogg Opus.
		s.needTags = false
		if opus.IsTags(p.Data) {
			continue
		}
		meta := packetMeta{}
		if s.pendingLink {
			meta.newLink = true
			meta.preSkip = s.pendingPreSkip
			meta.outputGain = s.pendingGain
			s.pendingLink = false
		}
		parsed, perr := opus.Parse(p.Data, false)
		if perr == nil {
			duration := int64(parsed.Duration48())
			s.pageDuration += duration
			if p.GranuleKnown && p.Granule >= 0 {
				if p.EOS {
					// The stream's end is where the previous page's granule
					// plus this page's packets land; a first (and only)
					// audio page counts from 0. Anything beyond the final
					// granule is encoder padding to trim (RFC 7845 §4.4).
					expected := s.pageDuration
					if s.haveGranule {
						expected += s.previousGranule
					}
					if expected > p.Granule {
						meta.trimEnd = int(min(expected-p.Granule, duration))
					}
				}
				s.previousGranule = p.Granule
				s.haveGranule = true
				s.pageDuration = 0
			}
		}
		return p.Data, meta, nil
	}
}

// webmSource reads the Opus track of a WebM file, deriving end trimming from
// DiscardPadding.
type webmSource struct {
	r *webm.Reader
}

func openWebM(br *bufio.Reader) (packetSource, Header, error) {
	r, err := webm.NewReader(br)
	if err != nil {
		return nil, Header{}, wrapContainer(err)
	}
	track := r.Track()
	channels := track.Channels
	var gain int16
	preSkip := int(durationToSamples(track.CodecDelay))
	if len(track.CodecPrivate) >= 19 {
		head, err := opus.ParseHead(track.CodecPrivate)
		if err != nil {
			return nil, Header{}, fmt.Errorf("puropus: WebM CodecPrivate: %w", err)
		}
		channels = head.Channels
		preSkip = head.PreSkip
		gain = head.OutputGain
	}
	header := Header{
		Format:     FormatWebM,
		Channels:   channels,
		PreSkip:    preSkip,
		OutputGain: gain,
	}
	return &webmSource{r: r}, header, nil
}

func (s *webmSource) reuseData() { s.r.ReuseBlockData() }

func (s *webmSource) next() ([]byte, packetMeta, error) {
	b, err := s.r.Next()
	if err != nil {
		return nil, packetMeta{}, err
	}
	meta := packetMeta{}
	if b.DiscardPadding > 0 {
		meta.trimEnd = int(durationToSamples(b.DiscardPadding))
	}
	return b.Data, meta, nil
}

// wrapContainer reports input that ended inside a container structure as
// ErrTruncated.
func wrapContainer(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %w", ErrTruncated, err)
	}
	return err
}

// ReadPackets parses a container without decoding it and returns its header
// and packets. It is the fast path for Cut when the decoded audio is not
// needed, for example when speech intervals were decided elsewhere.
func ReadPackets(r io.Reader) (Source, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	src, header, err := openSource(br)
	if err != nil {
		return Source{}, err
	}
	out := Source{Header: header}
	position := int64(0)
	preSkip := header.PreSkip
	skipped := 0
	for {
		data, meta, err := src.next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, wrapContainer(err)
		}
		if meta.newLink {
			preSkip = meta.preSkip
			skipped = 0
		}
		parsed, err := opus.Parse(data, false)
		if err != nil {
			return out, fmt.Errorf("puropus: packet at %v: %w", samplesToDuration(position), err)
		}
		skip, duration, trim := packetWindow(parsed.Duration48(), preSkip-skipped, meta.trimEnd)
		skipped += skip
		out.Packets = append(out.Packets, Packet{Data: data, Start: position, Duration: duration, Skip: skip, Trim: trim})
		position += int64(duration)
	}
}
