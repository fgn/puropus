# opus/internal: the codec core

Ports of the libopus 1.5.2 decoder (float build), following RFC 6716. This
file describes how the packages are written and how they fit together;
each package's documentation has its API. `docs/provenance.md` at the
repository root says where the code comes from.

| package   | follows (libopus 1.5.2)                                                  |
|-----------|---------------------------------------------------------------------------|
| `entcode` | `celt/entcode.[ch]`, `celt/entdec.[ch]`, `celt/mfrngcod.h`                 |
| `celt`    | `celt/celt_decoder.c`, `bands.c`, `vq.c`, `cwrs.c`, `rate.c`, `quant_bands.c`, `laplace.c`, `modes.c` + `static_modes_float.h`, `mdct.c`, `kiss_fft.c`, `pitch.c`, `celt_lpc.c`, `mathops` |
| `silk`    | `silk/dec_API.c` and the decoder files it reaches, `silk/resampler*.c` for the decoder rates, `silk/tables_*.c` |
| `opus` (parent) | `src/opus.c` (packet parsing) and `src/opus_decoder.c` (top level) |

## Conventions

- **Provenance.** Every Go file that ports libopus code starts with the
  BSD notice of the libopus file it follows (copy it, keeping the copyright
  lines), then a line `// Ported from libopus 1.5.2 celt/bands.c and
  celt/vq.c.` See `entcode/entcode.go`; `NOTICE` lists these files.
  Comments may cite libopus function names and RFC 6716 sections. The codec
  follows libopus and the RFCs only: do not bring in code from other Opus
  implementations.
- **Structure and names.** Keep libopus's functions, order and variable
  names, Go-cased: `quant_all_bands` is `quantAllBands`, `ec_dec_uint` is
  `DecUint`. Port the generic C paths with `FIXED_POINT` undefined (the
  `MULT16_32_Q15`-style macros are plain float arithmetic); leave out the
  encoder, custom modes, DRED, OSCE, deep PLC and `celt/x86`, `silk/x86`,
  `arm` code. `celt_decode_with_ec_dred` is `celt_decode_with_ec` without
  DRED.
- **Types.** `opus_val16`, `opus_val32`, `celt_sig`, `celt_norm` are
  `float32`. Use `int32` where libopus relies on 32-bit integer wraparound
  (SILK's fixed-point macros); `int` elsewhere. Tests also run with
  `GOARCH=386`, where `int` is 32 bits.
- **Floating point.** Go rounds every `float32` operation, like libopus on
  x86-64, but may fuse `x*y + z` into one FMA (arm64, ppc64le, s390x,
  riscv64, amd64 with `GOAMD64=v3`). An explicit conversion,
  `float32(x*y) + z`, forces the rounding where it matters; do not reorder
  sums.
- **Performance.** No allocation per packet: state lives in the decoder
  structs, allocated once by `opus.NewDecoder`, and scratch buffers are
  fields, not locals that escape. No interfaces, closures or `defer` in
  per-sample paths. Vector kernels (`GOEXPERIMENT=simd`) sit behind run-time
  dispatch, compute exactly what the scalar code does, and are tested
  against it; `docs/simd.md` describes them.

## The range decoder

`entcode.Decoder` is libopus's `ec_dec`. Pass it as `*entcode.Decoder` where
libopus passes `ec_dec *`; it is a plain value, so a package can embed one for
the frames it decodes itself (`celt_decode_with_ec` with `dec == NULL`)
without allocating.

| libopus                                   | Go                                  |
|-------------------------------------------|-------------------------------------|
| `ec_dec_init(&dec, buf, len)`             | `dec.Init(buf[:len])`               |
| `ec_decode`, `ec_decode_bin`, `ec_dec_update` | `Decode(ft)`, `DecodeBin(bits)`, `DecUpdate(fl, fh, ft)` |
| `ec_dec_bit_logp`                         | `DecBitLogp(logp)`, returns 0 or 1  |
| `ec_dec_icdf`, `ec_dec_icdf16`            | `DecICDF(icdf, ftb)`, `DecICDF16`   |
| `ec_dec_uint`, `ec_dec_bits`              | `DecUint(ft)`, `DecBits(bits)`      |
| `ec_tell`, `ec_tell_frac`                 | `Tell()`, `TellFrac()` (both `int`) |
| `ec_get_error`                            | `HasError()`                        |
| `dec->rng` (final range)                  | `Rng()`                             |
| `dec->storage`, `dec.storage -= n`        | `Storage()`, `SetStorage(Storage()-n)` |
| `dec->nbits_total += tell - ec_tell(dec)` | `SetTell(tell)`                     |
| `EC_ILOG`, `BITRES`                       | `entcode.ILog`, `entcode.BitRes`    |

## What package opus needs

From `src/opus_decoder.c`. The Go signatures are in each package's
documentation.

- **celt**: `celt_decoder_init(48000, channels)` with `CELT_SET_SIGNALLING(0)`;
  `celt_decode_with_ec(st, data, len, pcm, frame_size, dec, accum)` with
  `data == nil` for loss and `accum` to add to the SILK output in hybrid mode;
  `CELT_SET_START_BAND`, `CELT_SET_END_BAND`, `CELT_SET_CHANNELS` (coded
  channels), `OPUS_RESET_STATE`, `OPUS_GET_FINAL_RANGE`, `CELT_GET_MODE` (the
  window for `smooth_fade`) and `OPUS_SET_PHASE_INVERSION_DISABLED` (mono
  output).
- **silk**: `silk_InitDecoder`, `silk_ResetDecoder` and
  `silk_Decode(st, control, lostFlag, newPacketFlag, dec, pcm, &nSamplesOut)`
  with the `silk_DecControlStruct` fields `nChannelsAPI`,
  `nChannelsInternal`, `API_sampleRate` (48000), `internalSampleRate` and
  `payloadSize_ms`; `lostFlag` is `FLAG_DECODE_NORMAL` or `FLAG_PACKET_LOST`
  (FEC is parsed and skipped, never decoded).
- **both, not in libopus**: `SaveState` and `RestoreState`, plain copies of
  the state that carries over between calls (no scratch memory), with which
  `DecodeParsed` undoes a hybrid packet it rejects at `opus_decode_frame`'s
  redundancy sanity check.

## Tests against libopus

Unit tests compare with values libopus computes, produced by a small C
program in `tools/libopus-ref` (see its README): it calls the libopus function
on fixed or seeded input and prints the results; the output is committed in
the Go package's `testdata/` with a header naming the program and the libopus
version, and the test replays it. `entcode/entdec_test.go` with
`tools/libopus-ref/entcode_ref.c` is the pattern. Stages that libopus
vectorizes (pitch correlation, inner products, the comb filter) differ by
rounding between its SIMD and generic C builds; generate their data with
`make GENERIC=1` and say so. Whole-stream behaviour is gated by the black-box
tests in `opus/` and the repository root, whose goldens come from
`tools/libopus-ref/decode_float`; the loss and transition sequences of
`opus/testdata` also have the generic build's decode
(`tools/libopus-ref/generic-vectors.sh`), which the decoder matches bit for
bit.
