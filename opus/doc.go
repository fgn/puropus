// Package opus decodes Opus packets (RFC 6716), one at a time and without a
// container, to 48 kHz interleaved float32 PCM. It is a port of the float
// decoder of libopus 1.5.2: Decoder.Decode is opus_decode_float with
// decode_fec = 0, Decoder.Conceal is the same call without a packet (loss
// concealment for a known duration), Decoder.DecodeFEC is the call with
// decode_fec = 1 (a lost interval recovered from the in-band FEC of the
// packet after it), FinalRange is OPUS_GET_FINAL_RANGE and Reset is
// OPUS_RESET_STATE. Every SILK, hybrid and CELT configuration decodes, mono
// or stereo, to one or two output channels.
//
// Parse reads a packet's table of contents and frame layout without
// decoding it, HasLBRR reports whether a packet carries in-band FEC data,
// ParseHead and Head.Marshal read and write the OpusHead header of Ogg Opus
// and WebM, and Silence builds packets that decode to silence without an
// encoder.
//
// In-band FEC (LBRR) is low-bitrate redundancy that a SILK or hybrid packet
// carries for the audio just before it (RFC 6716 §4.2.5). Decode
// parses and skips it. When a packet is lost, a caller that has the next
// packet calls DecodeFEC with it for the lost duration, then decodes the
// next packet as usual: the end of the lost interval comes from the next
// packet's LBRR data and the rest is concealed, as libopus does. A caller
// without the next packet, or whose next packet carries no LBRR data
// (HasLBRR), conceals with Conceal.
//
// NewDecoder allocates all of a Decoder's state; decoding, concealment and
// FEC decoding allocate nothing. A packet that Decode rejects leaves the
// decoder as it was, so the caller can conceal it with Conceal instead; so
// does a next packet that DecodeFEC cannot parse.
package opus
