# SIMD kernels

The scalar build is the reference. With `GOEXPERIMENT=simd` on Go 1.27 and
amd64, the kernels below are chosen at run time from the CPU features that
`simd/archsimd` reports (`GODEBUG=cpu.*` settings included); the scalar code
stays in the binary, and the kernels are tested against it. Other
architectures, and builds without the experiment, compile the scalar code
alone.

Every kernel of the codec computes exactly what the scalar code computes, so
the 48 kHz output of the SIMD build is the scalar build's bit for bit. Only
the 16 kHz resampler has kernels that round differently: its tiers with fused
multiply-adds.

## Dispatch

| Package | Kernels | Vectors | Needs | Without it |
| --- | --- | --- | --- | --- |
| `opus/internal/celt` | the FFT's radix-2, 3, 4 and 5 stages; the inverse MDCT's pre-rotation, post-rotation and mirror, for long and short blocks; the stereo comb post-filter fused with de-emphasis; haar1 at strides 1 and 2; band denormalisation; PVQ normalisation (`normalise_residual`); the scaling of the folding copy in `quant_band` | 256-bit float (the post-filter 128-bit) | AVX2 | scalar |
| `opus/internal/silk` | the resampler's FIR interpolation from 8, 12 and 16 kHz to 48 kHz; the 2× all-pass upsampler (`up2_HQ`); mid/side to left/right; interleaving the two channels | 128-bit integer | AVX | scalar |
| `opus` | the SILK output's int16-to-float conversion, stored or added to the CELT output | 128-bit | AVX | scalar |
| `internal/resample` | the 100-tap 48 → 16 kHz FIR, four outputs at a time | 512-bit with FMA; 256-bit with FMA; 256-bit; 128-bit | AVX-512 + AVX2 + FMA; AVX2 + FMA; AVX2; AVX | scalar |

Each package decides once, at start-up, from `archsimd.X86`; each kernel's
caller checks a package-level flag and calls the kernel or the scalar loop.
The packages report their choice for tests: `celt.Acceleration()`
(`simd-amd64-avx2` or `scalar`), the internal `accel()` of `silk` and
`pcmAccel()` of `opus` (`simd-amd64-avx` or `scalar`), and
`resample.Acceleration()` (`simd-amd64-avx512`, `simd-amd64-avx2-fma`,
`simd-amd64-avx2`, `simd-amd64-avx` or `scalar`), which is also what
`puropus.Acceleration()` returns.

The CELT kernels run where the work fills vectors: the FFT in every inverse
MDCT, the rest of the IMDCT with the static mode's window, haar1, the folding
copy and PVQ normalisation from 16 values up, denormalisation in 20 ms
frames, and the post-filter in stereo; elsewhere the scalar code runs. The other kernels run on every call. Values that do not fill
a vector go through the scalar code, except in the resampler, which runs its
trailing outputs through the same vector code on a zero-padded copy so that
grouping never changes the bits.

### Why AVX2 for CELT and only AVX for SILK

Go's asynchronous preemption saves and restores the upper halves of the
vector registers only for the widths the CPU reports: the YMM registers when
it has AVX2, the ZMM registers with AVX-512, otherwise only the XMM registers.
A 256-bit kernel dispatched on AVX alone (a CPU with AVX but not AVX2, or
`GODEBUG=cpu.avx2=off`) could therefore be preempted and resume with
corrupted upper lanes. The CELT kernels and the resampler's 256-bit tiers use
`Float32x8`, so they need AVX2; the 512-bit resampler tier needs AVX-512.

The SILK kernels, the int16-to-float conversion and the resampler's AVX tier
use 128-bit VEX instructions. VEX-encoded 128-bit operations zero the upper
halves of the registers they write, so these kernels never leave state that a
preemption with only the XMM registers saved could lose, and they need only
AVX. The CELT post-filter is 128-bit too, but it follows the package's single
AVX2 switch.

### Dirty upper state

Go compiles scalar float code, and its own spills, copies and zeroing, with
legacy SSE instructions. On Intel cores, each legacy SSE instruction that runs
while the upper halves of the YMM registers are dirty, that is after a
256-bit operation and before a `VZEROUPPER`, costs a microcode assist. In
`GOEXPERIMENT=simd` builds an asynchronous preemption also returns with the
upper halves dirty. So the kernels clear the state
(`archsimd.ClearAVXUpperBits`, which is `VZEROUPPER`) wherever scalar code
could follow vector code:

- **CELT**: every kernel ends with it. The IMDCT also clears before the
  scalar tail of its pre- and post-rotation, and the FFT before the scalar
  remainder of its radix-4 stage with m = 1. haar1, the folding-copy scaling
  and PVQ normalisation run the values that do not fill a vector *before* the
  vector loop, so no scalar code runs in between. The IMDCT pre-rotation
  scatters its results in bit-reversed order through integer moves (the
  vectors are stored with `ToBits` and read back with
  `math.Float32frombits`), so its scatter loop runs no float instructions
  between the 256-bit operations.
- **SILK and the int16-to-float conversion**: every kernel *starts* with it,
  because its own code may contain legacy SSE spills and copies and a
  preemption may have left the state dirty, and `silk.Decoder.Decode`
  clears it once per frame for the scalar code that follows.
- **Resampler**: each kernel call ends with it, and the group of trailing
  outputs clears it before copying its results.

The CELT stereo post-filter runs its scalar de-emphasis recurrences inside
the vector loop; that is safe only because its comb filter uses 128-bit
operations, which leave the upper halves clean.

## Bit-exactness

The codec kernels compute every lane with the arithmetic of the scalar code:
the same operations in the same order, no fused multiply-adds (the scalar
code rounds every product), sign flips and swaps as exact bit operations.
SILK's kernels do integer arithmetic whose sums fit (the FIR's eight-tap
`VPMADDWD` sums cannot overflow `int32`) and saturate as `silk_SAT16` does.
So the SIMD build's 48 kHz output equals the scalar build's on every
fixture and sequence, at `GOAMD64=v1` and `v3`.

The resampler's AVX and AVX2 tiers multiply and add separately and match the
scalar kernel bit for bit. The AVX2 + FMA and AVX-512 tiers fuse each
multiply-add and differ from the scalar kernel by rounding: within
32·2⁻²⁴ of the sum of the absolute products per output, the bound the tests
use; on the decoder's 16 kHz output for the repository's fixtures, by at
most RMS 1.3e-8 and peak 1.2e-7. All tiers are gated against FFmpeg's resampler output, and the FMA
tier reproduces FFmpeg n9.0.2's bits on the machine that made the goldens
([internal/resample/DESIGN.md](../internal/resample/DESIGN.md)).

## Tests

- **Against the scalar code.** Each kernel has a test against the scalar code
  it replaces. The CELT tests (`TestFFTAVX2MatchesScalar`,
  `TestMDCTBackwardAVX2MatchesScalar` and the others in
  `fft_simd_amd64_test.go`) require identical bits on random input of varied
  scale mixed with signed zeros, subnormals and large values; the fused
  post-filter is checked against the separate comb filter and de-emphasis
  passes. The SILK and int16-to-float tests run every length up to the
  largest batch at every alignment, and the scalar SILK stages are in turn
  checked against libopus's loops (`kernels_test.go`). The resampler's
  kernels run on exactly sized input, so an over-read panics, and fused tiers
  are held to the bound above.
- **Across CPU tiers.** `task test:simd:noavx` builds with `GOAMD64=v1` and
  reruns the kernel, dispatch, golden and reference-sequence tests of each
  package under `GODEBUG` settings that hide one CPU feature after another
  (`cpu.avx512f=off`, then FMA, AVX2 and AVX), so every tier the CPU can
  reach, down to scalar, runs; a tier test in each package
  (`TestTierMatchesCPU`, `TestSilkPCMTierMatchesCPU`) checks that the
  selection follows the features. A `GOAMD64=v3` build cannot hide
  AVX, AVX2 or FMA, so this task uses `v1`.
- **Under preemption.** `TestDecodeSurvivesPreemption` (in the root package,
  `opus` and `silk`) decodes CELT, hybrid and SILK streams, mono and stereo,
  on more goroutines than processors for 400 ms and requires every result to
  be bit-identical to a single decode. It runs under each `GODEBUG` tier, to
  catch a kernel that uses vectors wider than what its tier's preemption
  saves.

## Where the time goes

Measured on one P-core of an i9-14900HX (`taskset`), in milliseconds of CPU
per second of audio, medians of five interleaved rounds. The codec rows time
the packet decoder (`opus.Decoder`) alone; the last row is
`BenchmarkStreamTo16k`, the whole path from a WebM arriving through an
`io.Pipe` to 16 kHz PCM. The SIMD column holds for `GOAMD64=v1` and `v3`
alike, which measure within 0.5% of each other.

| Stream | Scalar | SIMD |
| --- | ---: | ---: |
| mono CELT fullband, 60 ms packets, codec only | 1.048 | 0.88 |
| stereo CELT, 128 kb/s | 2.03 | 1.70 |
| hybrid fullband, 20 ms, 32 kb/s | 1.15 | 0.88 |
| SILK wideband, 20 ms | 0.53 | 0.41 |
| SILK narrowband | 0.32 | 0.21 |
| SILK mediumband | 0.42 | 0.30 |
| stereo SILK wideband, 32 kb/s | 1.14 | 0.86 |
| `StreamTo16k`: WebM, mono CELT, 60 ms → 16 kHz | 1.42 | 0.96 |

CELT, mono fullband 60 ms, by stage (from profiles taken a few commits before
the final build, so approximate):

| Stage | Scalar | SIMD |
| --- | ---: | ---: |
| band shapes | 0.238 | 0.223 |
| inverse MDCT and FFT | 0.227 | 0.109 |
| PVQ index decode (`cwrsi`) | 0.185 | 0.183 |
| spreading rotation (`exp_rotation`) | 0.096 | 0.091 |
| comb post-filter and de-emphasis | 0.094 | 0.094 |
| range decoder | 0.063 | 0.062 |
| bit allocation | 0.041 | 0.042 |
| denormalisation | 0.035 | 0.025 |
| energy decoding | 0.029 | 0.028 |

The transform halves; little else moves. What remains is mostly serial
dependency chains: the `cwrsi` walk, the de-emphasis recurrence at about six
cycles per sample (a mono stream, so the post-filter stays scalar), and the
stride-1 spreading rotation, each step of which needs the one before.

SILK wideband by stage:

| Stage | Scalar | SIMD |
| --- | ---: | ---: |
| resampler FIR interpolation | 0.096 | 0.018 |
| LPC synthesis | 0.093 | 0.088 |
| range decoder | 0.073 | 0.068 |
| 2× upsampler (`up2_HQ`) | 0.071 | 0.058 |
| shell decoder | 0.053 | 0.049 |
| NLSF to LPC (`NLSF2A`) | 0.046 | 0.037 |
| signs | 0.029 | 0.033 |
| excitation | 0.022 | 0.018 |
| LTP synthesis | 0.014 | 0.010 |
| rewhitening | 0.013 | 0.009 |

The FIR interpolation, five times faster, is where the vectors pay; the
upsampler is a chain of all-pass sections, so its vector form gains only by
running the even and the odd output's chains side by side.

## What didn't work

SILK:

- `up2_HQ` in scalar Go with its state in locals: the compiler spilled them to
  the stack.
- Scalar variants of the FIR interpolation: all bound by the integer
  multiplier port.
- LPC synthesis in AVX: 1.2% faster on wideband, for about 130 lines of code
  whose speed depends on instruction scheduling. Not kept.
- Two LPC synthesis outputs per iteration: slower.
- A branch-free decode of the nine-entry inverse CDFs: no gain, because it
  lengthens the range decoder's dependency chain.

CELT:

- Finding `cwrsi`'s index by ranking it among eight thresholds at once: 3.6
  to 9% slower.
- Conditional moves over two variables: the compiler emitted branches, 4.6
  to 10% slower.
- Interleaving two `cwrsi` walks: 17% faster on the walk alone, about 2.5%
  end to end, not worth restructuring the band decoder for.

## Notes on Go SIMD

- `archsimd` does not emulate: an AVX2 or AVX-512 method on a CPU without the
  feature faults. Check the features before every entry into vector code.
- Match the vector width to what the runtime saves on preemption (see above):
  256-bit vectors need AVX2, 512-bit vectors AVX-512, and an AVX-only tier
  must stay at 128 bits.
- Clear the upper state before scalar code runs, and keep scalar float work
  out of 256-bit loops: run remainders first, or move results out as integer
  bits.
- With `GOAMD64=v3`, `GODEBUG` cannot turn off AVX, AVX2 or FMA, which the
  build requires; test the lower tiers with a `GOAMD64=v1` build.
  `cpu.avx512f=off` works at every level.
- Vector-typed package-level variables cannot be initialized; keep tables as
  plain arrays and load them into vectors where they are used.
- Slicing inside a vector loop costs a bounds check per load. Convert a
  window to a fixed-size array pointer once (`(*[24]int16)(buf[i : i+24])`)
  and index it with constants.
- Go does not unroll loops, and a nested loop with a constant trip count is
  still a loop; unroll by hand where it matters.
- To stay bit-exact with scalar code, do not fuse multiply-adds the scalar
  code rounds separately, and reduce lanes in the scalar code's order.
