# puropus

*Friends, are you tired of shelling out to `ffmpeg` every time a browser hands
you a WebM? Sick of cgo, of `LD_LIBRARY_PATH`, of a 90 MB binary just to turn
Opus into numbers? Well, have I got something for you.*

**puropus** is a pure Go Opus decoder for Ogg Opus and WebM/Opus, including the
live, unknown-size WebM that browsers' `MediaRecorder` emits. It also edits
the compressed stream directly: keep the intervals your voice activity
detector picked, pad them with context, put synthesized silence between them,
and write the result back out as Ogg or WebM. Every packet you keep passes
through untouched.

Why re-encode an entire recording just to throw pieces away? That is slow.
That is wasteful. Opus is lossy. Cut out what you do not want, stitch together
what remains, and move on.

It decodes mono and stereo to 48 kHz PCM, or straight to 16 kHz for your
speech models. It streams: hand it a live `io.Reader` and it decodes as the
bytes arrive. It's `CGO_ENABLED=0`, it has **zero dependencies**, and its codec
is a port of libopus 1.5.2, tested bit for bit against libopus's own C code
with exact sample counts.

Now, how much would you expect to pay for an advanced audio environment like
this? Five hundred dollars? A thousand? *Wait, don't answer yet!*

Because with `GOEXPERIMENT=simd` on Go 1.27 you also get vector kernels for
CELT's inverse MDCT, band decoding and post-filter, for SILK's resampling and
stereo, and for the 16 kHz resampler, on the house. That's an hour of
Chrome's recordings streamed in and handed back at 16 kHz in under four and a
half seconds of one i9-14900HX core, against six for the scalar build, and
the 48 kHz output does not change by a single bit.

The price? **FREE.** Free! MIT-licensed (the files ported from libopus keep
its BSD licence), `go get`-able, no PO box required.

## Install

```sh
go get github.com/fgn/puropus
```

Requires Go 1.27. Build with `GOEXPERIMENT=simd` for the vector kernels on
amd64. They are chosen at run time from the CPU's features, so `GOAMD64=v3`
is not needed: `v1` and `v3` builds measure within 0.5% of each other.
Everything else runs the portable scalar code.

## Decode

```go
dec, err := puropus.NewDecoder(r, puropus.DecodeOptions{
    SampleRate: 16000, // 0 or 48000 for native; 16000 uses the built-in resampler
    Tolerant:   true,  // recover undecodable packets instead of stopping
})
if err != nil { ... }

buf := make([]float32, 4096)
for {
    n, err := dec.Read(buf) // interleaved float32, io.EOF at the end
    process(buf[:n])
    if err == io.EOF {
        break
    }
    if err != nil { ... }   // errors.Is(err, puropus.ErrTruncated) for cut-off input
}
```

`Read` works like `io.Reader`: it returns everything that was decodable before
reporting an error, and the input can arrive one byte at a time. Ogg pre-skip
and end trimming, WebM `CodecDelay` and `DiscardPadding`, chained Ogg links,
and the header output gain are all applied. The 16 kHz output matches FFmpeg's
default resampler. Audio the container lost, a corrupt Ogg page or WebM
blocks a recorder dropped, is concealed for its duration, recovered from
in-band FEC where the next packet has it, so every sample keeps its position
in the recording, which ffmpeg does not do. Only a gap of half a packet or
more counts, so the millisecond jitter of a browser's MediaRecorder
timestamps is not mistaken for loss, and a recording without loss decodes to
exactly ffmpeg's samples. A corrupt page of another grouped stream costs
nothing, and a chained link whose last page is lost ends where the next one
begins.

With `Tolerant`, a packet that parses but cannot be decoded does not stop the
stream. Its audio is recovered from the in-band FEC of the next packet when
that packet carries some, exactly as a libopus application recovers a lost
packet (`opus_decode_float` with `decode_fec = 1`, then the next packet), and
concealed otherwise; its audio is returned once the next packet has been
read. Packets that do not even parse are skipped.

## Cut

Run your VAD on the decoded audio, then edit the *compressed* stream:

```go
dec, _ := puropus.NewDecoder(r, puropus.DecodeOptions{SampleRate: 16000, KeepPackets: true})
// ... decode, run the VAD, obtain intervals ...

out, err := puropus.Cut(dec.Source(), puropus.Edit{
    Keep:    intervals,                                       // []puropus.Interval{Start, End}
    Extend:  puropus.Margin{Before: 40 * ms, After: 40 * ms}, // keep a little context
    Snap:    puropus.SnapOutward,                             // never lose a sample inside an interval
    Silence: puropus.Silence{Between: 120 * ms},              // synthesized, no encoder needed
})
if err != nil { ... }
err = out.WriteWebM(w) // or out.WriteOgg(w)
```

If the intervals come from somewhere else, `puropus.ReadPackets(r)` returns
the `Source` without decoding anything. On a long live stream,
`Decoder.ReleasePackets` drops what you have already cut.

`out.Spans` maps every stretch of the output back to the input, and
`out.InputTime(t)` answers "where in the original recording did this word come
from?" Synthesized silence returns `false`.

Snapping modes:

| Mode | Boundary handling | Needs |
| --- | --- | --- |
| `SnapOutward` | keeps every packet overlapping an interval (up to one packet of extra context per side) | nothing |
| `SnapInward` | keeps only packets entirely inside an interval | nothing |
| `SnapExact` | copies interior packets, re-encodes the guard regions at the edges (boundaries land within 1.25 ms) | an `Encoder` and `KeepNative` |

Packets sit on the decoded timeline exactly as the decoder produces it: the
first packet of a stream absorbs the pre-skip and the last one the end
trimming (`Packet.Skip` and `Packet.Trim`), so `Source.Duration()`, the
intervals you pass, and the spans you get back all refer to the samples a VAD
saw. A whole-stream cut round-trips bit for bit, chained Ogg files included:
a cut keeps each link's header and the decoder reset between links, and
`WriteOgg` writes them as a chain (WebM, which has one header, takes them
when their output gains agree). Where a kept packet's skipped or trimmed
samples land inside the output (after inserted silence, say) no container
can drop them; they are emitted as audio and mapped as synthetic spans.

Silence is added only around regions that keep packets, and it really is
silence: the inserted frames are CELT frames carrying only the silence flag,
which every Opus decoder renders as zero coefficients. Once the previous
packet's few-millisecond overlap and de-emphasis tail has decayed, the output
is zero as 16-bit PCM. As float it is a constant below 1e-33: libopus's float
de-emphasis adds a tiny offset to stay clear of denormal numbers, and puropus,
following libopus, keeps it. An empty selection returns an empty stream, and
writing it returns `ErrEmptyStream`.

puropus has no encoder, so `SnapExact` takes a `puropus.Encoder` you bring (a
cgo binding to libopus, say). The packets it returns must decode to exactly
the PCM it was given, time-aligned: an encoder with algorithmic delay primes
itself and drops the packets that cover the delay, as the `Encoder`
documentation describes. The other two modes need nothing at all.

## Command line

```sh
go run github.com/fgn/puropus/cmd/puropus@latest inspect recording.webm
go run github.com/fgn/puropus/cmd/puropus@latest decode -rate 16000 -o out.f32le recording.webm
go run github.com/fgn/puropus/cmd/puropus@latest cut -keep speech.txt -extend 40ms -silence 120ms -o trimmed.ogg recording.webm
```

`speech.txt` holds `start end` seconds per line; `#` starts a comment.
`decode` and `cut` refuse to write over their input (by file identity, so a
link to it counts) and replace the output file only when they succeed: a
failed run leaves it as it was. A truncated input is the exception for
`decode`, which writes what it decoded up to the cut and then exits with the
error, as `Read` does.

## Packet level

`github.com/fgn/puropus/opus` is the codec on its own: `opus.Decoder` turns
one packet into 48 kHz PCM, `opus.Parse` reads a packet's table of contents,
`opus.ParseHead` handles `OpusHead`, and `opus.Silence` synthesizes silence
packets for any 2.5 ms multiple.

`Decoder.Conceal(samples, out)` advances over a known missing interval of up
to 120 ms, in 2.5 ms multiples, the way libopus does: it extrapolates from the
previous codec mode, keeps prediction and overlap history, and returns silence
before the first audio packet. Mode changes without redundancy crossfade with
concealed audio as in libopus. A packet that `Decode` rejects leaves the
decoder exactly as it was, so `Conceal` can stand in for it.

`Decoder.DecodeFEC(next, samples, out)` fills the same interval from the
packet that follows it: SILK and hybrid packets can carry a low-bitrate copy
of the audio just before them (in-band FEC, LBRR), which `opus.HasLBRR`
detects. It is `opus_decode_float` with `decode_fec = 1`: it conceals all
but the last frame duration of the interval and decodes that part from
`next`'s LBRR data, or conceals all of it where libopus would (a CELT packet
on either side, or an interval shorter than `next`'s frames). Then decode
`next` itself as usual. A `next` that does not parse is rejected without
touching the decoder, so `Conceal` can take over.

## Performance

Decode speed in times real time on one i9-14900HX core (pinned, Go 1.27.0,
GOMAXPROCS 1, median of 5 interleaved rounds); higher is faster. The speech
sets are 58 minutes of recordings (not included) in the format Chrome's
`MediaRecorder` writes (mono CELT fullband, 60 ms packets), at 64 kb/s and at
the 128 kb/s Chrome uses by default. The others are a LibriVox reading of
*The Art of War* (public domain) encoded with libopus. "SIMD" is
`GOAMD64=v3 GOEXPERIMENT=simd`; "scalar" is `GOAMD64=v1`.

| Decoder | speech 64k | speech 128k | CELT 20 ms | SILK WB | hybrid FB | stereo CELT |
|---|---:|---:|---:|---:|---:|---:|
| puropus, SIMD | 1159× | 891× | 1123× | 2456× | 1134× | 588× |
| puropus, scalar | 962× | 769× | 956× | 1878× | 871× | 495× |
| [thesyncim/gopus](https://github.com/thesyncim/gopus) v0.2.2, SIMD | 1102× | 819× | 1043× | 2110× | 1027× | 549× |
| thesyncim/gopus v0.2.2, scalar | 998× | 759× | 983× | 1772× | 872× | 500× |
| libopus 1.4 (Ubuntu package, through cgo) | 932× | 700× | 886× | 1412× | 778× | 451× |

That is the codec alone (`opus.Decoder.Decode` per packet, 48 kHz float32,
no allocations). From file to 16 kHz in the same conditions, a service
decoding Chrome's default recordings spends this much CPU per hour of audio:

| Tool | CPU per audio hour |
|---|---:|
| puropus in process, SIMD | 4.3 s |
| puropus command line, SIMD | 4.4 s |
| FFmpeg n9.0.2 (static build), `-ar 16000` | 5.8 s |
| puropus in process, scalar | 6.1 s |
| FFmpeg 6.1.1 (Ubuntu package), `-ar 16000` | 6.1 s |

with the same sample counts as FFmpeg and within RMS 1.4e-8 of its output.
Starting a process matters for short clips: three 3-second files take 8.7 ms
of CPU in process, 12 ms through the puropus command and 18 ms through a
static FFmpeg. One machine, one core: measure on yours before you rely on
any of this.

See [docs/simd.md](docs/simd.md) for the kernels, where the time goes, and
what to watch for when writing Go SIMD code.

## Correctness

`go test ./...` runs offline, in a clone, against references committed to
this repository (the module `go get` downloads leaves them out, and its tests
skip): Ogg and WebM fixtures covering every SILK and hybrid
configuration and CELT in 10 to 60 ms packets, 28 libopus sequences with
packet loss, DTX, FEC skipping, and mode, bandwidth and channel transitions,
the 15 stereo ones decoded to mono and to stereo, and packet-by-packet
libopus decode logs of the SILK and hybrid fixtures and of 30 cases of loss
recovered with in-band FEC (single losses, bursts, the first and last
packet, and losses across mode, bandwidth, frame size and channel switches).

- **libopus, bit for bit.** The codec follows libopus 1.5.2's float decoder
  as compiled from its generic C code, and on amd64 (and 386) its 48 kHz
  output is that build's bit for bit, in the scalar and the SIMD build. The
  tests require it of the SILK-only streams sample by sample, of the
  CELT-only streams frame by frame, and of the hybrid fixtures and every FEC
  case packet by packet, by hash, the `Tolerant` decoder's FEC recovery
  included. On all 28 sequences, hybrid included, they require every sample
  to be bit-identical on amd64 and 386, and gate every frame at RMS 1e-6 and
  peak 1e-4 on other architectures.
- **Stock libopus.** A second gate compares with libopus as most programs
  build it, with its x86 SIMD kernels, at the same limits, except for CELT
  pitch concealment and the frames recovering from it, which allow RMS 3e-4
  and peak 5e-4: there the two libopus builds themselves differ by up to RMS
  2.6e-4 and peak 4.8e-4, on the longest loss run.
- **Final range.** Sample counts are exact everywhere, and the range
  coder's final state must equal libopus's after every packet of the
  sequences, the RFC vectors and the fixtures, the SILK packets that carry a
  CELT redundancy frame included, and after every FEC decode.
- **16 kHz.** The 16 kHz output is gated against FFmpeg's default
  resampler run on libopus's 48 kHz decode: exact sample counts, RMS 1e-6
  and peak 1e-4, with a worst case of RMS 2.0e-8 and peak 2.4e-7. The
  resampler alone stays within RMS 3.6e-8 and peak 1.2e-7 of FFmpeg n9.0.2,
  a margin of 28 on the RMS gate and 800 on the peak gate
  ([internal/resample/DESIGN.md](internal/resample/DESIGN.md)).
- **Streams and edits.** Container trimming, chained and grouped Ogg,
  corrupt and truncated input, slow and fragmented input, output gain and
  cuts all have tests, and fuzz targets cover both container readers, the
  packet parser and the decoders.

All twelve [RFC 8251 test vectors](https://www.rfc-editor.org/rfc/rfc8251#section-11)
pass the official `opus_compare`, mono and stereo, in both builds. That check
needs the C reference tools and runs outside this repository;
`TestOfficialOpusVectors` checks the same files' hashes, every packet's final
range, and exact sample counts, and `TestOfficialOpusVectorsFEC` decodes
them with three of every seven packets lost and recovered through
`DecodeFEC`, matching libopus record by record:

```sh
OPUS_TEST_VECTORS=/path/to/opus_newvectors task test:official
```

[docs/design.md](docs/design.md) describes the design and the test evidence
in more detail.

## Limitations

- Audio a container lost is concealed only where the container shows it,
  by the next Ogg page's granule position or the next WebM block's
  timestamp. So nothing is concealed for a loss before a link's first page
  or block, whose granule position or timestamp may be an offset, or after
  its last, which nothing follows.
- On a final Ogg page, lost audio and end trimming cannot be told apart:
  when the page before it is lost, the gap runs to the final granule
  position and the last packet keeps its encoder padding, its audio up to a
  packet early.
- WebM timestamps follow the recorder's capture clock, not the samples, so
  a gap counts only from half the next block's duration: a lost block
  shorter than that is not found. A gap's length is its lateness on the
  2.5 ms grid, or whole packets of the next block's duration when within
  8 ms of them; for other lengths it is accurate to the timestamps' jitter.
- No gap is concealed beyond 10 minutes, against a corrupt or hostile
  granule position or timestamp.
- No encoder. See `SnapExact` above.
- No laced WebM blocks, and no channel mapping families other than 0 (so at
  most two channels).
- Output at 48 or 16 kHz only.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). `task ci` runs the formatters,
linters, the scalar, SIMD, 32-bit and per-CPU-tier test builds, and vets
other platforms; `task fuzz` runs the fuzz targets.

## License

MIT ([LICENSE](LICENSE)), except the files ported from libopus, which stay
under libopus's BSD licence; [NOTICE](NOTICE) lists them.
[docs/provenance.md](docs/provenance.md) says where the code comes from.
