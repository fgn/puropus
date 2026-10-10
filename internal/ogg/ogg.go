// Package ogg reads and writes Ogg pages and logical packets (RFC 3533)
// incrementally. It knows nothing about Opus; the root package layers the
// Opus header conventions on top.
package ogg

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

const (
	headerSize     = 27
	maxSegments    = 255
	maxPagePayload = 255 * 255

	// FlagContinued marks a page whose first packet continues from the
	// previous page.
	FlagContinued = 0x01
	// FlagBOS marks the first page of a logical stream.
	FlagBOS = 0x02
	// FlagEOS marks the last page of a logical stream.
	FlagEOS = 0x04
)

// Packet is one logical packet reassembled from page segments.
type Packet struct {
	Data []byte
	// Granule is the granule position of the page on which the packet ends.
	// Only the last packet completed on a page carries it; GranuleKnown is
	// false for the others and when the page's granule position is -1. Its
	// meaning is codec specific.
	Granule      int64
	GranuleKnown bool
	// BOS is set on the first packet of a beginning-of-stream page.
	BOS bool
	// EOS is set on the last packet completed on an end-of-stream page.
	EOS    bool
	Serial uint32
}

// Reader reassembles packets from one logical stream at a time in an Ogg
// bitstream. Pages of grouped streams are skipped, while chained streams are
// returned in sequence.
//
// A page with a bad checksum or version is dropped and the bytes after its
// capture pattern are scanned again for the next page. A packet spanning
// pages is dropped only when a page of the selected stream is missing, never
// for a page of another stream. RFC 3533 leaves recovery to the reader. Like
// FFmpeg's Ogg demuxer, this one drops corrupt pages and rescans them.
// Unlike it, it also rescans a page whose corrupt size runs past the end of
// the input, so the pages that size swallowed are still read, and it never
// joins the fragments of a packet across a missing page (RFC 7845 §3).
type Reader struct {
	br *bufio.Reader
	// pending holds bytes already read from br that are scanned before br:
	// the rest of a dropped page after its capture pattern.
	pending []byte

	serial    uint32 // the selected stream, valid when haveSer is set
	haveSer   bool
	afterLink bool   // the selected stream ended with an EOS page
	partial   []byte // a packet that continues on the next page; empty unless haveSeq
	queue     []Packet
	queued    int // packets of queue that Next has returned
	selector  func([]byte) bool
	haveSeq   bool
	sequence  uint32 // sequence number of the selected stream's last page
	sawPage   bool   // a page header has been read
	dropped   int

	// The page being read, in buffers reused from page to page, and with
	// reuse set the data of the packets it completes.
	header  [headerSize]byte
	lacing  [maxSegments]byte
	payload []byte
	reuse   bool
	data    []byte
}

// NewReader returns a Reader over r.
func NewReader(r io.Reader) *Reader {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 64<<10)
	}
	return &Reader{br: br}
}

// SetBOSSelector makes the reader select the first logical stream in each
// chain whose BOS packet is accepted. It must be called before Next. A nil
// selector restores the default of following the first stream seen.
func (r *Reader) SetBOSSelector(accept func([]byte) bool) {
	r.selector = accept
}

// ReusePacketData makes Next return packet data in memory that the Reader
// reuses: Packet.Data is valid only until the next call to Next. Reading then
// allocates nothing per packet once the buffers have grown to the stream's
// page size. By default every packet has its own copy.
func (r *Reader) ReusePacketData() {
	r.reuse = true
}

// DroppedPages returns the number of pages discarded because their version or
// checksum was invalid.
func (r *Reader) DroppedPages() int {
	return r.dropped
}

// Next returns the next packet. It returns io.EOF at the end of input and
// io.ErrUnexpectedEOF when the input stops inside a page.
func (r *Reader) Next() (Packet, error) {
	for r.queued == len(r.queue) {
		r.queue, r.queued = r.queue[:0], 0
		if err := r.readPage(); err != nil {
			return Packet{}, err
		}
	}
	packet := r.queue[r.queued]
	r.queue[r.queued] = Packet{} // the queue keeps no reference to the data
	r.queued++
	return packet, nil
}

// sync advances to the next capture pattern. If the input ends first, the
// result is io.EOF when a page was read before, so junk after the last page
// ends the stream cleanly, and io.ErrUnexpectedEOF otherwise.
func (r *Reader) sync() error {
	for {
		head, err := r.peek4()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			if r.sawPage {
				return io.EOF
			}
			return io.ErrUnexpectedEOF
		}
		if head[0] == 'O' && head[1] == 'g' && head[2] == 'g' && head[3] == 'S' {
			return nil
		}
		if err := r.discardByte(); err != nil {
			return err
		}
	}
}

// peek4 returns the next four bytes without consuming them.
func (r *Reader) peek4() (head [4]byte, err error) {
	n := copy(head[:], r.pending)
	if n == len(head) {
		return head, nil
	}
	tail, err := r.br.Peek(len(head) - n)
	copy(head[n:], tail)
	return head, err
}

func (r *Reader) discardByte() error {
	if len(r.pending) > 0 {
		r.consumePending(1)
		return nil
	}
	_, err := r.br.Discard(1)
	return err
}

// consumePending drops the first n bytes of r.pending and releases the buffer
// once it is empty.
func (r *Reader) consumePending(n int) {
	r.pending = r.pending[n:]
	if len(r.pending) == 0 {
		r.pending = nil
	}
}

func (r *Reader) readFull(dst []byte) (int, error) {
	n := copy(dst, r.pending)
	r.consumePending(n)
	if n == len(dst) {
		return n, nil
	}
	m, err := io.ReadFull(r.br, dst[n:])
	if err == io.EOF && n > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n + m, err
}

// readPage reads the next page and queues the packets it completes for the
// selected stream.
func (r *Reader) readPage() error {
	if err := r.sync(); err != nil {
		return err
	}
	header := r.header[:]
	if _, err := r.readFull(header); err != nil {
		return unexpected(err)
	}
	r.sawPage = true
	version := header[4]
	flags := header[5]
	granule := int64(binary.LittleEndian.Uint64(header[6:14]))
	serial := binary.LittleEndian.Uint32(header[14:18])
	sequence := binary.LittleEndian.Uint32(header[18:22])
	crc := binary.LittleEndian.Uint32(header[22:26])
	segments := int(header[26])

	lacing := r.lacing[:segments]
	if n, err := r.readFull(lacing); err != nil {
		return r.incompletePage(pageBytes(header, lacing[:n], nil), err)
	}
	payloadSize := 0
	for _, size := range lacing {
		payloadSize += int(size)
	}
	if cap(r.payload) < payloadSize {
		r.payload = make([]byte, maxPagePayload)
	}
	payload := r.payload[:payloadSize]
	if n, err := r.readFull(payload); err != nil {
		return r.incompletePage(pageBytes(header, lacing, payload[:n]), err)
	}

	// The checksum covers the whole page with the checksum field zeroed.
	header[22], header[23], header[24], header[25] = 0, 0, 0, 0
	sum := crcUpdate(0, header)
	sum = crcUpdate(sum, lacing)
	sum = crcUpdate(sum, payload)
	if version != 0 || sum != crc {
		binary.LittleEndian.PutUint32(header[22:26], crc)
		r.dropAndRescan(pageBytes(header, lacing, payload))
		return nil
	}
	if !r.haveSer {
		// After an EOS page only a BOS page can start the next chained stream.
		if r.afterLink && flags&FlagBOS == 0 {
			return nil
		}
		if r.selector != nil {
			if flags&FlagBOS == 0 || flags&FlagContinued != 0 {
				return nil
			}
			bos, ok := firstPacket(lacing, payload)
			if !ok || !r.selector(bos) {
				return nil
			}
		}
		r.selectStream(serial)
	} else if serial != r.serial {
		return nil
	}

	// A page of the selected stream is missing (dropped as corrupt, or never
	// there) when the sequence number skips, or when a packet was left open
	// and this page does not continue it. The packet's rest was lost; drop
	// what was assembled. Dropped pages leave the packet alone, so this is
	// where a loss in the selected stream, and only there, discards it.
	gap := r.haveSeq && sequence != r.sequence+1
	if gap || flags&FlagContinued == 0 && len(r.partial) > 0 {
		r.partial = r.partial[:0]
	}
	// A continued page with no partial packet starts with the end of a packet
	// whose beginning was lost: skip that first segment run.
	discardRun := flags&FlagContinued != 0 && len(r.partial) == 0
	r.sequence = sequence
	r.haveSeq = true

	offset := 0
	// The page's granule position and EOS flag belong to the last packet
	// completed on it.
	lastComplete := -1
	for i, size := range lacing {
		if size < 255 {
			lastComplete = i
		}
	}
	firstOnPage := true
	r.data = r.data[:0]
	for i, size := range lacing {
		if discardRun {
			offset += int(size)
			if size < 255 {
				discardRun = false
			}
			continue
		}
		r.partial = append(r.partial, payload[offset:offset+int(size)]...)
		offset += int(size)
		if size < 255 {
			var data []byte
			if r.reuse {
				// Packets of this page stay valid while it is read: a
				// reallocated r.data leaves the old array to them.
				start := len(r.data)
				r.data = append(r.data, r.partial...)
				data = r.data[start:len(r.data):len(r.data)]
			} else {
				data = append([]byte(nil), r.partial...)
			}
			packet := Packet{
				Data:   data,
				Serial: serial,
				BOS:    flags&FlagBOS != 0 && firstOnPage,
				EOS:    flags&FlagEOS != 0 && i == lastComplete,
			}
			if i == lastComplete {
				packet.Granule = granule
				packet.GranuleKnown = granule != -1
			}
			r.queue = append(r.queue, packet)
			r.partial = r.partial[:0]
			firstOnPage = false
		}
	}
	if flags&FlagEOS != 0 {
		r.haveSer = false
		r.afterLink = true
		r.haveSeq = false
		r.partial = r.partial[:0]
	}
	return nil
}

func (r *Reader) selectStream(serial uint32) {
	r.serial = serial
	r.haveSer = true
	r.afterLink = false
	r.partial = r.partial[:0]
	r.haveSeq = false
}

// firstPacket returns the first packet completed on a page that does not
// continue a packet from the previous page.
func firstPacket(lacing, payload []byte) ([]byte, bool) {
	size := 0
	for _, segment := range lacing {
		size += int(segment)
		if segment < 255 {
			return payload[:size], true
		}
	}
	return nil, false
}

// incompletePage handles a page cut short by the end of the input. If its
// header is evidently corrupt (a bad version, a size that swallows the next
// capture pattern, or a segment count that only a damaged byte explains), the
// page is dropped and rescanned; otherwise the input is truncated.
func (r *Reader) incompletePage(page []byte, err error) error {
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	invalidVersion := page[4] != 0
	if invalidVersion || bytes.Contains(page[4:], []byte("OggS")) || hasRecoverableSegmentCount(page) {
		r.dropAndRescan(page)
		return nil
	}
	return io.ErrUnexpectedEOF
}

// hasRecoverableSegmentCount recognizes a page whose segment-count byte was
// corrupted but whose original complete page is present. This distinguishes
// that case from a genuinely truncated page when no following capture pattern
// is available for resynchronization.
func hasRecoverableSegmentCount(page []byte) bool {
	if len(page) < headerSize {
		return false
	}
	want := binary.LittleEndian.Uint32(page[22:26])
	for segments := 0; segments <= maxSegments && headerSize+segments <= len(page); segments++ {
		end := headerSize + segments
		for _, size := range page[headerSize:end] {
			end += int(size)
			if end > len(page) {
				break
			}
		}
		if end != len(page) || byte(segments) == page[26] {
			continue
		}
		candidate := append([]byte(nil), page...)
		candidate[26] = byte(segments)
		candidate[22], candidate[23], candidate[24], candidate[25] = 0, 0, 0, 0
		if crcUpdate(0, candidate) == want {
			return true
		}
	}
	return false
}

// pageBytes returns a new copy of a page as read: its header, lacing values
// and payload.
func pageBytes(header, lacing, payload []byte) []byte {
	page := make([]byte, 0, len(header)+len(lacing)+len(payload))
	page = append(page, header...)
	page = append(page, lacing...)
	return append(page, payload...)
}

// dropAndRescan drops page and queues its bytes after the capture pattern to
// be scanned for the next page.
//
// A packet being assembled is kept: the dropped page may belong to another
// stream of a grouped bitstream, and nothing in it, its serial number
// included, can be trusted. If it was a page of the selected stream, the
// stream's next page reveals the loss by its sequence number, and readPage
// drops the packet then.
func (r *Reader) dropAndRescan(page []byte) {
	r.dropped++
	r.pending = append(page[4:], r.pending...)
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// Writer assembles packets into pages. Packets are buffered into the current
// page until Flush, or until the page would exceed the segment or size limit.
type Writer struct {
	w        io.Writer
	serial   uint32
	sequence uint32
	lacing   []byte
	payload  []byte
	granule  int64
	flags    byte
	pending  int // packets completed in the buffered page
	failed   error
}

// NewWriter returns a Writer for one logical stream with the given serial.
func NewWriter(w io.Writer, serial uint32) *Writer {
	return &Writer{w: w, serial: serial, granule: -1}
}

// WritePacket appends a packet whose end corresponds to granule position
// granule. The first packet of a stream should be written with bos set.
func (w *Writer) WritePacket(data []byte, granule int64, bos bool) error {
	if w.failed != nil {
		return w.failed
	}
	if bos {
		if w.pending > 0 {
			if err := w.Flush(); err != nil {
				return err
			}
		}
		w.flags |= FlagBOS
	}
	segments := len(data)/255 + 1
	if w.pending > 0 && (len(w.lacing)+segments > maxSegments || len(w.payload)+len(data) > maxPagePayload) {
		if err := w.Flush(); err != nil {
			return err
		}
	}
	remaining := data
	for len(remaining) >= 255 {
		w.lacing = append(w.lacing, 255)
		w.payload = append(w.payload, remaining[:255]...)
		remaining = remaining[255:]
		if len(w.lacing) == maxSegments {
			// The packet spans pages: emit a full page without a granule and
			// continue it on the next one.
			if err := w.emit(-1); err != nil {
				return err
			}
			w.flags |= FlagContinued
		}
	}
	w.lacing = append(w.lacing, byte(len(remaining)))
	w.payload = append(w.payload, remaining...)
	w.granule = granule
	w.pending++
	return nil
}

// Flush writes the buffered page, if any.
func (w *Writer) Flush() error {
	if w.failed != nil {
		return w.failed
	}
	if len(w.lacing) == 0 {
		return nil
	}
	return w.emit(w.granule)
}

// Close writes the final page with the EOS flag set. The page is empty when
// no packets are buffered, so the stream is always terminated.
func (w *Writer) Close() error {
	if w.failed != nil {
		return w.failed
	}
	w.flags |= FlagEOS
	return w.emit(w.granule)
}

func (w *Writer) emit(granule int64) error {
	page := make([]byte, headerSize+len(w.lacing)+len(w.payload))
	copy(page, "OggS")
	page[5] = w.flags
	binary.LittleEndian.PutUint64(page[6:], uint64(granule))
	binary.LittleEndian.PutUint32(page[14:], w.serial)
	binary.LittleEndian.PutUint32(page[18:], w.sequence)
	page[26] = byte(len(w.lacing))
	copy(page[headerSize:], w.lacing)
	copy(page[headerSize+len(w.lacing):], w.payload)
	binary.LittleEndian.PutUint32(page[22:], crcUpdate(0, page))
	if _, err := w.w.Write(page); err != nil {
		w.failed = err
		return err
	}
	w.sequence++
	w.lacing = w.lacing[:0]
	w.payload = w.payload[:0]
	w.pending = 0
	w.flags = 0
	return nil
}

// The Ogg checksum (RFC 3533) is CRC-32 with polynomial 0x04C11DB7, no
// reflection, zero initial value, and no final XOR.
var crcTable = func() [256]uint32 {
	var table [256]uint32
	for i := range table {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		table[i] = r
	}
	return table
}()

func crcUpdate(crc uint32, data []byte) uint32 {
	for _, b := range data {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^b]
	}
	return crc
}
