#!/bin/sh
# gen-goldens.sh regenerates the libopus references of the CELT fixtures in
# testdata/ and records them in testdata/manifest.json:
#
#   <stem>.libopus48.f32le  libopus 1.5.2 opus_decode_float at 48 kHz, after
#                           the pre-skip and end trimming the container asks
#                           for: what puropus's Decoder must return
#   <stem>.16k.f32le        FFmpeg's 48-to-16 kHz resampling of it, the same
#                           step that made the SILK and hybrid 16 kHz goldens
#
# Usage: tools/libopus-ref/gen-goldens.sh [decode_float]
# (make -C tools/libopus-ref goldens). Needs go, jq, sha256sum and the FFmpeg
# binary named by $FFMPEG (default: ffmpeg on PATH). The committed goldens
# were made with FFmpeg n9.0.2 on an x86-64 CPU with AVX2 and FMA; FFmpeg
# on a CPU with other vector extensions may round the 16 kHz samples
# differently (internal/resample/DESIGN.md).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
decode=${1:-$here/build/decode_float}
ffmpeg=${FFMPEG:-ffmpeg}
fixtures="ogg-celt-fb.opus ogg-celt-fb-10ms.opus webm-celt-20ms.webm webm-celt-40ms.webm webm-celt-60ms.webm"

case $decode in /*) ;; *) decode=$(pwd)/$decode ;; esac
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$repo"

libopus=$("$decode" -version)
ffversion=$("$ffmpeg" -hide_banner -version | head -n 1)
manifest=testdata/manifest.json

for fixture in $fixtures; do
	stem=${fixture%.*}
	opts=$(go run tools/packets/main.go -o "$tmp/$stem.bit" "testdata/$fixture")
	# $opts is "-c N -skip N -trim N" and must split into words.
	# shellcheck disable=SC2086
	"$decode" $opts "$tmp/$stem.bit" "testdata/$stem.libopus48.f32le" "$tmp/$stem.ranges.txt"
	"$ffmpeg" -hide_banner -loglevel error -f f32le -ar 48000 -ac 1 -i "testdata/$stem.libopus48.f32le" \
		-ar 16000 -f f32le -y "testdata/$stem.16k.f32le"
	jq --arg fixture "$fixture" \
		--arg sha "$(sha256sum "testdata/$fixture" | cut -d' ' -f1)" \
		--arg pcm48 "$stem.libopus48.f32le" \
		--arg pcm48sha "$(sha256sum "testdata/$stem.libopus48.f32le" | cut -d' ' -f1)" \
		--arg pcm16sha "$(sha256sum "testdata/$stem.16k.f32le" | cut -d' ' -f1)" \
		--arg ffmpeg48 "$stem.48k.json" \
		--arg reference "$libopus float API (opus_decode_float, tools/libopus-ref/decode_float on the static float build, git tag v1.5.2); FFmpeg 48-to-16 kHz resampling of it" \
		'.fixtures[$fixture] += {
			sha256: $sha,
			reference: $reference,
			pcm48: $pcm48,
			pcm48_sha256: $pcm48sha,
			pcm16_sha256: $pcm16sha,
			full48: true,
			ffmpeg48: ($ffmpeg48 + ": FFmpeg n9.0.2 native CELT decoder checkpoints, a secondary comparison only")
		}' "$manifest" >"$tmp/manifest.json"
	cp "$tmp/manifest.json" "$manifest"
done

jq --arg libopus "$libopus (git tag v1.5.2)" --arg ffmpeg "$ffversion" \
	'.libopus = $libopus | .other["celt goldens"] = ("The CELT fixtures (ogg-celt-fb, ogg-celt-fb-10ms, webm-celt-20ms/40ms/60ms) are gated against libopus: <stem>.libopus48.f32le is " + $libopus + " opus_decode_float output after the container pre-skip and end trimming (tools/libopus-ref/gen-goldens.sh), and <stem>.16k.f32le is its 48-to-16 kHz resampling by " + $ffmpeg + ". The FFmpeg native CELT checkpoints <stem>.48k.json are kept as a secondary comparison whose distance is only reported.")' \
	"$manifest" >"$tmp/manifest.json"
cp "$tmp/manifest.json" "$manifest"
echo "updated $manifest"
