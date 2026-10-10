#!/bin/sh
# gen-silk-ranges.sh writes the libopus final range of every packet of the
# SILK-only fixtures in testdata/ to opus/internal/silk/testdata/ranges/, for
# the whole-stream tests of the SILK decoder. Each file is decode_float's
# ranges output (index, payload bytes, samples per channel decoded, final
# range in hex) after a header line naming the tool and libopus version. The
# script also checks that decode_float reproduces the fixture's 48 kHz golden
# byte for byte, so the ranges belong to the PCM the tests compare against.
#
# With OPUS_TEST_VECTORS set to the RFC 8251 vectors (opus_newvectors), it
# also writes opus/internal/silk/testdata/official-vectors.txt: the sample
# count and the SHA-256 of decode_float's mono and stereo output for the
# SILK-only vectors 02, 03 and 04.
#
# Usage: tools/libopus-ref/gen-silk-ranges.sh [decode_float]
# (make -C tools/libopus-ref silk-ranges). Needs go, cmp and sha256sum.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
decode=${1:-$here/build/decode_float}
case $decode in /*) ;; *) decode=$(pwd)/$decode ;; esac
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$repo"

out=opus/internal/silk/testdata/ranges
mkdir -p "$out"
libopus=$("$decode" -version)

for fixture in testdata/ogg-silk-*.opus; do
	stem=$(basename "$fixture" .opus)
	golden=testdata/$stem.48k.f32le
	[ -f "$golden" ] || golden=testdata/$stem.libopus48.f32le
	opts=$(go run tools/packets/main.go -o "$tmp/$stem.bit" "$fixture" 2>/dev/null)
	# $opts is "-c N -skip N -trim N" and must split into words.
	# shellcheck disable=SC2086
	"$decode" $opts "$tmp/$stem.bit" "$tmp/$stem.f32le" "$tmp/$stem.ranges.txt"
	cmp "$tmp/$stem.f32le" "$golden"
	{
		echo "# tools/libopus-ref/decode_float ($libopus, static float build) on $fixture: index bytes samples final-range"
		cat "$tmp/$stem.ranges.txt"
	} >"$out/$stem.txt"
done

if [ -n "${OPUS_TEST_VECTORS:-}" ]; then
	list=opus/internal/silk/testdata/official-vectors.txt
	{
		echo "# tools/libopus-ref/decode_float ($libopus, static float build) -c 1 and -c 2 on the RFC 8251 vectors: name channels samples-per-channel sha256-of-f32le"
		for n in 02 03 04; do
			for c in 1 2; do
				"$decode" -c "$c" "$OPUS_TEST_VECTORS/testvector$n.bit" "$tmp/out.f32le" 2>/dev/null
				bytes=$(wc -c <"$tmp/out.f32le")
				echo "testvector$n $c $((bytes / 4 / c)) $(sha256sum "$tmp/out.f32le" | cut -d' ' -f1)"
			done
		done
	} >"$list"
fi
