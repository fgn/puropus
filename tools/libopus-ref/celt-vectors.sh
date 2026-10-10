#!/bin/sh
# celt-vectors.sh regenerates the libopus references of the Go CELT decoder
# in opus/internal/celt/testdata:
#
#   <stem>.bit, fixtures.txt   the packets of the CELT fixtures in testdata/
#                              (tools/packets) and their channels, pre-skip
#                              and end trimming
#   <name>.bit                 the CELT loss, channel and transient sequences
#                              of opus/testdata/{transition,stereo}-vectors as
#                              packet lists, losses as empty records and
#                              packets libopus rejects left out
#   <name>.c<N>.frames.txt     celt_decode's frame log for N output channels
#                              from the generic build: final range and PCM
#                              hash of every frame; <name>.r<rate>.c<N> the
#                              same at another output rate, <name>.s17.c<N>
#                              with start band 17 (the hybrid CELT layer)
#   units.txt.gz               celt_ref's stage vectors (generic build)
#
# Usage: tools/libopus-ref/celt-vectors.sh (make -C tools/libopus-ref
# celt-vectors). Needs go, jq, xxd, gzip and both the default and the generic
# build of the tools. It also checks that the default build's celt_decode
# reproduces each fixture's golden PCM bit for bit, which confirms that the
# CELT-level decode is the Opus-level decode for CELT-only streams.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
generic=$here/build/generic
default=$here/build
out=$repo/opus/internal/celt/testdata
fixtures="ogg-celt-fb.opus ogg-celt-fb-10ms.opus webm-celt-20ms.webm webm-celt-40ms.webm webm-celt-60ms.webm"
sequences="transition-vectors/loss-celt transition-vectors/loss-celt-2p5ms transition-vectors/loss-celt-long
stereo-vectors/loss-celt stereo-vectors/loss-celt-2p5ms stereo-vectors/loss-celt-5ms stereo-vectors/channels-celt
stereo-vectors/transients-celt"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$out"
cd "$repo"

echo "# stem channels pre-skip trim: the decode_float options tools/packets/main.go prints" >"$out/fixtures.txt"
for fixture in $fixtures; do
	stem=${fixture%.*}
	opts=$(go run tools/packets/main.go -o "$out/$stem.bit" "testdata/$fixture" 2>/dev/null)
	# $opts is "-c N -skip N -trim N".
	# shellcheck disable=SC2086
	set -- $opts
	channels=$2 skip=$4 trim=$6
	echo "$stem $channels $skip $trim" >>"$out/fixtures.txt"
	"$generic/celt_decode" -c "$channels" "$out/$stem.bit" "$tmp/generic.f32le" "$out/$stem.c$channels.frames.txt"
	"$default/celt_decode" -c "$channels" "$out/$stem.bit" "$tmp/default.f32le" "$tmp/default.txt"
	total=$(($(wc -c <"$tmp/default.f32le") / 4 / channels))
	tail -c +$((skip * channels * 4 + 1)) "$tmp/default.f32le" | head -c $(((total - skip - trim) * channels * 4)) >"$tmp/trimmed.f32le"
	cmp "$tmp/trimmed.f32le" "testdata/$stem.libopus48.f32le"
done

for seq in $sequences; do
	name=$(basename "$seq")
	case $seq in transition-*) name=transition-$name ;; *) name=stereo-$name ;; esac
	# Losses are records of length 0 whose range field is the duration.
	jq -r '.records[] | select((.result // 0) >= 0) | if .missing then "lost \(.frame_size)" else .packet_hex end' \
		"opus/testdata/$seq.json" >"$tmp/records.txt"
	: >"$tmp/records.hex"
	while read -r rec; do
		case $rec in
		lost*) printf '00000000%08x\n' "${rec#lost }" >>"$tmp/records.hex" ;;
		*) printf '%08x00000000%s\n' $((${#rec} / 2)) "$rec" >>"$tmp/records.hex" ;;
		esac
	done <"$tmp/records.txt"
	xxd -r -p "$tmp/records.hex" "$out/$name.bit"
	for channels in 1 2; do
		"$generic/celt_decode" -c "$channels" "$out/$name.bit" "$tmp/generic.f32le" "$out/$name.c$channels.frames.txt"
	done
done

# The other output rates of celt_decoder_init (CELT's own downsampling).
for rate in 8000 12000 16000 24000; do
	"$generic/celt_decode" -c 1 -r "$rate" "$out/webm-celt-20ms.bit" "$tmp/generic.f32le" \
		"$out/webm-celt-20ms.r$rate.c1.frames.txt"
	"$generic/celt_decode" -c 2 -r "$rate" "$out/stereo-channels-celt.bit" "$tmp/generic.f32le" \
		"$out/stereo-channels-celt.r$rate.c2.frames.txt"
done

# Start band 17, the CELT layer of hybrid frames.
"$generic/celt_decode" -c 1 -s 17 "$out/webm-celt-20ms.bit" "$tmp/generic.f32le" "$out/webm-celt-20ms.s17.c1.frames.txt"
for channels in 1 2; do
	"$generic/celt_decode" -c "$channels" -s 17 "$out/stereo-loss-celt.bit" "$tmp/generic.f32le" \
		"$out/stereo-loss-celt.s17.c$channels.frames.txt"
done

"$generic/celt_ref" | gzip -9 -n >"$out/units.txt.gz"
echo "updated $out"
