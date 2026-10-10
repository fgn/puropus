// Package webm reads and writes the Opus audio track of Matroska/WebM files
// incrementally. The reader consumes elements as they arrive, so it works on
// a live recording with unknown-size Segment and Cluster elements; the writer
// produces the fixed single-track profile browsers emit for MediaRecorder.
package webm

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// EBML and Matroska element IDs used by this package.
const (
	idEBML               = 0x1A45DFA3
	idEBMLVersion        = 0x4286
	idEBMLReadVersion    = 0x42F7
	idEBMLMaxIDLength    = 0x42F2
	idEBMLMaxSizeLength  = 0x42F3
	idDocType            = 0x4282
	idDocTypeVersion     = 0x4287
	idDocTypeReadVersion = 0x4285
	idSegment            = 0x18538067
	idSeekHead           = 0x114D9B74
	idInfo               = 0x1549A966
	idTimecodeScale      = 0x2AD7B1
	idDuration           = 0x4489
	idMuxingApp          = 0x4D80
	idWritingApp         = 0x5741
	idTracks             = 0x1654AE6B
	idTrackEntry         = 0xAE
	idTrackNumber        = 0xD7
	idTrackUID           = 0x73C5
	idTrackType          = 0x83
	idCodecID            = 0x86
	idCodecPrivate       = 0x63A2
	idCodecDelay         = 0x56AA
	idSeekPreRoll        = 0x56BB
	idAudio              = 0xE1
	idSamplingFrequency  = 0xB5
	idChannels           = 0x9F
	idCluster            = 0x1F43B675
	idTimestamp          = 0xE7
	idSimpleBlock        = 0xA3
	idBlockGroup         = 0xA0
	idBlock              = 0xA1
	idDiscardPadding     = 0x75A2
	idCues               = 0x1C53BB6B
	idTags               = 0x1254C367
	idChapters           = 0x1043A770
	idAttachments        = 0x1941A469
	idVoid               = 0xEC
)

const (
	unknownSize = -1
	// maxPayloadSize limits an element payload read into memory: the header
	// elements and the Opus track's blocks. Skipped elements, including
	// blocks of other tracks, may be any size.
	maxPayloadSize = 1 << 20
	trackTypeAudio = 2
)

var (
	// ErrNoOpusTrack reports a file without an A_OPUS audio track.
	ErrNoOpusTrack = errors.New("webm: no Opus audio track")
	// ErrLacing reports a laced block, which this package does not support.
	ErrLacing = errors.New("webm: laced blocks are not supported")

	errInvalidElementHeader = errors.New("webm: invalid element header")
)

// Info holds the Segment information elements.
type Info struct {
	TimecodeScale uint64  // nanoseconds per timestamp tick
	Duration      float64 // in timestamp ticks, 0 when absent
	MuxingApp     string
	WritingApp    string
}

// Track describes the selected Opus audio track.
type Track struct {
	Number            uint64
	CodecID           string
	CodecPrivate      []byte
	Channels          int
	SamplingFrequency float64
	CodecDelay        time.Duration
	SeekPreRoll       time.Duration
}

// Block is one Opus packet from the selected track.
type Block struct {
	Data []byte
	// Timestamp is the container timestamp of the block.
	Timestamp time.Duration
	// DiscardPadding is the amount of the block's decoded audio the muxer
	// asked to drop (BlockGroup only): from the end of the block when
	// positive, from its start when negative.
	DiscardPadding time.Duration
}

// Reader yields the Opus blocks of a WebM stream in file order.
type Reader struct {
	br    *bufio.Reader
	info  Info
	track Track

	segmentRemaining int64 // bytes left in a known-size Segment, or unknownSize
	clusterRemaining int64 // bytes left in the current known-size Cluster, unknownSize, or 0 when outside a cluster
	inCluster        bool
	clusterTicks     int64 // the current Cluster's Timestamp
	seenCluster      bool

	// scratch holds integer elements while they are decoded; with reuse
	// set, block holds the block Next returns.
	scratch []byte
	reuse   bool
	block   []byte
}

// NewReader parses the EBML header, Segment information, and Tracks, and
// positions the reader at the first Cluster.
func NewReader(r io.Reader) (*Reader, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 64<<10)
	}
	rd := &Reader{br: br, info: Info{TimecodeScale: 1000000}, segmentRemaining: unknownSize}
	if err := rd.readHeaders(); err != nil {
		return nil, err
	}
	return rd, nil
}

// ReuseBlockData makes Next return block data in memory that the Reader
// reuses: Block.Data is valid only until the next call to Next. Reading then
// allocates nothing per block once the buffer has grown to the largest
// block. By default every block has its own copy.
func (r *Reader) ReuseBlockData() {
	r.reuse = true
}

// Info returns the Segment information.
func (r *Reader) Info() Info { return r.info }

// Track returns the selected Opus track.
func (r *Reader) Track() Track { return r.track }

func (r *Reader) readHeaders() error {
	id, size, err := r.readElementHeader()
	if err != nil {
		return unexpected(err)
	}
	if id != idEBML {
		return fmt.Errorf("webm: expected EBML header, found element 0x%X", id)
	}
	payload, err := r.readPayload(size, "EBML header")
	if err != nil {
		return err
	}
	if err := validateEBMLHeader(payload); err != nil {
		return err
	}

	id, size, err = r.readElementHeader()
	if err != nil {
		return unexpected(err)
	}
	if id != idSegment {
		return fmt.Errorf("webm: expected Segment, found element 0x%X", id)
	}
	r.segmentRemaining = size

	for {
		id, size, err := r.readElementHeader()
		if err != nil {
			return unexpected(err)
		}
		switch id {
		case idInfo:
			payload, err := r.readPayload(size, "Info")
			if err != nil {
				return err
			}
			if err := r.parseInfo(payload); err != nil {
				return err
			}
		case idTracks:
			payload, err := r.readPayload(size, "Tracks")
			if err != nil {
				return err
			}
			if err := r.parseTracks(payload); err != nil {
				return err
			}
		case idCluster:
			if r.track.Number == 0 {
				return ErrNoOpusTrack
			}
			return r.enterCluster(size)
		default:
			if size == unknownSize {
				return fmt.Errorf("webm: element 0x%X has unknown size", id)
			}
			if err := r.skip(size); err != nil {
				return err
			}
		}
	}
}

func (r *Reader) enterCluster(size int64) error {
	if err := r.validatePayloadSize(size); err != nil {
		return fmt.Errorf("webm: Cluster: %w", err)
	}
	r.inCluster = true
	r.clusterRemaining = size
	r.clusterTicks = 0
	r.seenCluster = true
	return nil
}

// Next returns the next block of the Opus track. It returns io.EOF at the
// end of the stream and io.ErrUnexpectedEOF if the input ends inside an
// element, a known-size Segment included: the input may end between
// elements only when no enclosing element declared more bytes.
func (r *Reader) Next() (Block, error) {
	for {
		if r.segmentRemaining == 0 {
			return Block{}, io.EOF
		}
		if !r.inCluster {
			id, size, err := r.readElementHeader()
			if err != nil {
				if errors.Is(err, io.EOF) || r.trailingInvalidElement(err) {
					return Block{}, r.endOfInput()
				}
				return Block{}, r.invalidInside(unexpected(err))
			}
			switch id {
			case idCluster:
				if err := r.enterCluster(size); err != nil {
					return Block{}, err
				}
			case idEBML:
				// A concatenated file follows; treat as end of this stream.
				return Block{}, io.EOF
			default:
				if size == unknownSize {
					return Block{}, fmt.Errorf("webm: element 0x%X has unknown size", id)
				}
				if err := r.skip(size); err != nil {
					return Block{}, err
				}
			}
			continue
		}

		if r.clusterRemaining == 0 {
			r.inCluster = false
			continue
		}
		if r.clusterRemaining == unknownSize {
			// An unknown-size cluster ends where the next segment-level
			// element begins.
			id, err := r.peekID()
			if err != nil {
				if errors.Is(err, io.EOF) || r.trailingInvalidElement(err) {
					return Block{}, r.endOfInput()
				}
				return Block{}, r.invalidInside(unexpected(err))
			}
			if isSegmentLevel(id) {
				r.inCluster = false
				continue
			}
		}

		id, size, err := r.readElementHeader()
		if err != nil {
			if (errors.Is(err, io.EOF) && r.clusterRemaining == unknownSize) || r.trailingInvalidElement(err) {
				return Block{}, r.endOfInput()
			}
			return Block{}, r.invalidInside(unexpected(err))
		}
		if size == unknownSize {
			return Block{}, fmt.Errorf("webm: cluster child 0x%X has unknown size", id)
		}
		if err := r.validatePayloadSize(size); err != nil {
			return Block{}, fmt.Errorf("webm: cluster child 0x%X: %w", id, err)
		}
		switch id {
		case idTimestamp:
			payload, err := r.readInto(&r.scratch, size, "cluster Timestamp")
			if err != nil {
				return Block{}, err
			}
			r.clusterTicks = int64(readUint(payload))
		case idSimpleBlock:
			trackNumber, err := r.peekBlockTrack(size)
			if err != nil {
				return Block{}, fmt.Errorf("webm: block track number: %w", err)
			}
			if trackNumber != r.track.Number {
				if err := r.skip(size); err != nil {
					return Block{}, err
				}
				continue
			}
			payload, err := r.readBlock(size, "SimpleBlock")
			if err != nil {
				return Block{}, err
			}
			block, ok, err := r.parseBlock(payload, 0)
			if err != nil {
				return Block{}, err
			}
			if ok {
				return block, nil
			}
		case idBlockGroup:
			block, ok, err := r.readBlockGroup(size)
			if err != nil {
				return Block{}, err
			}
			if ok {
				return block, nil
			}
		default:
			if err := r.skip(size); err != nil {
				return Block{}, err
			}
		}
	}
}

// readBlockGroup walks a BlockGroup directly from the input stream. This lets
// it skip a block from another track without buffering or applying the Opus
// packet size limit to that block.
func (r *Reader) readBlockGroup(size int64) (Block, bool, error) {
	remaining := size
	var blockData []byte
	var discard int64
	for remaining > 0 {
		id, childSize, n, err := r.readElementHeaderWithin(remaining)
		if err != nil {
			return Block{}, false, unexpected(err)
		}
		remaining -= int64(n)
		if childSize == unknownSize || childSize > remaining {
			return Block{}, false, errors.New("webm: malformed BlockGroup")
		}
		switch id {
		case idBlock:
			trackNumber, err := r.peekBlockTrack(childSize)
			if err != nil {
				return Block{}, false, fmt.Errorf("webm: block track number: %w", err)
			}
			if trackNumber != r.track.Number {
				if err := r.skip(childSize); err != nil {
					return Block{}, false, err
				}
				remaining -= childSize
				if err := r.skip(remaining); err != nil {
					return Block{}, false, err
				}
				return Block{}, false, nil
			}
			blockData, err = r.readBlock(childSize, "Block")
			if err != nil {
				return Block{}, false, err
			}
		case idDiscardPadding:
			body, err := r.readInto(&r.scratch, childSize, "DiscardPadding")
			if err != nil {
				return Block{}, false, err
			}
			discard = readInt(body)
		default:
			if err := r.skip(childSize); err != nil {
				return Block{}, false, err
			}
		}
		remaining -= childSize
	}
	if blockData == nil {
		return Block{}, false, nil
	}
	return r.parseBlock(blockData, discard)
}

// parseBlock decodes a Block or SimpleBlock payload: the track number as a
// VINT, a signed 16-bit timestamp relative to the Cluster, a flags byte, and
// the frame. It reports false for a block of another track.
func (r *Reader) parseBlock(payload []byte, discardNS int64) (Block, bool, error) {
	trackNumber, n, err := readVINTValue(payload)
	if err != nil {
		return Block{}, false, fmt.Errorf("webm: block track number: %w", err)
	}
	if len(payload) < n+3 {
		return Block{}, false, errors.New("webm: block header truncated")
	}
	if trackNumber != r.track.Number {
		return Block{}, false, nil
	}
	relative := int64(int16(binary.BigEndian.Uint16(payload[n : n+2])))
	flags := payload[n+2]
	if flags&0x06 != 0 { // the lacing bits
		return Block{}, false, ErrLacing
	}
	ticks := r.clusterTicks + relative
	return Block{
		Data:           payload[n+3:],
		Timestamp:      time.Duration(ticks * int64(r.info.TimecodeScale)),
		DiscardPadding: time.Duration(discardNS),
	}, true, nil
}

func (r *Reader) parseInfo(payload []byte) error {
	return walk(payload, func(id uint64, body []byte) error {
		switch id {
		case idTimecodeScale:
			r.info.TimecodeScale = readUint(body)
			if r.info.TimecodeScale == 0 {
				return errors.New("webm: TimecodeScale is zero")
			}
		case idDuration:
			f, err := readFloat(body)
			if err != nil {
				return err
			}
			r.info.Duration = f
		case idMuxingApp:
			r.info.MuxingApp = string(body)
		case idWritingApp:
			r.info.WritingApp = string(body)
		}
		return nil
	})
}

// parseTracks selects the first Opus audio track.
func (r *Reader) parseTracks(payload []byte) error {
	found := false
	err := walk(payload, func(id uint64, body []byte) error {
		if id != idTrackEntry || found || r.track.Number != 0 {
			return nil
		}
		track := Track{SamplingFrequency: 8000} // the Matroska default
		trackType := uint64(0)
		if err := walk(body, func(id uint64, body []byte) error {
			switch id {
			case idTrackNumber:
				track.Number = readUint(body)
			case idTrackType:
				trackType = readUint(body)
			case idCodecID:
				track.CodecID = string(body)
			case idCodecPrivate:
				track.CodecPrivate = append([]byte(nil), body...)
			case idCodecDelay:
				track.CodecDelay = time.Duration(readUint(body))
			case idSeekPreRoll:
				track.SeekPreRoll = time.Duration(readUint(body))
			case idAudio:
				return walk(body, func(id uint64, body []byte) error {
					switch id {
					case idSamplingFrequency:
						f, err := readFloat(body)
						if err != nil {
							return err
						}
						track.SamplingFrequency = f
					case idChannels:
						track.Channels = int(readUint(body))
					}
					return nil
				})
			}
			return nil
		}); err != nil {
			return err
		}
		// Early muxers wrote A_OPUS/EXPERIMENTAL; ffmpeg decodes such tracks as
		// Opus too.
		if trackType == trackTypeAudio && (track.CodecID == "A_OPUS" || track.CodecID == "A_OPUS/EXPERIMENTAL") {
			if track.Channels == 0 {
				track.Channels = 1 // the Matroska default
			}
			r.track = track
			found = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found && r.track.Number == 0 {
		return ErrNoOpusTrack
	}
	return nil
}

func validateEBMLHeader(payload []byte) error {
	return walk(payload, func(id uint64, body []byte) error {
		switch id {
		case idDocType:
			if docType := string(body); docType != "webm" && docType != "matroska" {
				return fmt.Errorf("webm: unsupported DocType %q", docType)
			}
		case idEBMLReadVersion:
			if readUint(body) > 1 {
				return fmt.Errorf("webm: unsupported EBMLReadVersion %d", readUint(body))
			}
		}
		return nil
	})
}

// walk calls fn for each child element of a fully buffered master payload.
func walk(payload []byte, fn func(id uint64, body []byte) error) error {
	for at := 0; at < len(payload); {
		id, size, n, err := parseElementHeader(payload[at:])
		if err != nil {
			return err
		}
		at += n
		if size == unknownSize || size > int64(len(payload)-at) {
			return fmt.Errorf("webm: element 0x%X overruns its parent", id)
		}
		end := at + int(size)
		if err := fn(id, payload[at:end]); err != nil {
			return err
		}
		at = end
	}
	return nil
}

func (r *Reader) readElementHeader() (uint64, int64, error) {
	id, size, _, err := r.readElementHeaderWithin(r.parentRemaining())
	return id, size, err
}

// readElementHeaderWithin consumes the next element header, which must fit in
// limit bytes (unknownSize for no limit), and returns the element's ID and
// size and the header's length. It returns io.EOF when limit is zero or the
// input ends before the header.
func (r *Reader) readElementHeaderWithin(limit int64) (uint64, int64, int, error) {
	if limit == 0 {
		return 0, 0, 0, io.EOF
	}
	head, err := r.br.Peek(1)
	if err != nil {
		return 0, 0, 0, err
	}
	idLen := vintLength(head[0])
	if idLen == 0 || idLen > 4 {
		return 0, 0, 0, fmt.Errorf("%w: invalid element ID byte 0x%02X", errInvalidElementHeader, head[0])
	}
	if limit != unknownSize && int64(idLen+1) > limit {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	peek, err := r.br.Peek(idLen + 1)
	if err != nil {
		return 0, 0, 0, unexpected(err)
	}
	sizeLen := vintLength(peek[idLen])
	if sizeLen == 0 {
		return 0, 0, 0, fmt.Errorf("%w: invalid element size byte 0x%02X", errInvalidElementHeader, peek[idLen])
	}
	if limit != unknownSize && int64(idLen+sizeLen) > limit {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	full, err := r.br.Peek(idLen + sizeLen)
	if err != nil {
		return 0, 0, 0, unexpected(err)
	}
	id, size, n, err := parseElementHeader(full)
	if err != nil {
		return 0, 0, 0, err
	}
	if _, err := r.br.Discard(n); err != nil {
		return 0, 0, 0, err
	}
	r.consumed(int64(n))
	return id, size, n, nil
}

// peekID returns the next element ID without consuming it.
func (r *Reader) peekID() (uint64, error) {
	head, err := r.br.Peek(1)
	if err != nil {
		return 0, err
	}
	idLen := vintLength(head[0])
	if idLen == 0 || idLen > 4 {
		return 0, fmt.Errorf("%w: invalid element ID byte 0x%02X", errInvalidElementHeader, head[0])
	}
	full, err := r.br.Peek(idLen)
	if err != nil {
		return 0, unexpected(err)
	}
	id := uint64(0)
	for _, b := range full {
		id = id<<8 | uint64(b)
	}
	return id, nil
}

// peekBlockTrack returns the track number at the start of the next block
// payload, of the given size, without consuming it.
func (r *Reader) peekBlockTrack(size int64) (uint64, error) {
	if size < 1 {
		return 0, io.ErrUnexpectedEOF
	}
	head, err := r.br.Peek(1)
	if err != nil {
		return 0, unexpected(err)
	}
	n := vintLength(head[0])
	if n == 0 || int64(n) > size {
		return 0, errors.New("webm: invalid variable-length integer")
	}
	data, err := r.br.Peek(n)
	if err != nil {
		return 0, unexpected(err)
	}
	trackNumber, _, err := readVINTValue(data)
	return trackNumber, err
}

// readBlock reads a Block or SimpleBlock payload: into the reused block
// buffer when ReuseBlockData was called, else into new memory.
func (r *Reader) readBlock(size int64, what string) ([]byte, error) {
	if r.reuse {
		return r.readInto(&r.block, size, what)
	}
	return r.readPayload(size, what)
}

// readPayload reads an element payload of the given size into new memory.
// what names the element in errors.
func (r *Reader) readPayload(size int64, what string) ([]byte, error) {
	var payload []byte
	return r.readInto(&payload, size, what)
}

// readInto reads an element payload of the given size into *buf, growing it
// as needed, and returns the payload. what names the element in errors.
func (r *Reader) readInto(buf *[]byte, size int64, what string) ([]byte, error) {
	if size == unknownSize {
		return nil, fmt.Errorf("webm: %s has unknown size", what)
	}
	if size < 0 {
		return nil, fmt.Errorf("webm: %s has invalid size %d", what, size)
	}
	if err := r.validatePayloadSize(size); err != nil {
		return nil, fmt.Errorf("webm: %s: %w", what, err)
	}
	if size > maxPayloadSize {
		return nil, fmt.Errorf("webm: %s is %d bytes, exceeds limit", what, size)
	}
	if *buf == nil || int64(cap(*buf)) < size {
		*buf = make([]byte, int(size))
	}
	payload := (*buf)[:int(size)]
	if _, err := io.ReadFull(r.br, payload); err != nil {
		return nil, unexpected(err)
	}
	r.consumed(size)
	return payload, nil
}

func (r *Reader) skip(size int64) error {
	if size < 0 {
		return fmt.Errorf("webm: invalid skip size %d", size)
	}
	if err := r.validatePayloadSize(size); err != nil {
		return err
	}
	if _, err := io.CopyN(io.Discard, r.br, size); err != nil {
		return unexpected(err)
	}
	r.consumed(size)
	return nil
}

// parentRemaining returns the bytes left in the innermost known-size parent,
// the Cluster or the Segment, or unknownSize when neither has a known size.
func (r *Reader) parentRemaining() int64 {
	remaining := r.segmentRemaining
	if r.inCluster && r.clusterRemaining != unknownSize &&
		(remaining == unknownSize || r.clusterRemaining < remaining) {
		remaining = r.clusterRemaining
	}
	return remaining
}

func (r *Reader) validatePayloadSize(size int64) error {
	if size < 0 && size != unknownSize {
		return fmt.Errorf("invalid element size %d", size)
	}
	remaining := r.parentRemaining()
	if size != unknownSize && remaining != unknownSize && size > remaining {
		return fmt.Errorf("element size %d exceeds parent remainder %d", size, remaining)
	}
	return nil
}

// endOfInput returns the error for input that ends between elements outside
// any known-size Cluster: io.EOF in an unknown-size Segment, which ends where
// the input does, and io.ErrUnexpectedEOF in a known-size Segment, which
// declared bytes that never came (a Segment with none left ends with io.EOF
// before reading on).
func (r *Reader) endOfInput() error {
	if r.segmentRemaining == unknownSize {
		return io.EOF
	}
	return io.ErrUnexpectedEOF
}

// trailingInvalidElement reports whether err is an invalid element header
// after the last Cluster of an unknown-size Segment, outside any known-size
// Cluster that still declares bytes. Such bytes, zero padding for example,
// end the stream: everything before them has been delivered.
func (r *Reader) trailingInvalidElement(err error) bool {
	unfinished := r.inCluster && r.clusterRemaining > 0
	return r.segmentRemaining == unknownSize && r.seenCluster && !unfinished && errors.Is(err, errInvalidElementHeader)
}

// invalidInside returns the error for an invalid element header inside a
// known-size Cluster or Segment that still declares bytes:
// io.ErrUnexpectedEOF when the input ends before them, since then the
// element is cut off rather than corrupt, and err otherwise. Any other error
// is returned as it is.
func (r *Reader) invalidInside(err error) error {
	remaining := r.parentRemaining()
	if remaining == unknownSize || remaining == 0 || !errors.Is(err, errInvalidElementHeader) {
		return err
	}
	if _, derr := io.CopyN(io.Discard, r.br, remaining); derr != nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

// consumed records n bytes read from the current Cluster and Segment.
func (r *Reader) consumed(n int64) {
	if r.inCluster && r.clusterRemaining != unknownSize {
		r.clusterRemaining -= n
		if r.clusterRemaining < 0 {
			r.clusterRemaining = 0
		}
	}
	if r.segmentRemaining != unknownSize {
		r.segmentRemaining -= n
		if r.segmentRemaining < 0 {
			r.segmentRemaining = 0
		}
	}
}

// isSegmentLevel reports whether id starts a Segment child or a new EBML
// document, either of which ends an unknown-size Cluster.
func isSegmentLevel(id uint64) bool {
	switch id {
	case idCluster, idSeekHead, idInfo, idTracks, idCues, idTags, idChapters, idAttachments, idEBML, idSegment:
		return true
	}
	return false
}

// parseElementHeader reads an element ID and size from the start of data. It
// returns the header length in bytes and unknownSize for all-ones sizes.
func parseElementHeader(data []byte) (uint64, int64, int, error) {
	if len(data) == 0 {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	idLen := vintLength(data[0])
	if idLen == 0 || idLen > 4 || len(data) < idLen+1 {
		return 0, 0, 0, errors.New("webm: invalid element ID")
	}
	id := uint64(0)
	for _, b := range data[:idLen] {
		id = id<<8 | uint64(b)
	}
	size, n, unknown, err := readVINTSize(data[idLen:])
	if err != nil {
		return 0, 0, 0, err
	}
	if unknown {
		return id, unknownSize, idLen + n, nil
	}
	return id, int64(size), idLen + n, nil
}

// vintLength returns the length of a variable-length integer from its first
// byte, or 0 when the byte has no length marker.
func vintLength(first byte) int {
	for i := 0; i < 8; i++ {
		if first&(0x80>>uint(i)) != 0 {
			return i + 1
		}
	}
	return 0
}

// readVINTValue decodes a variable-length integer with its marker removed.
func readVINTValue(data []byte) (uint64, int, error) {
	if len(data) == 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	n := vintLength(data[0])
	if n == 0 || len(data) < n {
		return 0, 0, errors.New("webm: invalid variable-length integer")
	}
	value := uint64(data[0]) & (0xFF >> uint(n))
	for _, b := range data[1:n] {
		value = value<<8 | uint64(b)
	}
	return value, n, nil
}

// readVINTSize decodes an element size. It also reports whether the size is
// the reserved all-ones value that marks an unknown size.
func readVINTSize(data []byte) (uint64, int, bool, error) {
	if len(data) == 0 {
		return 0, 0, false, io.ErrUnexpectedEOF
	}
	n := vintLength(data[0])
	if n == 0 || len(data) < n {
		return 0, 0, false, errors.New("webm: invalid element size")
	}
	value, _, err := readVINTValue(data)
	if err != nil {
		return 0, 0, false, err
	}
	allOnes := value == (uint64(1)<<(7*uint(n)))-1
	return value, n, allOnes, nil
}

func readUint(body []byte) uint64 {
	value := uint64(0)
	for _, b := range body {
		value = value<<8 | uint64(b)
	}
	return value
}

// readInt decodes a big-endian two's-complement integer.
func readInt(body []byte) int64 {
	if len(body) == 0 {
		return 0
	}
	value := int64(int8(body[0]))
	for _, b := range body[1:] {
		value = value<<8 | int64(b)
	}
	return value
}

// readFloat decodes a float element, which is 0, 4, or 8 bytes long.
func readFloat(body []byte) (float64, error) {
	switch len(body) {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(body))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(body)), nil
	}
	return 0, fmt.Errorf("webm: invalid float length %d", len(body))
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// Writer produces a single-track WebM/Opus file with 1 ms timestamps, one
// unknown-size Segment, and clusters split before the relative block
// timestamp could exceed 30 s. Blocks are placed on a continuous timeline
// derived from their Opus durations.
type Writer struct {
	w         io.Writer
	cursor    int64 // 48 kHz samples written
	cluster   []byte
	clusterMS int64 // cluster timestamp in ms, valid when len(cluster) > 0
	failed    error
}

// WriterOptions configures the header of a written file.
type WriterOptions struct {
	Channels int
	// CodecPrivate is the OpusHead to embed. When nil a header with pre-skip
	// zero, 48 kHz input rate, and zero gain is written.
	CodecPrivate []byte
	// CodecDelay, when positive, is written to the track as the amount of
	// decoded audio players drop from the start; it should match the
	// pre-skip in CodecPrivate.
	CodecDelay time.Duration
	// Duration, when positive, is written to the Segment Info.
	Duration time.Duration
	// App names the muxer in MuxingApp and WritingApp.
	App string
}

// NewWriter writes the EBML header, Segment, Info, and Tracks and returns a
// Writer ready for blocks.
func NewWriter(w io.Writer, opts WriterOptions) (*Writer, error) {
	if opts.Channels < 1 || opts.Channels > 2 {
		return nil, fmt.Errorf("webm: unsupported channel count %d", opts.Channels)
	}
	if opts.App == "" {
		opts.App = "puropus"
	}
	codecPrivate := opts.CodecPrivate
	if codecPrivate == nil {
		codecPrivate = []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, byte(opts.Channels), 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	}
	if _, err := w.Write(header(opts, codecPrivate)); err != nil {
		return nil, err
	}
	return &Writer{w: w}, nil
}

// header returns the EBML header, the start of an unknown-size Segment, and
// the Segment's Info and Tracks.
func header(opts WriterOptions, codecPrivate []byte) []byte {
	var ebml []byte
	ebml = appendUint(ebml, idEBMLVersion, 1)
	ebml = appendUint(ebml, idEBMLReadVersion, 1)
	ebml = appendUint(ebml, idEBMLMaxIDLength, 4)
	ebml = appendUint(ebml, idEBMLMaxSizeLength, 8)
	ebml = appendString(ebml, idDocType, "webm")
	ebml = appendUint(ebml, idDocTypeVersion, 4)
	ebml = appendUint(ebml, idDocTypeReadVersion, 2)

	file := appendElement(nil, idEBML, ebml)
	file = appendUnknownSizeHeader(file, idSegment)

	var info []byte
	info = appendUint(info, idTimecodeScale, 1000000)
	info = appendString(info, idMuxingApp, opts.App)
	info = appendString(info, idWritingApp, opts.App)
	if opts.Duration > 0 {
		info = appendFloat64(info, idDuration, float64(opts.Duration)/float64(time.Millisecond))
	}
	file = appendElement(file, idInfo, info)

	var audio []byte
	audio = appendFloat64(audio, idSamplingFrequency, 48000.0)
	audio = appendUint(audio, idChannels, uint64(opts.Channels))
	var entry []byte
	entry = appendUint(entry, idTrackNumber, 1)
	entry = appendUint(entry, idTrackUID, 1)
	entry = appendUint(entry, idTrackType, trackTypeAudio)
	entry = appendString(entry, idCodecID, "A_OPUS")
	if opts.CodecDelay > 0 {
		entry = appendUint(entry, idCodecDelay, uint64(opts.CodecDelay))
	}
	entry = appendElement(entry, idCodecPrivate, codecPrivate)
	entry = appendElement(entry, idAudio, audio)
	tracks := appendElement(nil, idTrackEntry, entry)
	return appendElement(file, idTracks, tracks)
}

// WriteBlock appends one Opus packet lasting duration48 samples at 48 kHz.
// A nonzero discard48 asks players to drop that many samples of the packet's
// decoded audio, from its end when positive and from its start when
// negative, which needs a BlockGroup with DiscardPadding instead of a
// SimpleBlock.
func (w *Writer) WriteBlock(data []byte, duration48, discard48 int) error {
	if w.failed != nil {
		return w.failed
	}
	ms := (w.cursor*1000 + 24000) / 48000 // rounded to the nearest millisecond
	if len(w.cluster) > 0 && ms-w.clusterMS > 30000 {
		if err := w.flushCluster(); err != nil {
			return err
		}
	}
	if len(w.cluster) == 0 {
		w.clusterMS = ms
		w.cluster = appendUint(w.cluster[:0], idTimestamp, uint64(ms))
	}
	relative := ms - w.clusterMS
	// Track number 1 as a VINT, the timestamp relative to the cluster, and
	// the SimpleBlock keyframe flag.
	block := append([]byte{0x81, byte(uint16(relative) >> 8), byte(relative), 0x80}, data...)
	if discard48 != 0 {
		block[3] = 0 // a Block has no keyframe flag
		group := appendElement(nil, idBlock, block)
		group = appendInt(group, idDiscardPadding, int64(time.Duration(discard48)*time.Second/48000))
		w.cluster = appendElement(w.cluster, idBlockGroup, group)
	} else {
		w.cluster = appendElement(w.cluster, idSimpleBlock, block)
	}
	w.cursor += int64(duration48)
	return nil
}

func (w *Writer) flushCluster() error {
	if len(w.cluster) == 0 {
		return nil
	}
	if _, err := w.w.Write(appendElement(nil, idCluster, w.cluster)); err != nil {
		w.failed = err
		return err
	}
	w.cluster = w.cluster[:0]
	return nil
}

// Close writes the pending cluster. The underlying writer is not closed.
func (w *Writer) Close() error {
	if w.failed != nil {
		return w.failed
	}
	return w.flushCluster()
}

// appendID appends an element ID, which keeps its length marker, in its
// minimal number of bytes.
func appendID(dst []byte, id uint64) []byte {
	length := 1
	for probe := id; probe>>8 != 0; probe >>= 8 {
		length++
	}
	for i := length - 1; i >= 0; i-- {
		dst = append(dst, byte(id>>(8*uint(i))))
	}
	return dst
}

// appendSize appends size as a VINT in the fewest bytes that keep it distinct
// from the all-ones unknown-size value.
func appendSize(dst []byte, size uint64) []byte {
	length := 1
	for length < 8 && size >= (uint64(1)<<(7*uint(length)))-1 {
		length++
	}
	value := size | uint64(1)<<(7*uint(length))
	for i := length - 1; i >= 0; i-- {
		dst = append(dst, byte(value>>(8*uint(i))))
	}
	return dst
}

// appendUnknownSizeHeader appends the header of an element whose size is not
// known up front: its ID followed by the 8-byte all-ones size.
func appendUnknownSizeHeader(dst []byte, id uint64) []byte {
	dst = appendID(dst, id)
	return append(dst, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
}

func appendElement(dst []byte, id uint64, payload []byte) []byte {
	dst = appendID(dst, id)
	dst = appendSize(dst, uint64(len(payload)))
	return append(dst, payload...)
}

// appendUint appends an unsigned integer element in the fewest bytes.
func appendUint(dst []byte, id uint64, value uint64) []byte {
	length := 1
	for length < 8 && value >= uint64(1)<<(8*uint(length)) {
		length++
	}
	var bytes [8]byte
	for i := 0; i < length; i++ {
		bytes[length-1-i] = byte(value >> (8 * uint(i)))
	}
	return appendElement(dst, id, bytes[:length])
}

// appendInt appends a signed integer element in the fewest two's-complement
// bytes that keep its sign.
func appendInt(dst []byte, id uint64, value int64) []byte {
	length := 1
	for length < 8 && (value>>(8*uint(length)-1) != 0 && value>>(8*uint(length)-1) != -1) {
		length++
	}
	var bytes [8]byte
	for i := 0; i < length; i++ {
		bytes[length-1-i] = byte(value >> (8 * uint(i)))
	}
	return appendElement(dst, id, bytes[:length])
}

func appendString(dst []byte, id uint64, value string) []byte {
	return appendElement(dst, id, []byte(value))
}

func appendFloat64(dst []byte, id uint64, value float64) []byte {
	var bytes [8]byte
	binary.BigEndian.PutUint64(bytes[:], math.Float64bits(value))
	return appendElement(dst, id, bytes[:])
}
