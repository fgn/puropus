// Package opus decodes Opus packets (RFC 6716), one at a time and without a
// container, to 48 kHz interleaved float32 PCM. It is a port of the float
// decoder of libopus 1.5.2: Decoder.Decode is opus_decode_float with
// decode_fec = 0, Decoder.Conceal is the same call without a packet (loss
// concealment for a known duration), FinalRange is OPUS_GET_FINAL_RANGE and
// Reset is OPUS_RESET_STATE. Every SILK, hybrid and CELT configuration
// decodes, mono or stereo, to one or two output channels.
//
// Parse reads a packet's table of contents and frame layout without
// decoding it, HasLBRR reports whether a packet carries in-band FEC data,
// ParseHead and Head.Marshal read and write the OpusHead header of Ogg Opus
// and WebM, and Silence builds packets that decode to silence without an
// encoder.
//
// In-band FEC (LBRR) is parsed and skipped: a lost packet is concealed, not
// recovered from the redundancy in the packet after it.
//
// NewDecoder allocates all of a Decoder's state; decoding and concealment
// allocate nothing. A packet that Decode rejects leaves the decoder as it
// was, so the caller can conceal it with Conceal instead.
package opus
