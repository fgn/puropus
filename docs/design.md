# Design

## Packages

```
puropus              Decoder (container → PCM), Source, Cut, Stream, writers
├── cmd/puropus      command line: inspect, decode, cut
├── opus             packet decoder (RFC 6716): TOC parsing, frame decoding and
│   │                mode transitions, loss concealment and in-band FEC,
│   │                OpusHead, LBRR detection, silence packets
│   └── internal
│       ├── entcode  range decoder
│       ├── celt     CELT decoder, with loss concealment
│       └── silk     SILK decoder, with loss concealment, comfort noise and its resampler
└── internal
    ├── ogg          Ogg page reader and writer (RFC 3533), incremental
    ├── webm         Matroska/WebM reader and writer for one Opus track, incremental
    └── resample     48 → 16 kHz windowed-sinc FIR matching FFmpeg's default resampler
```

The root package is the API. `opus` is public for callers who only need the
codec. Everything else is an implementation detail. `opus` and the three
packages under it are a port of the libopus 1.5.2 float decoder that keeps
libopus's structure and names; [`opus/internal/README.md`](../opus/internal/README.md)
maps them to the libopus sources and states the porting conventions, and
[provenance.md](provenance.md) says where each part of the code comes from.
`tools/` holds the programs that made the test references (C against
libopus, Python against the FFmpeg binary); they are not part of the Go
packages.

## The decode pipeline

```
io.Reader
  → internal/ogg or internal/webm     one packet at a time, with its trimming
  → opus.Parse                        table of contents and frame layout
  → opus.Decoder                      per frame: SILK, hybrid (SILK + CELT) or CELT
  → 48 kHz float32                    pre-skip and end trimming dropped, output gain applied
  → internal/resample                 only for 16 kHz output, one resampler per channel
  → Decoder.Read
```

**Container.** `NewDecoder` sniffs the first four bytes (`OggS` or the EBML
magic), reads the stream headers (`OpusHead` and `OpusTags`, or the WebM
header, Info and Tracks) and stops; the first `Read` decodes the first
packet. Both readers are incremental, reading one page or element at a time,
so a live recording decodes as it arrives.

**Packet.** `opus.Parse` reads the table of contents and the frame layout of
all four framing codes, padding included, as libopus's
`opus_packet_parse_impl` does (RFC 6716 §3). `opus.Decoder.DecodeParsed` is
`opus_decode_float` with `decode_fec = 0`: it decodes the packet's frames
in order with the port of `opus_decode_frame`, which takes the mode from the
TOC and runs one or both of the codecs:

- **SILK** (`silk.Decoder.Decode`, libopus's `silk_Decode`) for SILK and
  hybrid frames: the range-coded indices and pulses, NLSF to LPC conversion,
  LTP and LPC synthesis in libopus's fixed-point arithmetic, stereo
  prediction and mid/side unmixing, and libopus's SILK resampler from 8, 12
  or 16 kHz to 48 kHz. Its 16-bit output becomes float as in libopus
  (x/32768). 40 and 60 ms packets are two or three SILK frames sharing one
  range decoder.
- **CELT** (`celt.Decoder.DecodeWithEC`, libopus's `celt_decode_with_ec`)
  for CELT frames, and for the bands from 17 up in hybrid frames, where its
  output is added to SILK's: band energies, bit allocation, PVQ band
  decoding with anti-collapse, denormalisation, the inverse MDCT on libopus's
  KISS FFT, the comb post-filter and de-emphasis.

Around them, `opus_decode_frame` handles the CELT redundancy frames at mode
switches and their cross-fades, the 2.5 ms fade from the previous mode's
concealment into a new mode when there is no redundancy, the CELT silence
frame that fades out the MDCT after a hybrid-to-SILK switch, and the final
range (the range decoder's state, XORed with the redundancy frame's).

**Loss.** `Conceal(samples, out)` is `opus_decode_float` without a packet. It
conceals in the last packet's mode and frame size, in steps of at most 20 ms,
as libopus does: SILK with its pitch and LPC extrapolation and comfort noise,
CELT with pitch-based concealment that turns to noise after 100 ms of loss
(and uses noise from the start in the CELT layer of hybrid frames).
Before any packet it returns zeros. Packets of zero or one byte per frame
(DTX) conceal the same way.

**In-band FEC.** A SILK or hybrid packet can carry LBRR frames, a
low-bitrate copy of the audio just before it (RFC 6716 §4.2.5). Decoding
parses and skips them. `DecodeFEC(next, samples, out)` is `opus_decode_float`
with `decode_fec = 1`, ported from `opus_decode_native` and
`opus_decode_frame`: when `next` is SILK or hybrid, the last decoded packet
was not CELT-only and the lost interval is at least `next`'s frame duration,
it conceals all but that last part as `Conceal` does, takes `next`'s mode,
bandwidth, frame size and coded channels, and decodes the part from `next`'s
first frame with SILK's LBRR path (`silk_Decode` with `FLAG_DECODE_LBRR`,
which conceals the SILK frames that carry no LBRR data); the CELT layer of a
hybrid frame is concealed, and no redundancy is read. Otherwise it conceals
the whole interval, exactly as `Conceal`. The final range is the range
decoder's after the LBRR data, or 0. Like `opus_decode_native`, it rejects a
`next` that does not parse before it changes anything.

**Output.** The root `Decoder` drops the pre-skip from the start, the end
trimming from the last packet, applies the header output gain, and resamples
each channel to 16 kHz when asked to. `Position` counts what `Read` has
delivered.

## Streaming and containers

The output timeline starts at the first sample after the pre-skip. Each
packet is decoded to 48 kHz and end-trimmed if the container says so (the
final Ogg granule position, a WebM `DiscardPadding`), the pre-skip is dropped
from the first packets, the output gain is applied, and the result is
optionally resampled to 16 kHz. Chained Ogg Opus links reset the codec and
apply their own pre-skip and gain on the same output timeline; the resampler
runs on uninterrupted. A link with a different channel count is
`ErrUnsupported`, which ends decoding there.

The final Ogg page's granule position sets how many of its samples remain
(RFC 7845 §4.4), so the Ogg reader reads a whole page before it returns the
page's first packet, and the end trimming spreads over the page's packets
from the last one back: a packet trimmed whole contributes nothing and keeps
a `Trim` of its coded duration. ffmpeg trims the same, except for a final
granule position below the pre-skip, which RFC 7845 §4.5 makes the stream
invalid: puropus then returns no audio, ffmpeg the first packet after its
pre-skip.

WebM pre-skip comes from the `OpusHead` in `CodecPrivate`, with `CodecDelay`
as a fallback when there is no `OpusHead`. A block's `DiscardPadding` drops
audio from its end when positive and from its start when negative
(Matroska), on any block; the start's samples are the packet's `Skip`. On
the first block they come before the pre-skip, which still applies: the
Matroska specification defines the two as separate discards and does not
combine them. ffmpeg does not agree with itself there (6.1.1 drops 480
samples in all for a pre-skip of 312 and a padding of -480, n9.0.2 only the
pre-skip), and decodes negative padding elsewhere as puropus does. The WebM
reader handles
unknown-size Segment and Cluster elements, as live recordings have them,
`SimpleBlock`s and `BlockGroup`s, and skips other tracks; laced blocks are an
error. The Ogg reader selects the Opus stream among grouped logical streams
and reads chained links in sequence.

A page with a bad checksum or version is dropped, and the reader
resynchronizes on the next capture pattern, looking in the dropped page's own
bytes first so that a corrupt page size cannot swallow the pages after it. A
dropped page could belong to any stream, since its serial number is as
untrustworthy as the rest of it. So an Opus packet that spans pages is
dropped only when the Opus stream's own page sequence numbers show that the
stream lost a page, and then its fragments are discarded, never joined. A
corrupt page of another grouped stream costs no audio.

RFC 3533 places all BOS pages of a link before its other pages, so a BOS
page that follows the Opus stream's data starts the next chained link even
when the Opus stream's EOS page was lost. The file then decodes as if the
link had ended on the page before: the lost page's packets are missing, as
is the end trimming its granule position set, and the next link's header
applies. A chained link whose OpusTags page was lost is decoded too;
the first link must have its OpusTags, or `NewDecoder` fails. A file in
which no BOS page carries OpusHead, because it has no Opus stream or because
its only OpusHead page is corrupt, is `ErrUnsupported`, and the error counts
the corrupt pages dropped; one that ends after its OpusHead is
`ErrTruncated`. ffmpeg handles these cases the same
way, except that it joins a packet's fragments across a lost page, into a
packet that fails to decode (its native decoder renders it as silence); it
loses the pages swallowed by a corrupt page whose size runs past the end of
the input; it cannot read a grouped chained link at all; and ffmpeg 6.1.1,
though not n9.0.2, stops at a chained link without OpusTags.

A packet that cannot be parsed stops decoding, or is skipped in `Tolerant`
mode. One that parses but cannot be decoded stops decoding too, or in
`Tolerant` mode is lost for its duration, which the TOC gives. The decoder
then reads the next packet, waiting for it as `Read` does, and keeps it to
decode next. When that packet belongs to the same Ogg link and `HasLBRR`
reports in-band FEC in it, the lost packet is decoded with `DecodeFEC` from
it, as a libopus application recovers a lost packet (`opus_decode_float`
with `decode_fec = 1` for the lost duration, then the next packet with
`decode_fec = 0`); libopus itself may still conceal, as described above.
Otherwise, also at the end of the input and before an error, which the next
`Read` reports, the packet is concealed. Only the audio of the lost packet
waits for the next packet; positions, trimming and retained packets are
those of the stream. Input that ends inside a page or element, or between
elements before the end its known-size WebM Segment declares, yields
everything decodable, the resampler's tail included, and then
`ErrTruncated`. A live recording's unknown-size Segment ends wherever the
input does.

The containers' own timestamps are not used to find lost packets: a WebM
block or an Ogg page that is missing altogether (rather than present and
undecodable) leaves no gap in the decoded timeline, which simply continues
with the next packet, so neither concealment nor FEC applies to it. A page
the Ogg reader drops for a bad checksum is such a gap.

The WebM writer produces the single-track, unknown-size-Segment, 1 ms
timestamp profile that browsers emit, splitting clusters before a relative
block timestamp could exceed 30 s. The Ogg writer produces pages of about one
second with granule positions counting 48 kHz samples; the serial is a hash
of the packets, so identical streams write identical bytes.

## Allocation

`opus.NewDecoder` allocates all codec state at once: the SILK and CELT
decoders are values inside `opus.Decoder`, and their scratch memory is
arrays in them, not locals that escape. Decoding, concealment and FEC
decoding allocate nothing per packet, in every mode, for multi-frame packets and mode switches,
mono and stereo (`TestDecodeDoesNotAllocate` in `opus` and `celt`,
`TestDecodeAllocs` in `silk`). CELT's tables are built once, at the first
decoder's initialization. The resampler grows its buffer to the largest
input chunk and reuses it.

By default the container readers return each packet in its own slice, which
`KeepPackets` needs. When packets are not kept, `puropus.Decoder` switches
them to reused buffers (`ReusePacketData` in `internal/ogg`,
`ReuseBlockData` in `internal/webm`), and `Read` then allocates nothing per
packet at 48 or 16 kHz, from Ogg or WebM, mono or stereo
(`TestReadDoesNotAllocate`). `KeepNative` keeps the decoded PCM in
one-second blocks, so a long stream does not repeatedly regrow one slice.

## Rejected packets

`DecodeParsed` returns an error, leaving the decoder state exactly as it was,
for a packet that does not parse, a `Packet` that is not what `Parse` returns
for the data, and an output buffer that is too short. It parses the data
again and compares the result field by field, which costs nanoseconds
against the microseconds of decoding, so metadata that stays inside the data
but describes another frame layout is rejected too
(`TestDecodeParsedRejectsMetadataParseDoesNotReturn`). It does all this
before it changes anything. It also rejects a frame whose CELT redundancy is longer than the
frame itself. libopus catches that case with a sanity check in
`opus_decode_frame` ("it should never happen for a valid packet"), drops the
redundancy and goes on. puropus rejects the packet instead, so that a caller
can conceal it like a lost one: in `Tolerant` mode the output is then the
same as for the stream with that packet lost, recovered from the next
packet's in-band FEC where it carries some
(`TestStereoRejectedPacketPreservesRecovery`).

That check fires only in hybrid frames, after SILK has decoded the frame
and after the packet's earlier frames have run CELT. So `DecodeParsed`
snapshots the decoder for hybrid packets alone: the top-level state, the
SILK state, and the CELT state too when the packet has more than one frame
(the first frame reaches the check before it touches CELT). It restores the
snapshot on rejection. CELT-only packets have no redundancy, and a SILK-only
frame's redundancy is exactly the bytes after its SILK data, so once those
packets parse their decoding cannot fail, and they pay nothing. Random
packets check that (`TestCELTPacketsDecodeWithoutError`,
`TestSILKPacketsDecodeWithoutError`, `FuzzDecode`), and
`TestRejectedRedundancyLeavesStateUnchanged` checks the restored state, the
final range and the next packet's output against a decoder that never saw
the rejected packet.

## Editing

Packets are positioned on the decoded timeline. Each `Packet` records the
samples it contributes (`Start`, `Duration`) and the coded samples at its
edges that the container discards (`Skip` for pre-skip, `Trim` for an Ogg
final granule or WebM `DiscardPadding`), so packets tile the timeline from 0
and `Source.Duration()` is exactly what `Read` delivers. A packet that lies
entirely inside the pre-skip contributes nothing. Chained Ogg links continue
the same timeline, the boundary marked by the first link's `Trim` and the
second's `Skip`. `Source.Native`, when kept, starts at `Packets[0].Start`, and
`ReleasePackets` drops native PCM only up to the first packet it retains, so
the two never drift apart. A kept `Source.Native` is not nil even when the
packets decode to nothing, so `Cut` can tell that case from PCM that was
never kept. `ReleasePackets` also keeps packets inside the pre-skip for as
long as it keeps the packet after them, whose decoder warm-up they carry, as
`Cut` keeps them with it.

`Cut` works on packets, never on audio. Intervals are converted to 48 kHz
sample positions, extended, clamped to the retained packets, sorted, and
merged. Each region then selects packets by overlap (`SnapOutward`),
containment (`SnapInward`), or, with an `Encoder`, by copying the interior and
re-encoding the guards (`SnapExact`). In the first two modes a kept first
packet brings along any packets before it that lie inside the pre-skip, so the
decoder warm-up they carry is kept too. Exact guards are the stretches between
the interval boundary and the first or last copied packet, at least
`Edit.Guard` (80 ms by default) and rounded to the nearest 2.5 ms so the
encoder receives whole Opus frames; the `Encoder` contract makes the
implementation responsible for its own algorithmic delay, since only it knows
the delay and its frame sizes.

Selected packets are laid on a fresh continuous timeline. Pre-skip survives
only on the leading packets of the output and end trimming only on its last
packet, because that is all Ogg and WebM can express; the writers put them in
`OpusHead`, `CodecDelay`, the final granule position, and a `DiscardPadding`
on the last block. Skipped or trimmed samples anywhere else (a source's first
packet after inserted silence, its last packet before trailing silence, a
chained-link boundary) are emitted as audio and mapped as synthetic spans:
they are the encoder's warm-up or padding, so the timeline stays exact at the
cost of a few milliseconds of near-silence.

Every region that selects packets contributes a `Span` recording where its
output came from, so a consumer can map transcript timestamps back to the
recording. Silence is inserted only around contributing regions and is marked
synthetic. Its packets (`opus.Silence`) are CELT frames whose only coded
symbol is the silence flag, at the bandwidth of the neighbouring packet. An
empty selection has no spans or silence and cannot be written as a container
stream.

## Correctness

The tests compare the decoder with libopus 1.5.2 at several levels, and the
16 kHz output with FFmpeg's resampler. The references are committed; the
programs in `tools/` that made them are described in
[`tools/libopus-ref/README.md`](../tools/libopus-ref/README.md). Two builds of
libopus serve as references: the **generic** build, compiled from libopus's C
code alone without intrinsics or fused multiply-adds, which is the code the
port follows, and the **default** build, libopus as CMake builds it, with its
x86 SIMD kernels chosen at run time.

1. **Stages.** The range decoder (every symbol kind, truncated and random
   input), the packet parser (every TOC byte, every framing code, damaged
   packets), the CELT stages (PVQ index decoding, bit allocation, energy
   decoding, band decoding, anti-collapse, denormalisation, the inverse MDCT
   for every size and stride, the comb filter, de-emphasis and the
   concealment's pitch and LPC helpers) and the SILK stages (NLSF decoding
   and conversion to LPC, gains, pitch, the shell and pulse decoders, stereo
   unmixing, the resampler from every internal rate to every output rate)
   reproduce libopus's values bit for bit, as do the SILK decoder's state
   after voiced, unvoiced and inactive frames and after concealment.
2. **Frames.** The CELT decoder, driven directly over every CELT stream as
   `opus_decode_frame` drives it, matches the generic build's final range
   after every frame and, on amd64 and 386, the hash of every frame's PCM,
   also at the other output rates of `celt_decoder_init` and with start band
   17 as in hybrid frames. The SILK decoder matches libopus's final range
   after every packet and its output sample for sample, through loss,
   comfort noise and in-band FEC. Only the eight packets of the
   bandwidth-switch fixture that carry a CELT redundancy frame, which
   package `silk` cannot decode alone, are left out there; package `opus`
   checks them (item 4).
3. **Fixtures** (`testdata/`, provenance in `testdata/manifest.json`). Ogg and
   WebM streams cover every SILK and hybrid configuration and CELT in 10, 20,
   40 and 60 ms packets, with in-band FEC, a SILK/CELT mode switch and a SILK
   bandwidth switch. At 48 kHz they are compared with the default build's
   decode; at 16 kHz with FFmpeg n9.0.2's default resampling of it. Every
   comparison requires the exact sample count, RMS error ≤ 1e-6 and peak
   error ≤ 1e-4, with no alignment shifts or skipped edges. Measured: the
   SILK fixtures are bit-identical, the others within RMS 1.6e-8 and peak
   1.8e-7 at 48 kHz, and every fixture within RMS 2.0e-8 and peak 2.4e-7 at
   16 kHz. The CELT fixtures also keep FFmpeg's native decode as a black-box
   comparison that is reported, not gated (RMS 3.2e-8, peak 1.8e-7). The
   same fixtures drive the stream API through slow input, odd read sizes,
   output gain, chained links, packet retention, strict and tolerant errors,
   and Ogg and WebM cuts.
4. **Decode logs and FEC** (`opus/testdata/decode-logs`). The generic
   build's record-by-record decode (kind, sample count, final range and PCM
   hash) of 52 cases, which `opus` replays with `Decode`, `Conceal` and
   `DecodeFEC` bit for bit, final ranges included: every SILK and hybrid
   fixture and the SILK/CELT switch fixture without loss (the eight
   redundancy packets above included), and 30 cases of loss on the FEC
   fixtures, the channel switch sequences decoded to mono and stereo, the
   mode switch and loss sequences, and a splice of fixtures that switches
   between SILK NB, MB and WB, 20 and 60 ms frames, hybrid FB and CELT FB:
   single losses, bursts, the first and the last packet, losses before
   packets without LBRR data, before and after a CELT packet, before a
   longer frame, before TOC-only and empty-frame packets, and across every
   kind of switch. Lost packets are recovered with FEC as the `Tolerant`
   decoder does it (when `HasLBRR`, 215 FEC decodes of which 195 decode LBRR
   data) or whenever a packet follows, as `opus_demo` does. The root
   package's `TestTolerantRecoversFromFEC` turns the losses of the `Tolerant`
   cases into undecodable packets of Ogg and WebM streams and requires the
   decoder's 48 kHz output to have libopus's hash, and the 16 kHz output to
   be its resampling.
5. **Loss and transitions** (`opus/testdata/transition-vectors`). Thirteen
   libopus sequences (732 packets and losses) cover single losses and loss
   runs at frame sizes from 2.5 to 60 ms, a run of twelve losses, DTX
   (TOC-only packets, zero-length frames, encoded DTX in quiet input),
   recovery, mode switches, and a malformed packet that libopus rejects,
   which must be rejected with the final range unchanged. Results, sample
   counts and final ranges are exact. Every frame's PCM must be within RMS
   1e-6 and peak 1e-4 of the generic build, and is in fact bit-identical to
   it in the scalar, SIMD, `GOAMD64=v3` and 386 builds. Against the default
   build the same limits apply, except to CELT pitch concealment and the
   frames recovering from it, which allow RMS 3e-4 and peak 5e-4: the two
   libopus builds differ there by up to RMS 2.60e-4 and peak 4.79e-4 (twelve
   losses in a row), mostly from the summation order of the SSE
   `xcorr_kernel` in `celt_fir` and `celt_iir`; on every other frame they
   are within RMS 1.8e-8 and peak 1.2e-7 of each other.
6. **Stereo** (`opus/testdata/stereo-vectors`). Fifteen libopus sequences,
   decoded to mono and to stereo, cover changes of the coded channel count
   with FEC, mode and bandwidth switches, losses of 2.5 to 60 ms frames,
   transients and DTX, with the same gates. A band-domain vector checks the
   order of CELT's anti-collapse noise across both channels, and Ogg and WebM
   tests compare 16 kHz stereo output with FFmpeg resampler goldens.
7. **Official vectors.** With `OPUS_TEST_VECTORS` pointing at the RFC 8251
   corpus, `TestOfficialOpusVectors` checks the files' hashes, the final
   range of every packet, exact sample counts and finite output for all
   twelve streams, mono and stereo; together they cover all 32
   configurations. Packages `silk` and `celt` also decode the SILK-only and
   CELT-only vectors on their own: `silk` compares its output with
   libopus's bit for bit, `celt` every frame's final range (and its PCM, with
   the references `make celt-official-refs` builds).
   `TestOfficialOpusVectorsFEC` loses three of every seven packets of each
   vector and decodes every loss that a packet follows with `DecodeFEC`
   (5,733 calls per output channel count, in all configurations, none with
   LBRR data), to mono and to stereo; the record-by-record log must hash as
   the generic build's. The RFC's waveform
   check, the official `opus_compare`, needs the C reference tools and runs
   outside the repository: all twelve vectors pass it, mono and stereo, in
   both builds.
8. **Malformed input.** Fuzz targets cover both container readers, the
   packet parser and the Opus (packets and FEC decoding), CELT and SILK
   decoders, and truncated or corrupt containers have explicit tests.

The SIMD build is held to the same tests, plus each kernel against the
scalar code it replaces; [simd.md](simd.md) describes them. On other
architectures than amd64 and 386, whose floating-point results (fused
multiply-adds, the math library) need not round as on x86 with glibc, the
CELT frame hashes and the decode logs' PCM hashes are reported rather than
required, except for the cases whose PCM is integer SILK output; the PCM
gates still apply.
