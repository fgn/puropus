// Copyright (c) 2010 Xiph.Org Foundation, Skype Limited
// Written by Jean-Marc Valin and Koen Vos
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
//
// - Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//
// - Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER
// OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
// PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
// NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
// Ported from libopus 1.5.2 src/opus_decoder.c.

package opus

import (
	"errors"
	"fmt"

	"github.com/fgn/puropus/opus/internal/celt"
	"github.com/fgn/puropus/opus/internal/entcode"
	"github.com/fgn/puropus/opus/internal/silk"
)

const (
	// SampleRate is the Opus clock rate; decoded PCM is always 48 kHz.
	SampleRate = 48000
	// MaxPacketSamples is the largest per-channel sample count one packet
	// can decode to (120 ms).
	MaxPacketSamples = 5760
)

// Frame durations at the decoder's 48 kHz (F20, F10, F5 and F2_5 in
// opus_decode_frame).
const (
	f20  = SampleRate / 50
	f10  = f20 >> 1
	f5   = f10 >> 1
	f2p5 = f5 >> 1
)

// Mode and bandwidth values for "none": prev_mode before the first frame,
// and the bandwidth of a concealed frame (0 in libopus).
const (
	modeNone      Mode      = -1
	bandwidthNone Bandwidth = -1
)

// Reasons DecodeParsed and Conceal fail. A packet that does not parse fails
// with Parse's error.
var (
	// errBufferTooSmall is OPUS_BUFFER_TOO_SMALL: out cannot hold the
	// decoded samples.
	errBufferTooSmall = errors.New("opus: output buffer too small")
	// errPacketMismatch reports Packet metadata that data cannot have
	// produced.
	errPacketMismatch = errors.New("opus: packet metadata does not match its data")
	// errConcealDuration reports a concealment duration that is not a
	// multiple of 2.5 ms from 2.5 to 120 ms (OPUS_BAD_ARG).
	errConcealDuration = errors.New("opus: concealment duration must be a multiple of 120 samples from 120 to 5760")
	// errRedundancy reports a frame whose CELT redundancy is longer than
	// the frame, where opus_decode_frame's sanity check fires.
	errRedundancy = errors.New("opus: invalid packet: CELT redundancy longer than its frame")
	// errBadArg is OPUS_BAD_ARG from opus_decode_frame: a frame longer than
	// the remaining output.
	errBadArg = errors.New("opus: invalid argument")
	// errInternal is OPUS_INTERNAL_ERROR: the SILK decoder failed on a
	// received frame.
	errInternal = errors.New("opus: internal error")
)

// decoderState is the part of OpusDecoder that OPUS_RESET_STATE clears,
// everything from OPUS_DECODER_RESET_START (stream_channels) on.
// softclip_mem is left out: the float API decodes without soft clipping.
type decoderState struct {
	streamChannels     int
	bandwidth          Bandwidth
	mode               Mode
	prevMode           Mode
	frameSize          int
	prevRedundancy     bool
	lastPacketDuration int
	rangeFinal         uint32
}

// snapshot is the decoder state a hybrid packet can change before
// DecodeParsed rejects it, restored on rejection. It is not in libopus,
// which keeps decoding where DecodeParsed fails: see DecodeParsed.
type snapshot struct {
	state      decoderState
	decControl silk.DecControl
	silk       silk.State
	// celt is saved only for packets of more than one frame: the first
	// frame reaches the sanity check before it touches the CELT decoder.
	celt celt.State
}

// Decoder decodes Opus packets (RFC 6716) to interleaved float32 PCM at 48
// kHz: every SILK, hybrid, and CELT configuration, mono or stereo. It carries
// codec state between packets, so packets of one stream must be decoded in
// order by one Decoder. It is not safe for concurrent use.
type Decoder struct {
	// OpusDecoder: the SILK and CELT decoders, the configuration and the
	// state OPUS_RESET_STATE clears. Fs is always 48 kHz; decode_gain and
	// complexity keep their default of 0 (no gain, no deep PLC or OSCE).
	silkDec    silk.Decoder
	celtDec    celt.Decoder
	channels   int
	decControl silk.DecControl
	decoderState

	// Scratch memory for one frame: opus_decode_frame's stack arrays. A SILK
	// frame is at most 60 ms (pcm_silk), transition and redundancy frames
	// 5 ms.
	dec            entcode.Decoder
	pcmSilk        [3 * f20 * 2]int16
	pcmTransition  [f5 * 2]float32
	redundantAudio [f5 * 2]float32

	snap snapshot
}

// NewDecoder returns a decoder for streams with the given channel count.
// One or two output channels are supported, independently of the coded
// channel count. Mono output disables CELT phase inversion, as libopus does.
func NewDecoder(channels int) (*Decoder, error) {
	if channels < 1 || channels > 2 {
		return nil, fmt.Errorf("opus: unsupported channel count %d", channels)
	}
	d := new(Decoder)
	if err := d.init(channels); err != nil {
		return nil, err
	}
	return d, nil
}

// init is opus_decoder_init with Fs = 48000 on a zeroed decoder.
func (st *Decoder) init(channels int) error {
	st.channels = channels
	st.streamChannels = channels

	st.decControl.APISampleRate = SampleRate
	st.decControl.NChannelsAPI = channels

	// Reset decoder.
	st.silkDec.Init()

	// Initialize CELT decoder. It disables phase inversion for mono output
	// (opus_custom_decoder_init); CELT_SET_SIGNALLING(0) does nothing
	// without custom modes.
	if err := st.celtDec.Init(SampleRate, channels); err != nil {
		return err
	}

	st.prevMode = modeNone
	st.frameSize = SampleRate / 400
	return nil
}

// Channels reports the configured channel count.
func (st *Decoder) Channels() int { return st.channels }

// smoothFade cross-fades overlap samples per channel from in1 to in2 into
// out with the squared window (smooth_fade). out may alias in1 or in2.
func smoothFade(in1, in2, out []float32, overlap, channels int, window []float32) {
	const inc = 48000 / SampleRate
	for c := 0; c < channels; c++ {
		for i := 0; i < overlap; i++ {
			w := window[i*inc] * window[i*inc]
			out[i*channels+c] = float32(w*in2[i*channels+c]) + float32((1-w)*in1[i*channels+c])
		}
	}
}

// silence is the CELT frame that opus_decode_frame decodes to fade out the
// CELT layer after a hybrid frame: only the silence flag is coded.
var silence = [2]byte{0xFF, 0xFF}

// decodeFrame decodes one frame of the current packet into pcm, or conceals
// up to frameSize samples when data holds at most one byte, and returns the
// number of samples per channel written (opus_decode_frame with decode_fec
// = 0). The packet's mode, bandwidth, frame size and coded channels are in
// st (opus_decode_native sets them).
func (st *Decoder) decodeFrame(data []byte, pcm []float32, frameSize int) (int, error) {
	var (
		pcmTransition   []float32
		redundantAudio  []float32
		audiosize       int
		mode            Mode
		bandwidth       Bandwidth
		transition      bool
		redundancy      bool
		redundancyBytes int
		celtToSilk      bool
		redundantRng    uint32
		celtErr         error
		// silkOnly is set when pcm holds no CELT output for the SILK output
		// to be added to.
		silkOnly bool
	)
	channels := st.channels
	length := len(data)
	// The range decoder of the frame; the loss path passes it unused, as
	// libopus passes its uninitialized ec_dec.
	dec := &st.dec

	if frameSize < f2p5 {
		return 0, errBufferTooSmall
	}
	// Limit frame_size to avoid excessive stack allocations.
	frameSize = min(frameSize, SampleRate/25*3)
	// Payloads of 1 (2 including ToC) or 0 trigger the PLC/DTX.
	if length <= 1 {
		data = nil
		// In that case, don't conceal more than what the ToC says.
		frameSize = min(frameSize, st.frameSize)
	}
	if data != nil {
		audiosize = st.frameSize
		mode = st.mode
		bandwidth = st.bandwidth
		dec.Init(data)
	} else {
		audiosize = frameSize
		// Run PLC using last used mode (CELT if we ended with CELT
		// redundancy).
		mode = st.prevMode
		if st.prevRedundancy {
			mode = ModeCELT
		}
		bandwidth = bandwidthNone

		if mode == modeNone {
			// If we haven't got any packet yet, all we can do is return
			// zeros.
			clear(pcm[:audiosize*channels])
			return audiosize, nil
		}

		// Avoids trying to run the PLC on sizes other than 2.5 (CELT), 5
		// (CELT), 10, or 20 (e.g. 12.5 or 30 ms).
		if audiosize > f20 {
			for audiosize > 0 {
				ret, err := st.decodeFrame(nil, pcm, min(audiosize, f20))
				if err != nil {
					return 0, err
				}
				pcm = pcm[ret*channels:]
				audiosize -= ret
			}
			return frameSize, nil
		} else if audiosize < f20 {
			if audiosize > f10 {
				audiosize = f10
			} else if mode != ModeSILK && audiosize > f5 && audiosize < f10 {
				audiosize = f5
			}
		}
	}

	// The float build never lets CELT accumulate on top of the SILK output
	// (celt_accum = 0).

	if data != nil && st.prevMode != modeNone &&
		((mode == ModeCELT && st.prevMode != ModeCELT && !st.prevRedundancy) ||
			(mode != ModeCELT && st.prevMode == ModeCELT)) {
		transition = true
	}
	if transition && mode == ModeCELT {
		pcmTransition = st.pcmTransition[:f5*channels]
		_, _ = st.decodeFrame(nil, pcmTransition, min(f5, audiosize))
	}
	if audiosize > frameSize {
		return 0, errBadArg
	}
	frameSize = audiosize

	// pcm_silk.
	pcmSilk := st.pcmSilk[:max(f10, frameSize)*channels]

	// SILK processing.
	if mode != ModeCELT {
		if st.prevMode == ModeCELT {
			st.silkDec.Reset()
		}

		// The SILK PLC cannot produce frames of less than 10 ms.
		st.decControl.PayloadSizeMs = max(10, 1000*audiosize/SampleRate)

		if data != nil {
			st.decControl.NChannelsInternal = st.streamChannels
			if mode == ModeSILK {
				switch bandwidth {
				case BandwidthNarrowband:
					st.decControl.InternalSampleRate = 8000
				case BandwidthMediumband:
					st.decControl.InternalSampleRate = 12000
				default:
					// Wideband; libopus asserts nothing else reaches here.
					st.decControl.InternalSampleRate = 16000
				}
			} else {
				// Hybrid mode.
				st.decControl.InternalSampleRate = 16000
			}
		}

		lostFlag := silk.FlagDecodeNormal
		if data == nil {
			lostFlag = silk.FlagPacketLost
		}
		decodedSamples := 0
		for {
			// Call SILK decoder.
			firstFrame := decodedSamples == 0
			pcmPtr := pcmSilk[decodedSamples*channels:]
			silkFrameSize, err := st.silkDec.Decode(&st.decControl, lostFlag, firstFrame, dec, pcmPtr)
			if err != nil {
				if lostFlag == silk.FlagDecodeNormal {
					return 0, errInternal
				}
				// PLC failure should not be fatal.
				silkFrameSize = frameSize
				clear(pcmPtr[:min(frameSize*channels, len(pcmPtr))])
			}
			decodedSamples += silkFrameSize
			if decodedSamples >= frameSize {
				break
			}
		}
	}

	startBand := 0
	if mode != ModeCELT && data != nil {
		hybrid := 0
		if mode == ModeHybrid {
			hybrid = 1
		}
		if dec.Tell()+17+20*hybrid <= 8*length {
			// Check if we have a redundant 0-8 kHz band.
			if mode == ModeHybrid {
				redundancy = dec.DecBitLogp(12) != 0
			} else {
				redundancy = true
			}
			if redundancy {
				celtToSilk = dec.DecBitLogp(1) != 0
				// redundancy_bytes will be at least two, in the non-hybrid
				// case due to the ec_tell() check above.
				if mode == ModeHybrid {
					redundancyBytes = int(dec.DecUint(256)) + 2
				} else {
					redundancyBytes = length - (dec.Tell()+7)>>3
				}
				length -= redundancyBytes
				// This is a sanity check. It should never happen for a valid
				// packet, so the exact behaviour is not normative. libopus
				// drops the redundancy and conceals the CELT layer; this
				// decoder rejects the packet (DecodeParsed restores the
				// state).
				if length*8 < dec.Tell() {
					return 0, errRedundancy
				}
				// Shrink decoder because of raw bits.
				dec.SetStorage(dec.Storage() - redundancyBytes)
			}
		}
	}
	if mode != ModeCELT {
		startBand = 17
	}

	if redundancy {
		transition = false
	}

	if transition && mode != ModeCELT {
		pcmTransition = st.pcmTransition[:f5*channels]
		_, _ = st.decodeFrame(nil, pcmTransition, min(f5, audiosize))
	}

	if bandwidth != bandwidthNone {
		endband := 21
		switch bandwidth {
		case BandwidthNarrowband:
			endband = 13
		case BandwidthMediumband, BandwidthWideband:
			endband = 17
		case BandwidthSuperWideband:
			endband = 19
		case BandwidthFullband:
			endband = 21
		}
		_ = st.celtDec.SetEndBand(endband)
	}
	_ = st.celtDec.SetStreamChannels(st.streamChannels)

	// Only allocation memory for redundancy if/when needed.
	if redundancy {
		redundantAudio = st.redundantAudio[:f5*channels]
	}

	// 5 ms redundant frame for CELT->SILK.
	if redundancy && celtToSilk {
		// If the previous frame did not use CELT (the first redundancy frame
		// in a transition from SILK may have been lost) then the CELT decoder
		// is stale at this point and the redundancy audio is not useful,
		// however the final range is still needed (for testing), so the
		// redundancy is always decoded but the decoded audio may not be used.
		_ = st.celtDec.SetStartBand(0)
		_, _ = st.celtDec.DecodeWithEC(data[length:length+redundancyBytes], redundantAudio, f5, nil)
		redundantRng = st.celtDec.FinalRange()
	}

	// MUST be after PLC.
	_ = st.celtDec.SetStartBand(startBand)

	switch {
	case mode != ModeSILK:
		celtFrameSize := min(f20, frameSize)
		// Make sure to discard any previous CELT state.
		if mode != st.prevMode && st.prevMode != modeNone && !st.prevRedundancy {
			st.celtDec.Reset()
		}
		// Decode CELT.
		var celtData []byte
		if data != nil {
			celtData = data[:length]
		}
		_, celtErr = st.celtDec.DecodeWithEC(celtData, pcm, celtFrameSize, dec)
	case st.prevMode == ModeHybrid && !(redundancy && celtToSilk && st.prevRedundancy):
		clear(pcm[:frameSize*channels])
		// For hybrid -> SILK transitions, we let the CELT MDCT do a fade-out
		// by decoding a silence frame.
		_ = st.celtDec.SetStartBand(0)
		_, _ = st.celtDec.DecodeWithEC(silence[:], pcm, f2p5, nil)
	default:
		// libopus clears pcm and adds the SILK output below; silkToFloat
		// writes the same values.
		silkOnly = true
	}

	if mode != ModeCELT {
		n := frameSize * channels
		if silkOnly {
			silkToFloat(pcm[:n], pcmSilk[:n])
		} else {
			addSilk(pcm[:n], pcmSilk[:n])
		}
	}

	window := st.celtDec.Mode().Window()

	// 5 ms redundant frame for SILK->CELT.
	if redundancy && !celtToSilk {
		st.celtDec.Reset()
		_ = st.celtDec.SetStartBand(0)

		_, _ = st.celtDec.DecodeWithEC(data[length:length+redundancyBytes], redundantAudio, f5, nil)
		redundantRng = st.celtDec.FinalRange()
		smoothFade(pcm[channels*(frameSize-f2p5):], redundantAudio[channels*f2p5:],
			pcm[channels*(frameSize-f2p5):], f2p5, channels, window)
	}
	// 5 ms redundant frame for CELT->SILK; ignore if the previous frame did
	// not use CELT (the first redundancy frame in a transition from SILK may
	// have been lost).
	if redundancy && celtToSilk && (st.prevMode != ModeSILK || st.prevRedundancy) {
		for c := 0; c < channels; c++ {
			for i := 0; i < f2p5; i++ {
				pcm[channels*i+c] = redundantAudio[channels*i+c]
			}
		}
		smoothFade(redundantAudio[channels*f2p5:], pcm[channels*f2p5:],
			pcm[channels*f2p5:], f2p5, channels, window)
	}
	if transition {
		if audiosize >= f5 {
			copy(pcm[:channels*f2p5], pcmTransition[:channels*f2p5])
			smoothFade(pcmTransition[channels*f2p5:], pcm[channels*f2p5:],
				pcm[channels*f2p5:], f2p5, channels, window)
		} else {
			// Not enough time to do a clean transition, but we do it anyway.
			// This will not preserve amplitude perfectly and may introduce a
			// bit of temporal aliasing, but it shouldn't be too bad and
			// that's pretty much the best we can do. In any case, generating
			// this transition is pretty silly in the first place.
			smoothFade(pcmTransition, pcm, pcm, f2p5, channels, window)
		}
	}

	// decode_gain is 0: no gain to apply.

	if length <= 1 {
		st.rangeFinal = 0
	} else {
		st.rangeFinal = dec.Rng() ^ redundantRng
	}

	st.prevMode = mode
	st.prevRedundancy = redundancy && !celtToSilk

	if celtErr != nil {
		return 0, celtErr
	}
	return audiosize, nil
}

// Decode decodes one packet into out and returns the number of samples per
// channel written. out must hold at least MaxPacketSamples*Channels() values.
func (st *Decoder) Decode(packet []byte, out []float32) (int, error) {
	parsed, err := Parse(packet, false)
	if err != nil {
		return 0, err
	}
	return st.DecodeParsed(parsed, packet, out)
}

// DecodeParsed is Decode for a packet whose table of contents has already been
// parsed with Parse. data must be the packet bytes parsed produced from.
//
// It is opus_decode_float(st, data, len, out, len(out)/Channels(), 0) with
// one difference: a frame whose CELT redundancy is longer than the frame
// itself, which libopus's opus_decode_frame catches with a non-normative
// sanity check and then decodes without the redundancy, makes DecodeParsed
// return an error. Like every other error, it leaves the decoder state as it
// was before the call, so the caller can conceal the packet as lost.
func (st *Decoder) DecodeParsed(packet Packet, data []byte, out []float32) (int, error) {
	if !packet.describes(data) {
		return 0, errPacketMismatch
	}
	// opus_decode_float limits frame_size to the packet duration, and
	// opus_decode_native rejects a buffer shorter than that.
	frameSize := packet.Duration48()
	if len(out) < frameSize*st.channels {
		return 0, errBufferTooSmall
	}
	return st.decodeNative(packet, data, out, frameSize)
}

// decodeNative decodes a packet that DecodeParsed checked
// (opus_decode_native with decode_fec = 0 and no soft clipping).
func (st *Decoder) decodeNative(packet Packet, data []byte, pcm []float32, frameSize int) (int, error) {
	toc := data[0]
	packetMode := tocMode(toc)
	count := packet.FrameCount

	// A hybrid frame can fail opus_decode_frame's redundancy sanity check
	// after SILK has decoded it, and the frames before it have changed the
	// CELT state too. CELT-only frames have no redundancy, and a SILK-only
	// frame's redundancy is the bytes after its SILK data, so it always fits;
	// nothing else fails once a packet parses (TestCELTPacketsDecodeWithoutError,
	// TestSILKPacketsDecodeWithoutError and FuzzDecode check it). So only
	// hybrid packets pay for the snapshot.
	if packetMode == ModeHybrid {
		st.saveState(count > 1)
	}

	// Update the state as the last step to avoid updating it on an invalid
	// packet.
	st.mode = packetMode
	st.bandwidth = tocBandwidth(toc)
	st.frameSize = tocSamplesPerFrame(toc)
	st.streamChannels = 1
	if toc&0x4 != 0 {
		st.streamChannels = 2
	}

	nbSamples := 0
	for i := 0; i < count; i++ {
		frame := data[packet.FrameOffset[i] : packet.FrameOffset[i]+packet.FrameSize[i]]
		ret, err := st.decodeFrame(frame, pcm[nbSamples*st.channels:], frameSize-nbSamples)
		if err != nil {
			if packetMode == ModeHybrid {
				st.restoreState(count > 1)
			}
			return 0, err
		}
		nbSamples += ret
	}
	st.lastPacketDuration = nbSamples
	// opus_decode_float passes soft_clip = 0, which only clears
	// softclip_mem.
	return nbSamples, nil
}

// saveState saves what a packet can change before DecodeParsed rejects
// it, the CELT state only when withCELT is set.
func (st *Decoder) saveState(withCELT bool) {
	s := &st.snap
	s.state = st.decoderState
	s.decControl = st.decControl
	st.silkDec.SaveState(&s.silk)
	if withCELT {
		st.celtDec.SaveState(&s.celt)
	}
}

// restoreState undoes a rejected packet with the state saveState saved.
func (st *Decoder) restoreState(withCELT bool) {
	s := &st.snap
	st.decoderState = s.state
	st.decControl = s.decControl
	st.silkDec.RestoreState(&s.silk)
	if withCELT {
		st.celtDec.RestoreState(&s.celt)
	}
}

// describes reports whether p is what Parse returns for data, as far as
// decoding depends on it: the TOC fields, and frames that lie inside data
// and are at most MaxFrameSize bytes.
func (p *Packet) describes(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	toc := data[0]
	if p.Config != int(toc>>3) || p.Stereo != (toc&0x4 != 0) || p.Mode != tocMode(toc) ||
		p.Bandwidth != tocBandwidth(toc) || p.FrameDuration != tocSamplesPerFrame(toc) {
		return false
	}
	if p.FrameCount < 1 || p.FrameCount > MaxFrames || p.FrameCount*p.FrameDuration > MaxPacketDuration48 {
		return false
	}
	for i := 0; i < p.FrameCount; i++ {
		offset, size := p.FrameOffset[i], p.FrameSize[i]
		if offset < 1 || size < 0 || size > MaxFrameSize || offset > len(data)-size {
			return false
		}
	}
	return true
}

// Conceal advances a lost interval using the last decoded mode and bandwidth.
// samples is a per-channel 48 kHz count, from 120 to MaxPacketSamples in
// multiples of 120 (2.5 ms). With no history it returns silence. It does not
// recover FEC; callers provide the missing duration. Reset clears this
// history.
//
// It is opus_decode_float(st, NULL, 0, out, samples, 0).
func (st *Decoder) Conceal(samples int, out []float32) (int, error) {
	// For PLC, frame_size has to be a multiple of 2.5 ms.
	if samples < f2p5 || samples > MaxPacketSamples || samples%f2p5 != 0 {
		return 0, errConcealDuration
	}
	if len(out) < samples*st.channels {
		return 0, errBufferTooSmall
	}
	pcmCount := 0
	for pcmCount < samples {
		ret, err := st.decodeFrame(nil, out[pcmCount*st.channels:], samples-pcmCount)
		if err != nil {
			return 0, err
		}
		pcmCount += ret
	}
	st.lastPacketDuration = pcmCount
	return pcmCount, nil
}

// FinalRange returns the final entropy range of the last successfully decoded
// packet, including the XOR contribution of CELT redundancy. It can be checked
// against official Opus test vectors. Reset clears it; a rejected packet
// leaves the value unchanged.
//
// It is OPUS_GET_FINAL_RANGE.
func (st *Decoder) FinalRange() uint32 { return st.rangeFinal }

// Reset clears codec state, as at the start of a stream.
//
// It is OPUS_RESET_STATE.
func (st *Decoder) Reset() {
	st.decoderState = decoderState{}

	st.celtDec.Reset()
	st.silkDec.Reset()
	st.streamChannels = st.channels
	st.frameSize = SampleRate / 400
	// prev_mode is cleared to 0, "no mode".
	st.prevMode = modeNone
}
