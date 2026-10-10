# 48 → 16 kHz resampler: design and measurements

`Resampler48To16` has to reproduce what

```
ffmpeg -f f32le -ar 48000 -ac 1 -i in.f32le -ar 16000 -f f32le out.f32le
```

produces: the same number of samples, and samples within RMS 1e-6 and peak
1e-4 of FFmpeg's, the first and last ones included. The resampler was designed
from textbook DSP (a Kaiser-windowed sinc low-pass FIR, decimated by 3). Its
parameters, its alignment, the treatment of both stream ends and the output
count were then pinned down by running the FFmpeg binaries as black boxes on
crafted inputs. No FFmpeg source code was read.

Binaries: FFmpeg n9.0.2 (the reference) and Ubuntu 24.04's FFmpeg 6.1.1
(package version 6.1.1-3ubuntu5). **They produced byte-identical output on
every probe below**, so there is no version difference to note.

`tools/resample-measure/measure.py` repeats the measurements and prints the
numbers quoted here (`python3 -I measure.py <ffmpeg> --alt <other ffmpeg>`,
numpy only). The Go tests pin the conclusions against committed goldens (see
"Tests").

## What the documentation says

`ffmpeg -h filter=aresample` (n9.0.2), `man ffmpeg-resampler` (6.1.1) and the
"Resampler Options" section of https://ffmpeg.org/ffmpeg-resampler.html give
the defaults of the built-in (`swr`) resampler: `filter_size` 32,
`phase_shift` 10, `exact_rational` enabled ("try to use exact phase_count
based on input and output sample rate"), `linear_interp` enabled, `cutoff`
0.97 for swr ("cutoff frequency (swr: 6dB point) ratio"; the n9.0.2 help lists
the option default as 0, i.e. the resampler's own default), `filter_type`
kaiser ("Kaiser windowed sinc"), `kaiser_beta` 9, no dither for float output.

For 48 → 16 kHz the ratio is exactly 3:1, so with an exact rational phase
count every output instant falls on an input sample: one filter phase, no
interpolation between phases (`linear_interp` and `phase_shift` play no
part). The expectation was therefore a single low-pass FIR with a Kaiser
window (β = 9), cut-off at 0.97 of the output Nyquist frequency (8 kHz), whose
output is taken at every third input sample. Its length and normalisation,
the time alignment and the edges had to be measured.

## Measurements

All probes are mono f32le streams resampled with the command above.

### Impulse response and alignment

Unit impulses at every position p = 0 … 599 of a 600-sample stream (600 runs
per binary). Away from the ends (150 ≤ p < 450) output m is non-zero only for
−50 ≤ d ≤ 49 with d = 3m − p, and its value depends on d alone (no
shift-variance in 300 × 200 outputs). So output m is a 100-tap FIR centred
exactly on input 3m, with no delay:

- h(d) = h(−d) bit for bit for |d| ≤ 49; one extra tap h(−50) = 2.91e-6
  (h(+50) = 0);
- h(0) = 0.32333672 ≈ 0.97/3, the gain of an ideal low-pass at 0.97 × 8 kHz;
- Σ h = 0.999999972 (unit DC gain up to float32 rounding of the taps).

### Filter design

Fitting h(d) = c · sinc(fc·d) · I0(β·√(1 − (d/W)²)), normalised to unit sum
over the 100 taps, one parameter at a time:

| half-width W | cut-off fc | β | max \|error\| | float32 taps equal |
|---|---|---|---|---|
| **50** | **0.97/3** | **9** | **1.3e-8** | **100/100** |
| 49.5 / 50.5 | 0.97/3 | 9 | 1.2e-4 | 0 |
| 48 / 51 | 0.97/3 | 9 | 4.9e-4 / 2.3e-4 | 0 |
| 50 | 0.965/3 / 0.975/3 | 9 | 1.7e-3 | 0 |
| 50 | 0.97/3 | 8.9 / 9.1 | 6.8e-5 | 0 |

With W = 50, fc = 0.97/3, β = 9, designed in float64, scaled to unit sum and
rounded to float32, all 100 taps equal the measured ones bit for bit. The
window reaches zero argument (I0(0) = 1, i.e. 1/I0(9) relative to the centre)
at d = ±50; d = −50 is inside the 100 taps and d = +50 is not, which explains
the single asymmetric tap.

The length follows `filter_size` F: with `-af aresample=filter_size=F` the
impulse response spans 26, 50, 76, 96, 100, 104, 124 and 198 taps for F = 8,
16, 24, 31, 32, 33, 40 and 64, the smallest even number ≥ F · 3 / 0.97 (F
periods of the cut-off frequency, in input samples). For the default F = 32
that is 100 taps, W = 50.

The implementation computes I0 with its power series and the taps with
`math.Sin`; `TestTapsMatchImpulseResponse` checks them against FFmpeg's
impulse response bit for bit. Every float64 tap lies at least 9.5·10⁶ float64
ulps from a float32 rounding boundary, so last-bit differences in the float64
design between platforms (fused multiply-adds on arm64 or s390x, other
`math.Sin` implementations) cannot change a float32 tap.

### Stream start

For an impulse at 1 ≤ p ≤ 49 the output contains, besides h(3m − p), a second
copy h(3m + p): the sample also acts at the virtual position −p. Trying the
virtual position c − p for c ∈ {−1, 0, 1}, the residual is 1.5e-8 (float
rounding) for c = 0 and 0.13 otherwise; p = 0 is not doubled. **Before the
first sample the stream is mirrored about sample 0: x[−k] = x[k].**

### Stream end

For an impulse at N − 50 ≤ p < N (N = 600) the extra copy sits at virtual
position 2N + k − p; the residual is 1.5e-8 for k = −1 and 0.07 … 0.26 for
k ∈ {−3, −2, 0, 1}. **After the last sample the stream is mirrored about
N − ½: x[N + j] = x[N − 1 − j]** (the last sample repeats once).

### Output count

Constant input of every length 0 … 2999, 4090 … 4299, 8191 … 8193 and
48000 … 48002 (3216 lengths; and every length of the random-signal checks
below):

```
count(N) = 0           N ≤ 66
           N/2 − 16    67 ≤ N ≤ 100   (integer division)
           (N+1)/3     N ≥ 101        (N/3 rounded to nearest)
```

with no exception. For example 66 → 0, 67 → 17, 99 → 33, 100 → 34, 101 → 34,
103 → 34, 104 → 35, 48001 → 16000, 48002 → 16001. For N ≥ 101 that is the
duration rounded to whole output samples; the last output is centred on input
N − 4, N − 3 or N − 2. Streams of 100
samples or fewer behave irregularly: nothing below 67 samples, then one output
per two input samples. Their values are the first count(N) outputs of the same
filter with the same mirrored ends, so only the count is unusual; the
mechanism behind it was not identified, and the formula is reproduced as
measured.

The n9.0.2 `f32le` demuxer delivers 4096-sample packets (`ffprobe
-show_packets`), so streams up to 4096 samples reach the resampler in one
piece. Lengths around 4096, 8192 and 12288 (packet boundaries) follow the same
formula and values; output does not depend on the packetisation.

### Values

Random uniform signals at every length 60 … 399 and at 1000, 4095 … 4097,
10000 and 48001, and DC, ramps and the goldens below: the model (taps above,
mirrored ends, count formula) evaluated in float64 differs from FFmpeg by at
most 1.3e-7 per sample (1 ulp near full scale), RMS ≤ 4e-8. A constant 0.5
comes out as exactly 0.5.

### Arithmetic

FFmpeg's float32 outputs are reproduced **bit for bit** (1000 of 1000 outputs
of a 3000-sample noise probe, and every golden below) by this evaluation
order: taps in ascending input order, tap i accumulated into lane i mod 8
with a fused multiply-add, the four remaining taps into lanes 0-3, then the
lanes folded by halves (8 → 4 → 2 → 1). Plain sequential float32 sums or other
lane widths differ by 1-3 ulp. This is what an 8-lane FMA evaluation on this
CPU (i9-14900HX, AVX2+FMA, no AVX-512) gives; FFmpeg on a CPU with other
vector extensions may round differently.

## Implementation

`resample.go` is the streaming logic, `filter.go` the filter design and the
scalar kernel, `accel_*.go` the dispatch and vector kernels.

- **Taps**: designed once at start-up as above; `taps[i]` weights input
  3m − 49 + i of output m.
- **Streaming**: the resampler buffers input from the first sample of the next
  output's window. Once the stream reaches 67 samples it prepends the mirror
  image x[49] … x[1]; from then on, every output whose 100-sample window lies
  entirely in received input is computed and returned. Such an output never
  depends on where the stream ends, and the count formula never retracts it
  (count(N) ≥ ⌊(N − 48)/3⌋ for N ≥ 67), so output is independent of how the
  input is split. Before 67 samples nothing is returned, because a stream of
  51 … 66 samples has complete windows but no output.
- **Flush** appends the 50-sample mirror image of the end and computes the
  remaining count(N) − emitted outputs, as many per call as fit in `out`;
  once drained it returns the resampler to its zero state. No remaining
  window reaches more than 49 samples past the end, and the buffer then holds
  the last 97 … 99 samples of the (start-mirrored) stream, at least 67 of them
  real, so the 50-sample mirror never needs samples that are gone.
- **Buffering**: the buffer holds at most 99 samples between calls (plus the
  pending input when `out` is too small, which is kept rather than dropped).
  It grows with the largest input chunk and is reused, so Process does not
  allocate in steady state. Counters are int64, so streams longer than 2³¹
  samples work on 32-bit platforms.
- **Kernels**: all evaluate the 100-tap dot product with tap i in lane
  i mod 8 and fold the lanes as ((s0+s4)+(s2+s6)) + ((s1+s5)+(s3+s7)).
  - `scalar`: eight float32 partial sums; products are rounded before they are
    added (an explicit conversion stops the compiler from fusing), so the
    result does not depend on the platform. Checked: identical output bits for
    amd64 at GOAMD64=v1 and v3 and for 386 with GO386=sse2 and softfloat.
  - `simd-amd64-avx` (128-bit, two 4-lane accumulators) and `simd-amd64-avx2`
    (256-bit): multiply then add, bit-identical to scalar.
  - `simd-amd64-avx2-fma`: fused multiply-add; bit-identical to FFmpeg on this
    machine, within 32·2⁻²⁴ Σ|tap·x| of scalar.
  - `simd-amd64-avx512`: 16 lanes folded to 8, then as above; within the same
    bound of scalar.

  Vector kernels compute four outputs per group and reduce them with
  shuffles into one vector that is stored whole, so no scalar float code runs
  while the upper register halves are dirty (an earlier per-output scalar
  reduction made the 256-bit kernels ten times slower). Trailing outputs that
  do not fill a group run through the same group code on a zero-padded copy
  of their input, so grouping never changes the bits. Each tier has its own
  driver loop that calls its group function directly; through a function
  value, escape analysis moved the padded copy to the heap, two allocations
  in every call that ended in a partial group. 256-bit kernels run only
  when AVX2 is present and 512-bit ones only with AVX-512; every kernel ends
  with `ClearAVXUpperBits`. Dispatch requires AVX for any vector kernel
  (`GODEBUG=cpu.avx=off` leaves AVX2/FMA reported but selects scalar).

## Tests

- `TestTapsMatchImpulseResponse`: designed taps equal FFmpeg's impulse response
  (goldens `impulse-300/301/302`) bit for bit.
- `TestFFmpegGoldens`: nine signals (impulses, impulses at both ends, DC, ramp,
  1 kHz square, 20 Hz–24 kHz chirp, white noise), fed whole and in chunks of
  1, 7, 120 and 960 samples. This test, the next two and the streaming tests
  run once per kernel the CPU supports.
- `TestFFmpegPrefixes`: FFmpeg's output for every prefix length 0 … 140 of the
  noise signal and lengths around 200, 300, 1000 and 4096 (counts and values).
- `TestRepositoryGoldens`: the repository's `testdata/*.48k.f32le` (libopus
  decodes of the 20 SILK/hybrid fixtures, paired with `*.16k.f32le` by name;
  both are the full stream, 48000·k samples, no pre-skip or trim to apply) and
  the four stereo references in `testdata/stereo-containers` (the libopus
  stereo vectors trimmed by 317 and 269 samples, each channel resampled on its
  own).
- Streaming: a float64 reference implementation of the definition for every
  length 0 … 259 and several longer ones; bit-identical output for chunks of 1,
  7, 120, 960, 5760 and random sizes and for Flush buffers of 1 sample; empty
  input, Flush without input or repeated, every stream length 1 … 66, reuse
  after a complete and an unfinished Flush, too-small output buffers, the
  decoder's `len(in)/3 + 256` sizing, int64 counters. No allocations in
  steady state with each kernel, for Process calls that produce 0 … 4, 41,
  320, 959 and 1920 outputs and for Flush, counted over hundreds of calls
  rather than averaged (an average rounds a rare allocation down to zero);
  the root package's `TestReadDoesNotAllocate` does the same for `Read`, on
  WebM streams whose every block discards a few samples.
- Kernels: every vector kernel against scalar (exact, or within the stated
  bound for fused kernels), with exactly sized input so over-reads panic;
  dispatch against the CPU features.
- `RESAMPLE_FFMPEG=<ffmpeg> go test -run TestFFmpegLive` compares 200 random
  streams (lengths 0 … 20000, random chunking) with a live binary;
  `RESAMPLE_REGENERATE=1` with `TestFFmpegRegenerate` rewrites `testdata`
  (`RESAMPLE_FFMPEG_ALT` checks a second binary byte for byte).
  `testdata/manifest.json` records the command, both versions and the hashes.

## Results

Errors against FFmpeg (gates: RMS 1e-6, peak 1e-4):

| | scalar, avx, avx2 | avx2-fma |
|---|---|---|
| package goldens (9 signals, all chunkings) | RMS 3.0e-8, peak 1.2e-7 | 0, 0 |
| prefixes (156 lengths) | RMS 3.6e-8, peak 1.2e-7 | 0, 0 |
| repository mono goldens (20) | RMS 1.2e-8, peak 1.2e-7 | 0, 0 |
| repository stereo goldens (4) | RMS 6.1e-9, peak 6.0e-8 | 0, 0 |
| live, 200 random streams, n9.0.2 and 6.1.1 | RMS 1.6e-8, peak 1.2e-7 | 0, 0 |

The worst case leaves a factor 28 of margin on the RMS gate and 800 on the
peak gate. The avx512 kernel could not run on this machine (no AVX-512).

Speed per output sample, `BenchmarkProcess` with 960-sample packets, one
core of an i9-14900HX pinned to a 5.8 GHz core (`taskset -c 8`):

| kernel | ns/output |
|---|---|
| scalar (default build) | 23.3 |
| simd-amd64-avx | 7.0 |
| simd-amd64-avx2 | 4.2 |
| simd-amd64-avx2-fma | 4.3 |

The machine was shared with other work; on a throttled core all numbers were
about twice as high.
