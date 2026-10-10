# libopus reference tools

Small C programs around libopus 1.5.2 (git tag `v1.5.2`, BSD-3-Clause) that
produce the reference data the Go decoder is tested against. They are not
part of the Go module's packages and never run in `go test`; their output is
committed under `testdata/` directories with a note on how it was made.

The tools link one of two builds of libopus 1.5.2, both float:

- the **default build**, `$OPUS_BUILD/libopus.a`: libopus's CMake Release
  build with its default options, which detects the CPU at run time and uses
  its SSE, SSE2, SSE4.1 and AVX2 kernels on x86-64. This is the stock
  libopus most programs link. The CELT fixture goldens
  (`testdata/*.libopus48.f32le`) and the PCM of the sequences in
  `opus/testdata` come from it, and it reproduces the SILK and hybrid
  fixture goldens (`testdata/*.48k.f32le`) bit for bit.
- the **generic build**, `build/generic/libopus.a`, which `make GENERIC=1`
  compiles from `$OPUS_SRC` itself: libopus's own source lists, float, no
  intrinsics, no run-time CPU detection and no fused multiply-add
  contraction (`-ffp-contract=off`), so its arithmetic is that of the C
  source, the code the Go port follows. The `*.generic.f32le` references,
  the CELT frame logs and the CELT stage vectors come from it.

## Getting libopus

The tools need the libopus 1.5.2 source tree (git tag `v1.5.2`, commit
`ddbe48383984d56acd9e1ab6a090c54ca6b735a6`) and a CMake build of it. The
committed references were made with GCC 13 on x86-64.

```sh
git clone --branch v1.5.2 --depth 1 https://gitlab.xiph.org/xiph/opus.git opus-1.5.2
cmake -S opus-1.5.2 -B opus-1.5.2-build -DCMAKE_BUILD_TYPE=Release
cmake --build opus-1.5.2-build
export OPUS_SRC=$PWD/opus-1.5.2 OPUS_BUILD=$PWD/opus-1.5.2-build
```

([github.com/xiph/opus](https://github.com/xiph/opus) mirrors the
repository.) The CMake defaults are what the references need: a static
float library (`OPUS_FIXED_POINT`, `OPUS_BUILD_SHARED_LIBRARY`, `OPUS_DRED`,
`OPUS_OSCE` and `OPUS_CUSTOM_MODES` off, `OPUS_DISABLE_INTRINSICS` off).
Because the default build picks its kernels at run time, it may round
differently on a CPU with other vector extensions than the x86-64 machine
with AVX2 that made the references; the generic build does not depend on
the CPU.

## Building the tools

```sh
make                      # build/decode_float, build/entcode_ref, build/packet_ref, ...
make GENERIC=1            # build/generic/*: the same tools on the generic build
```

`OPUS_SRC` and `OPUS_BUILD` are required (as absolute paths, in the
environment or on the command line); `make` stops with a message when one is
missing. `GENERIC=1` needs only `OPUS_SRC`. Targets that compare the two
builds (`generic-vectors`, `celt-vectors`) build the generic library
themselves. Build output goes to `build/`, which git ignores. Besides a C
compiler and make, the scripts below need go, jq, xxd, gzip, cmp and
sha256sum, and `gen-goldens.sh` an `ffmpeg` binary.

## decode_float

The reference decode: `opus_decoder_create(48000, channels)` and
`opus_decode_float()` with `decode_fec = 0` for every packet in order,
writing interleaved float32 little-endian PCM at 48 kHz.

```sh
build/decode_float [-c channels] [-skip n] [-trim n] [-lost n] packets.bit out.f32le [ranges.txt]
```

- `-c` output channels, 1 (default) or 2, independent of the coded channels.
- `-skip n`, `-trim n` drop the first and last `n` samples per channel of the
  decoded stream (pre-skip and end trimming), so the output can be exactly
  what a container-level decoder returns.
- `-lost n` the duration of lost packets, see below.

`packets.bit` is the `opus_demo` bitstream format (also used by the RFC 8251
test vectors): a sequence of records, each

| bytes | content                                  |
|-------|------------------------------------------|
| 4     | payload length, big-endian               |
| 4     | final range, big-endian                  |
| n     | payload (the Opus packet)                |

A record with length 0 is a lost packet. It is decoded as
`opus_decode_float(st, NULL, 0, pcm, n, 0)`, where `n` is the record's range
field when nonzero, else `-lost n`, else the duration of the last packet
(`OPUS_GET_LAST_PACKET_DURATION`), else 960. For other records a nonzero range
field is checked against `OPUS_GET_FINAL_RANGE` as `opus_demo` does; a
mismatch is reported and makes the exit status 1, so
`build/decode_float testvector01.bit out.f32le` also verifies an official
vector. A packet libopus rejects stops the program with exit status 1.

`ranges.txt` receives one line per record: index, payload bytes, samples per
channel decoded, final range (hex). `decode_float -version` prints the libopus
version string.

## Fixture goldens

`tools/packets/main.go` (a Go program behind the `ignore` build tag, so
`go build ./...` and `go vet ./...` skip it) extracts the packets of an Ogg
Opus or WebM file with `internal/ogg` and `internal/webm` into the packet list
above and prints the `decode_float` options (`-c`, `-skip`, `-trim`) that make
the output what puropus's `Decoder` returns for the file at 48 kHz: the
OpusHead pre-skip (or WebM CodecDelay) and the end trimming from the final
Ogg granule position or the last WebM DiscardPadding, computed as the root
package does.

```sh
opts=$(go run tools/packets/main.go -o /tmp/x.bit testdata/ogg-celt-fb.opus)
tools/libopus-ref/build/decode_float $opts /tmp/x.bit /tmp/x.f32le
```

`gen-goldens.sh` (`make goldens`) does this for the CELT fixtures, writing
`testdata/<stem>.libopus48.f32le`, resamples each to 16 kHz with the FFmpeg
binary in `$FFMPEG` (default `ffmpeg` on `PATH`; the goldens were made with
FFmpeg n9.0.2) (`ffmpeg -f f32le -ar 48000 -ac 1 -i in -ar 16000 -f f32le
out`, the step that made every other `.16k.f32le`) into
`testdata/<stem>.16k.f32le`, and records the files and their SHA-256 in
`testdata/manifest.json`. Run on the SILK and hybrid fixtures, the same
pipeline reproduces their `.48k.f32le` and `.16k.f32le` goldens bit for bit.

## Generic-build decodes of the vector sequences (generic-vectors.sh)

`generic-vectors.sh` (`make generic-vectors`) turns each libopus sequence in
`opus/testdata/transition-vectors` and `opus/testdata/stereo-vectors` into a
packet list (packets with their final range, losses as empty records whose
range field is the concealed duration, packets libopus rejects left out),
checks that the default build's `decode_float` reproduces the committed PCM
byte for byte, and writes the generic build's decode next to it:
`<name>.generic.f32le` (and `<name>.generic.mono.f32le` for the stereo
sequences), with their SHA-256 in each directory's `manifest.json`. The tests
gate the Go decoder against both builds: strictly against the generic one,
which it follows; against the default one with a wider limit only where the
two libopus builds differ, in CELT pitch concealment and its recovery
(`opus/transition_vectors_test.go` has the measured distances).

## packet_ref

Reference results of libopus's packet parser for `opus.Parse` and
`opus.HasLBRR`: `opus_packet_get_bandwidth`, `opus_packet_get_samples_per_frame`
and `opus_packet_get_nb_channels` for every TOC byte, and
`opus_packet_parse_impl` (both framings) and `opus_packet_has_lbrr` for built
packets of every framing code, damaged packets and random bytes. The format
is documented at the top of `packet_ref.c`.

```sh
make packet-vectors       # writes opus/testdata/packet-parse/libopus.txt
```

## entcode_ref

Reference vectors for the range decoder `opus/internal/entcode`: it encodes
pseudo-random sequences of every symbol kind with libopus's range encoder
(`celt/entenc.c`), decodes them with libopus's range decoder
(`celt/entdec.c`) and prints each decoded value with `ec_tell`,
`ec_tell_frac`, `rng` and the error flag; it also decodes truncated
encodings and random bytes, and shrinks `storage` and advances
`nbits_total` the way `opus_decode_frame` and `celt_decode_with_ec` do. The
output is deterministic (fixed seed) and identical for the default and the
generic build. The format is documented at the top of `entcode_ref.c`.

```sh
make entcode-vectors      # writes opus/internal/entcode/testdata/entdec.txt
```

## Writing a libopus oracle test

For a codec stage (a CELT or SILK function), write a C program here that
calls the libopus function on fixed or seeded pseudo-random input and prints
or writes its output, add a Makefile rule (include `$(OPUS_SRC)/celt`,
`$(OPUS_SRC)/silk`, `$(OPUS_SRC)/silk/float` as needed; internal functions
are linkable from `libopus.a`, static ones must be reached through a caller
or by compiling the libopus `.c` file into the tool), commit the output under
the Go package's `testdata/` with a header line naming the tool and the
libopus version, and have the Go test replay it. Prefer the generic build
(`GENERIC=1`) when the stage has SIMD variants in libopus (pitch
correlation, inner products, the comb filter), and say which build produced
the data.

## SILK references (silk_ref, gen-silk-ranges.sh)

`silk_ref` produces the unit references of `opus/internal/silk`. It calls
libopus's internal SILK decoder functions (and `silk_shell_encoder` and
`silk_encode_pulses` to build test bitstreams) on seeded input:

- `silk_ref units`: `silk_NLSF2A`, `silk_NLSF_decode`, `silk_NLSF_stabilize`,
  `silk_LPC_inverse_pred_gain`, `silk_gains_dequant`, `silk_decode_pitch`,
  the shell and pulse decoders, `silk_stereo_MS_to_LR` and `silk_resampler`
  from every internal rate to every output rate up to 48 kHz.
- `silk_ref plc name=packets.bit ...`: channel states recorded while
  `silk_Decode` decodes SILK-only packet lists (after voiced, unvoiced and
  inactive frames) and reset states, the frames that the loss path of
  `silk_decode_frame` (PLC, CNG, frame gluing) conceals from them, and the
  state after four lost frames.
- `silk_ref loss name=packets.bit ...`: libopus's float decode of SILK
  fixtures with single losses and a loss run, concealed for the last packet
  duration, as an FNV-1a hash of every record's output with its sample count
  and final range.
- `silk_ref fec name=packets.bit ...`: the same with in-band FEC: a lost
  record followed by a received one is decoded from the next packet's LBRR
  data (`opus_decode_float` with `decode_fec = 1`).

The formats are documented at the top of `silk_ref.c`. The SILK decoder is
integer code, so the default and the generic build give identical output.

`gen-silk-ranges.sh` writes the final range of every packet of the SILK-only
fixtures (`decode_float`'s ranges output) after checking that `decode_float`
reproduces their 48 kHz goldens byte for byte, and, with `OPUS_TEST_VECTORS`
set, the SHA-256 of libopus's mono and stereo output for the SILK-only RFC
8251 vectors 02 to 04.

```sh
make silk-vectors         # opus/internal/silk/testdata/units.txt, plc.txt,
                          # loss.txt, fec.txt
make silk-ranges          # opus/internal/silk/testdata/ranges/*.txt (and
                          # official-vectors.txt with OPUS_TEST_VECTORS set)
```

## CELT (opus/internal/celt)

```sh
make celt-vectors         # regenerate opus/internal/celt/testdata/ (builds the generic tools)
make celt-official-refs OPUS_TEST_VECTORS=...   # into build/celt-official/, not committed
```

### celt_decode

The frame-level reference: libopus's CELT decoder driven directly
(`celt_decoder_init`, `celt_decode_with_ec` and the CELT CTLs, internal
functions `libopus.a` exports) over a packet list, the way `opus_decode_native`
and `opus_decode_frame` drive it for CELT-only packets: the end band from the
packet bandwidth, the coded channels from the TOC, start band 0, frames of at
most one byte and lost packets concealed in 20, 10, 5 or 2.5 ms steps, zeros
before the first frame. Its PCM equals `decode_float`'s for CELT-only streams;
`celt-vectors.sh` checks that on every CELT fixture.

```sh
build/generic/celt_decode [-c channels] [-r rate] [-s start] packets.bit out.f32le frames.txt
```

`frames.txt` has one line per CELT call: record, frame, kind (`frame`, `plc`
or `zeros`), bytes, samples, end band, coded channels, the CELT final range
(also after concealment, where `OPUS_GET_FINAL_RANGE` of the Opus decoder
reports 0) and the FNV-1a hash of the frame's PCM. The hashes of the generic
build are what the Go decoder reproduces bit for bit. `-r` selects another
output rate of `celt_decoder_init` and `-s 17` decodes every frame with start
band 17, as the CELT layer of hybrid frames.

### celt_ref

Stage vectors from the generic build: `decode_pulses`, `alg_unquant`,
`init_caps`, `clt_compute_allocation`, the coarse, fine and final energy
decoding, `quant_all_bands`, `anti_collapse`, `denormalise_bands`,
`clt_mdct_backward_c` for every size and stride, `comb_filter`, `deemphasis`
(static in `celt_decoder.c`, so the tool compiles its own copy with `RESYNTH`
defined) and the concealment's `pitch_downsample`, `pitch_search`,
`_celt_autocorr`, `_celt_lpc`, `celt_fir_c` and `celt_iir`, on seeded input.
The format is documented at the top of `celt_ref.c`; the output is
`opus/internal/celt/testdata/units.txt.gz`.

### celt-vectors.sh

Writes `opus/internal/celt/testdata/`: the packet lists of the CELT fixtures
(`tools/packets`) with their pre-skip and trimming (`fixtures.txt`), packet
lists made from the CELT sequences of `opus/testdata/transition-vectors` and
`stereo-vectors` (losses as empty records, packets libopus rejects left out;
`decode_float` on them reproduces those vectors' PCM), `celt_decode` frame logs
of the generic build for mono and stereo output and for the other output rates,
and `units.txt.gz`.

The SIMD build's PCM is the gated reference of the fixture goldens and
`celt-official-refs`; the sequences' PCM is gated against both builds (see
`generic-vectors.sh` above). The generic build differs from the SIMD one by
rounding in the vectorized kernels. In concealment the two builds can differ by
more than the CELT concealment gate: on twelve losses in a row
(`transition-loss-celt-long`) they are RMS 2.60e-4 and peak 4.79e-4 apart,
almost all from the summation order of the SSE `xcorr_kernel` that `celt_fir`
and `celt_iir` use.
