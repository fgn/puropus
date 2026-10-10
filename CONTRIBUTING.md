# Contributing

Thanks for looking. Ground rules, kept short:

- **Pure Go.** `CGO_ENABLED=0` must build and pass. No C wrappers, no
  handwritten assembly. Go's `simd` intrinsics (`GOEXPERIMENT=simd`) are fine
  behind runtime dispatch, with the scalar path kept as the oracle.
- **Reference-gated.** Decoder changes must keep every committed fixture within
  the gates against libopus (48 kHz) and FFmpeg's resampling of it (16 kHz)
  (`go test ./...` checks this) and keep the scalar and SIMD builds in
  agreement. If you change the goldens, say how they were produced (see
  "Reference data" below).
- **libopus and the RFCs only.** The codec under `opus/` is a port of
  libopus 1.5.2; follow its structure and the conventions in
  `opus/internal/README.md`, keep the libopus notice on ported files, and do
  not bring in code from other Opus implementations
  ([docs/provenance.md](docs/provenance.md)).
- **Small surface.** New exported API needs a reason that a user, not the
  implementation, has. Prefer adding to `internal/`.
- **Hermetic tests.** Tests run offline with what is in `testdata/`. External
  fixtures may be used through environment variables and must skip when unset.
  Every `testdata` directory has a `go.mod` of its own, so that the module zip
  `go get` downloads leaves the references out (0.5 MB instead of 38 MB); a
  package whose tests read them has a `TestMain` that skips when they are
  missing. A new data directory needs both.
- **Fuzz what parses bytes.** Container and packet parsers have fuzz targets;
  keep them passing (`task fuzz`).

## Workflow

```sh
task format   # gofumpt, go mod tidy
task lint     # golangci-lint (scalar and SIMD builds), govulncheck
task test     # scalar build
task test:simd
task fuzz     # brief fuzzing of the parsers
task ci       # everything the CI runs
```

Commit messages describe the change and, for anything touching the decoder,
the numbers: what got faster or more accurate, measured how.

## Reference data

The tests need nothing outside the repository. Making new reference data
does:

- **libopus references** (`tools/libopus-ref`): small C programs linked
  against libopus 1.5.2, with make targets that regenerate each kind of
  reference. They need a C compiler and make, the libopus 1.5.2 source tree
  in `OPUS_SRC` and a CMake Release build of it in `OPUS_BUILD` (the
  [README](tools/libopus-ref/README.md) says how to get and build them),
  and go, jq, xxd, gzip, cmp and sha256sum for the scripts. The 16 kHz
  goldens also need an `ffmpeg` binary (`FFMPEG`, default `ffmpeg` on
  `PATH`); the committed ones were made with FFmpeg n9.0.2. `task refs`
  builds the tools.
- **Container packets** (`tools/packets`): a Go program, behind the `ignore`
  build tag, that writes a fixture's packets in the format the C tools read.
- **Resampler measurements** (`tools/resample-measure`): a Python 3 script,
  with numpy, that repeats the FFmpeg measurements behind
  `internal/resample/DESIGN.md`. `RESAMPLE_FFMPEG=<ffmpeg> go test
  ./internal/resample -run TestFFmpegLive` compares the resampler with a live
  binary, and adding `RESAMPLE_REGENERATE=1` with `-run
  TestFFmpegRegenerate` rewrites its goldens.
- **RFC 8251 test vectors**: `OPUS_TEST_VECTORS=/path/to/opus_newvectors
  task test:official` checks them; they are not in the repository.
- **Your own recordings**: `PUROPUS_STREAMS` (a list of globs) points
  `BenchmarkStreamTo16k` and `BenchmarkDecodeStreams` at them.

## License

By contributing you agree that your contributions are licensed under the MIT
license in `LICENSE`, except changes to files that carry a libopus notice,
which are made under that file's BSD terms (see `NOTICE`).
