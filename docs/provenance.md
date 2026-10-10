# Provenance

Where puropus's code comes from, and which licence covers each part.

## The codec: a port of libopus

The Opus decoder is a port of the float decoder of
[libopus](https://opus-codec.org/) 1.5.2 (BSD-3-Clause), written from the
libopus source and [RFC 6716](https://www.rfc-editor.org/rfc/rfc6716). It
consists of:

- the range decoder, CELT and SILK: `opus/internal/entcode`,
  `opus/internal/celt` and `opus/internal/silk`;
- the decoder logic in package `opus`: packet parsing, frame decoding with
  its mode transitions and redundancy, LBRR detection and the conversion of
  SILK output to float (`decoder.go`, `packet.go`, `lbrr.go`, and
  `silk_pcm.go` with its vector kernel).

The vector kernels in these packages are vector forms of the same libopus
loops and give the same results bit for bit.

Each ported file starts with the copyright and licence notice of the libopus
source files it follows, then a line naming them, for example
`// Ported from libopus 1.5.2 celt/bands.c and celt/vq.c.`
[`NOTICE`](../NOTICE) lists these files and reproduces libopus's licence,
which continues to cover them.
[`opus/internal/README.md`](../opus/internal/README.md) maps the packages to
the libopus sources.

## The 16 kHz resampler: an original design

`internal/resample` is an original windowed-sinc design: a 100-tap
Kaiser-windowed sinc low-pass filter evaluated at every third input sample.
It matches the output of FFmpeg's default resampler for 48 to 16 kHz. Its
parameters, its alignment, its handling of both stream ends and its output
count were learnt by running the FFmpeg binary on crafted inputs: impulses
at chosen positions, constants of thousands of lengths, ramps and noise.
[`internal/resample/DESIGN.md`](../internal/resample/DESIGN.md) records the
measurements, and `tools/resample-measure` repeats them.

## What was not used

The implementers of the codec and the resampler did not consult FFmpeg's
source code, other Go implementations of Opus, or any LGPL- or
GPL-licensed Opus code. FFmpeg binaries were used only as black boxes, to
produce outputs to compare with.

For the performance work, a separate analyst described in prose, without
code, the optimisation techniques of one BSD-licensed Go implementation
(thesyncim/gopus); the implementers read only that description.

An independent review compared the code with FFmpeg's decoder source and
found no code derived from it.

## Everything else: original

The containers (`internal/ogg`, `internal/webm`), the streaming decoder,
cutting and writing in the root package, the command line (`cmd/puropus`),
the header and silence helpers of package `opus` (`head.go`, `silence.go`)
and the tests are original puropus code under the MIT licence
([`LICENSE`](../LICENSE)).

The reference outputs the tests compare with were produced with libopus,
through the C programs in [`tools/libopus-ref`](../tools/libopus-ref/README.md),
and with FFmpeg binaries; the manifests next to them (for example
`testdata/manifest.json`) record how. The RFC 8251 test vectors are not part
of the repository.
