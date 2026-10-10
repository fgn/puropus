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
	// Tolerant decodes past packets that parse but do not decode, instead of
	// failing on them. Such a packet's duration is known, and its audio is
	// recovered from the in-band FEC (LBRR) data of the next packet when that
	// packet carries some, as libopus applications do on a loss, and
	// concealed otherwise; codec history is kept for what follows. The next
	// packet is read before the lost packet's audio is returned.
	// Unparseable packets are skipped. Corrupt Ogg pages are always dropped,
	// regardless of this setting.
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
	links   []Header // the header of each link so far, for Source
	packets []Packet
	// native is kept in fixed-size blocks so retaining a long stream does not
	// repeatedly regrow and copy one large slice; Source concatenates once.
	nativeBlocks [][]float32
	nativeCache  []float32
	nativeStart  int64 // timeline position of the first retained native sample
	// ahead is the packet after an undecodable one, read in Tolerant mode
	// for its in-band FEC; fill decodes it next.
	ahead lookahead
	done  bool
	err   error
}

// lookahead is a result of packetSource.next read ahead of its turn.
type lookahead struct {
	data []byte
	meta packetMeta
	err  error
	ok   bool
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
		links:    []Header{header},
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
	return Source{Header: d.header, Links: d.links, Packets: d.packets, Native: d.nativePCM()}
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
// can hand each finished window to Cut and then bound its memory. Packets
// inside the pre-skip stay as long as the packet of their link after them
// does, because they carry its decoder warm-up, which Cut keeps with it. Later Source calls
// return only what remains; packet positions are unchanged.
func (d *Decoder) ReleasePackets(t time.Duration) {
	limit := durationToSamples(t)
	keep := 0
	for keep < len(d.packets) && d.packets[keep].End() <= limit {
		keep++
	}
	for keep > 0 && keep < len(d.packets) && d.packets[keep-1].Duration == 0 && d.packets[keep-1].Link == d.packets[keep].Link {
		keep--
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
	data, meta, err := d.nextPacket()
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
		d.links = append(d.links, meta.linkHeader(d.header))
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
		samples, err = d.recoverLost(parsed.Duration48())
		if err != nil {
			return d.finishInput(err)
		}
	}
	d.finishPacket(data, meta, d.buf48[:samples*d.channels])
	return nil
}

// nextPacket returns the packet recoverLost read ahead, if there is one,
// and otherwise the source's next packet.
func (d *Decoder) nextPacket() ([]byte, packetMeta, error) {
	if d.ahead.ok {
		a := d.ahead
		d.ahead = lookahead{}
		return a.data, a.meta, a.err
	}
	return d.src.next()
}

// recoverLost produces the samples of a packet that parsed but did not
// decode, in Tolerant mode. It reads the next packet, waiting for it as
// Read does, and keeps it for fill to decode next. When that packet belongs
// to the same link and carries in-band FEC data, the lost packet's samples
// are decoded from it, as a libopus application does on a loss
// (opus_decode_float with decode_fec = 1 for the lost duration, before the
// next packet itself); otherwise, and at the end of the input or on an
// error the next fill reports, they are concealed. Without KeepPackets the
// read reuses the lost packet's data buffer, which nothing reads again.
func (d *Decoder) recoverLost(samples int) (int, error) {
	data, meta, err := d.src.next()
	d.ahead = lookahead{data: data, meta: meta, err: err, ok: true}
	if err == nil && !meta.newLink && opus.HasLBRR(data) {
		return d.codec.DecodeFEC(data, samples, d.buf48)
	}
	return d.codec.Conceal(samples, d.buf48)
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
	skip, preSkipped, kept, trim := packetWindow(samples, meta.trimStart, d.preSkip-d.skipped, meta.trimEnd)
	d.skipped += preSkipped
	pcm := decoded[skip*d.channels : (skip+kept)*d.channels]
	if d.opts.KeepPackets {
		d.packets = append(d.packets, Packet{Data: data, Start: d.timeline, Duration: kept, Skip: skip, Trim: trim, Link: len(d.links) - 1})
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

// packetWindow splits a packet's coded samples into those the container
// discards from its start, those it contributes to the timeline, and those it
// trims from its end. The start loses the packet's own leading discard
// (trimStart, a negative WebM DiscardPadding) and then up to preSkipLeft of
// the stream's pre-skip, which preSkipped reports. Trimming applies first,
// so a packet inside the pre-skip that is also trimmed contributes nothing.
func packetWindow(coded, trimStart, preSkipLeft, trimEnd int) (skip, preSkipped, duration, trim int) {
	trim = min(max(trimEnd, 0), coded)
	lead := min(max(trimStart, 0), coded-trim)
	preSkipped = min(max(preSkipLeft, 0), coded-trim-lead)
	skip = lead + preSkipped
	return skip, preSkipped, coded - skip - trim, trim
}

// packetMeta carries container-level information about one packet.
type packetMeta struct {
	// trimStart and trimEnd are the numbers of 48 kHz samples to drop from
	// the start and the end of this packet's decoded audio.
	trimStart int
	trimEnd   int
	// newLink marks the first packet of a chained Ogg link, whose header
	// gives the pre-skip and output gain from here on.
	newLink    bool
	preSkip    int
	outputGain int16
}

// linkHeader returns the header of the link that meta starts, in a stream
// whose first link has header first.
func (meta packetMeta) linkHeader(first Header) Header {
	return Header{Format: first.Format, Channels: first.Channels, PreSkip: meta.preSkip, OutputGain: meta.outputGain}
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

// oggSource reads the Opus stream of an Ogg file, following chained links.
// It reads all the packets of a page before it returns the first, so that it
// knows the page's granule position: the end trimming of the final page
// spreads over the page's packets (RFC 7845 §4.4).
type oggSource struct {
	r        *ogg.Reader
	channels int // of the first link; chained links must match it
	// The header of a chained link, applied to its first audio packet.
	pendingLink    bool
	pendingPreSkip int
	pendingGain    int16
	needTags       bool // a chained link's OpusTags is the next packet
	// page holds the audio packets of the current page from pagePos on, the
	// last ones from linkStart on in the link the page ends in; read is
	// scratch for reading the page.
	page      []oggPacket
	pagePos   int
	linkStart int
	read      []ogg.Packet
	// pageDuration counts the samples of the packets since the last page
	// with a granule position; previousGranule is that position.
	pageDuration    int64
	previousGranule int64
	haveGranule     bool
}

// oggPacket is an audio packet of the current page.
type oggPacket struct {
	data     []byte
	meta     packetMeta
	duration int // its coded samples, 0 when it does not parse
}

func openOgg(br *bufio.Reader) (packetSource, Header, error) {
	r := ogg.NewReader(br)
	r.SetBOSSelector(func(packet []byte) bool {
		return len(packet) >= 8 && string(packet[:8]) == "OpusHead"
	})
	first, err := r.Next()
	if errors.Is(err, io.EOF) {
		// Every page was read and no BOS page carried OpusHead.
		if dropped := r.DroppedPages(); dropped > 0 {
			return nil, Header{}, unsupported("Ogg file without an Opus stream (%d corrupt pages dropped)", dropped)
		}
		return nil, Header{}, unsupported("Ogg file without an Opus stream")
	}
	if err != nil {
		return nil, Header{}, wrapContainer(err)
	}
	head, err := opus.ParseHead(first.Data)
	if err != nil {
		return nil, Header{}, fmt.Errorf("puropus: first Ogg packet: %w", err)
	}
	tags, err := r.Next()
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF // the input ended inside the headers
	}
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
	for s.pagePos == len(s.page) {
		if err := s.readPage(); err != nil {
			return nil, packetMeta{}, err
		}
	}
	p := s.page[s.pagePos]
	s.page[s.pagePos] = oggPacket{} // the queue keeps no reference to the data
	s.pagePos++
	return p.data, p.meta, nil
}

// readPage reads the packets of the next page, which stay valid together
// even with ReusePacketData, and queues its audio packets with their link
// headers and end trimming.
func (s *oggSource) readPage() error {
	s.page, s.pagePos, s.linkStart = s.page[:0], 0, 0
	p, err := s.r.Next()
	if err != nil {
		if s.needTags && errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // the input ended inside a chained link's headers
		}
		return err
	}
	s.read = append(s.read[:0], p)
	for n := s.r.Buffered(); n > 0; n-- {
		p, err := s.r.Next()
		if err != nil {
			return err
		}
		s.read = append(s.read, p)
	}
	for _, p := range s.read {
		if err := s.queue(p); err != nil {
			return err
		}
	}
	last := s.read[len(s.read)-1]
	clear(s.read) // keep no reference to the data
	if !last.GranuleKnown || last.Granule < 0 {
		return nil
	}
	// The stream's end is where the previous page's granule position plus
	// the packets since land; a first (and only) audio page counts from 0.
	// Anything beyond the final granule position is encoder padding to
	// trim, from the last packet back (RFC 7845 §4.4).
	expected := s.pageDuration
	if s.haveGranule {
		expected += s.previousGranule
	}
	if last.EOS && expected > last.Granule {
		trim := expected - last.Granule
		for i := len(s.page) - 1; i >= s.linkStart && trim > 0; i-- {
			t := min(trim, int64(s.page[i].duration))
			s.page[i].meta.trimEnd = int(t)
			trim -= t
		}
	}
	s.previousGranule = last.Granule
	s.haveGranule = true
	s.pageDuration = 0
	return nil
}

// queue adds a packet of the page being read: a chained link's OpusHead
// starts the link, its OpusTags is skipped, and an audio packet is queued.
func (s *oggSource) queue(p ogg.Packet) error {
	if p.BOS && len(p.Data) >= 8 && string(p.Data[:8]) == "OpusHead" {
		head, err := opus.ParseHead(p.Data)
		if err != nil {
			return fmt.Errorf("puropus: chained Ogg OpusHead: %w", err)
		}
		if head.Channels != s.channels {
			return unsupported("channel-count change between Ogg links (%d to %d)", s.channels, head.Channels)
		}
		s.pendingLink = true
		s.pendingPreSkip = head.PreSkip
		s.pendingGain = head.OutputGain
		s.needTags = true
		s.pageDuration = 0
		s.haveGranule = false
		s.linkStart = len(s.page)
		return nil
	}
	// A chained link whose OpusTags page was lost (dropped as corrupt) goes
	// on to its audio, which is decoded, as ffmpeg n9.0.2 decodes it.
	// openOgg still requires the first link's OpusTags, as ffmpeg does:
	// without it the input may not be Ogg Opus.
	s.needTags = false
	if opus.IsTags(p.Data) {
		return nil
	}
	packet := oggPacket{data: p.Data}
	if s.pendingLink {
		packet.meta.newLink = true
		packet.meta.preSkip = s.pendingPreSkip
		packet.meta.outputGain = s.pendingGain
		s.pendingLink = false
	}
	if parsed, err := opus.Parse(p.Data, false); err == nil {
		packet.duration = parsed.Duration48()
	}
	s.pageDuration += int64(packet.duration)
	s.page = append(s.page, packet)
	return nil
}

// webmSource reads the Opus track of a WebM file, deriving each block's
// trimming from its DiscardPadding.
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
	// DiscardPadding drops audio from the end of the block when positive and
	// from its start when negative (Matroska).
	meta := packetMeta{}
	switch {
	case b.DiscardPadding > 0:
		meta.trimEnd = int(durationToSamples(b.DiscardPadding))
	case b.DiscardPadding < 0:
		meta.trimStart = int(durationToSamples(-b.DiscardPadding))
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
	out := Source{Header: header, Links: []Header{header}}
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
			out.Links = append(out.Links, meta.linkHeader(header))
		}
		parsed, err := opus.Parse(data, false)
		if err != nil {
			return out, fmt.Errorf("puropus: packet at %v: %w", samplesToDuration(position), err)
		}
		skip, preSkipped, duration, trim := packetWindow(parsed.Duration48(), meta.trimStart, preSkip-skipped, meta.trimEnd)
		skipped += preSkipped
		out.Packets = append(out.Packets, Packet{Data: data, Start: position, Duration: duration, Skip: skip, Trim: trim, Link: len(out.Links) - 1})
		position += int64(duration)
	}
}
