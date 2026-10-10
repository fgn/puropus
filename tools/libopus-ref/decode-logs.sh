#!/bin/sh
# decode-logs.sh writes opus/testdata/decode-logs: libopus's decode of the
# SILK and hybrid fixtures and of loss cases recovered with in-band FEC,
# record by record, from the generic build (make GENERIC=1: libopus's
# generic C paths, the code the Go port follows):
#
#   <case>.txt     decode_float's -log output for the case, after a header
#                  line: index, kind (packet, lost or fec), payload bytes,
#                  samples per channel, final range and the FNV-1a hash of
#                  the record's float32 output
#   manifest.json  the cases, how each was decoded, and the SHA-256 of each
#                  log and of the decoded PCM (pre-skip and end trimming
#                  applied for the fixtures)
#
# A case decodes one packet list: a fixture of testdata/ (tools/packets), a
# sequence of opus/testdata/{transition,stereo}-vectors (losses as empty
# records whose range field is the concealed duration, packets libopus
# rejects left out), or a splice of fixture packet ranges. -drop loses the
# listed records as well, each for its own packet's duration; -fec lbrr
# decodes a lost record from the in-band FEC of the next record when that
# record carries LBRR data (the puropus Decoder's Tolerant mode), -fec always
# whenever the next record is received (opus_demo with in-band FEC). See
# decode_float.c.
#
# The script checks that the default build's decode of every fixture without
# losses reproduces the fixture's committed golden byte for byte, so the
# packet lists are the fixtures, and decodes every case with the default
# build too: the kinds, sample counts and final ranges must agree, and the
# manifest records how many records' PCM differs between the two builds
# (default_build_pcm_differs).
#
# Usage: tools/libopus-ref/decode-logs.sh (make -C tools/libopus-ref
# decode-logs). Needs go, jq, xxd, awk, cmp, sha256sum and both builds of
# decode_float.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
generic=$here/build/generic/decode_float
default=$here/build/decode_float
out=$repo/opus/testdata/decode-logs

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$out"
cd "$repo"

# A splice of fixture packet ranges (fixture:first:count) that switches
# between SILK WB, hybrid FB, CELT FB, SILK WB 60 ms, SILK NB and SILK MB,
# all but the CELT packets with LBRR data.
mode_switch="ogg-silk-wb-fec.opus:0:30 ogg-hybrid-fb-fec.opus:30:30 ogg-celt-fb.opus:60:15
ogg-silk-wb-fec.opus:75:25 ogg-silk-wb-60ms-fec.opus:5:10 ogg-silk-nb-fec.opus:20:20
ogg-hybrid-fb-fec.opus:100:20 ogg-silk-mb-fec.opus:50:10"

# Loss patterns, on top of the empty records of the sequences: single
# losses, the first and the last packet, bursts, and losses before packets
# without LBRR data, before a CELT packet, across mode, bandwidth, frame
# duration and coded channel switches.
drop76=0,4,13,22,30-33,39,45,58-59,75
drop151=0,4,13,22,30-33,39,45,58-59,72,99-102,120,150
drop26=0,4,9-11,17,25
drop_channels=0,5,12,14,19-21,23,28,36,47,55,60,71
drop_transitions=5,11,23,25,35,48,59,62,71,80-81,83
drop_modes=5,11,23,25,35,48,59,62,71
drop_switch=0,10,29,40,59,65,74,76,99,102,109,115-117,129,140,149,159
# Before a TOC-only packet and before code 1 and code 3 packets of empty
# frames, which opus_decode_frame conceals.
drop_dtx=32,42,48

# name source channels drop fec
cases="
ogg-silk-nb fixture:ogg-silk-nb.opus 1 - off
ogg-silk-mb fixture:ogg-silk-mb.opus 1 - off
ogg-silk-wb fixture:ogg-silk-wb.opus 1 - off
ogg-silk-nb-10ms fixture:ogg-silk-nb-10ms.opus 1 - off
ogg-silk-mb-10ms fixture:ogg-silk-mb-10ms.opus 1 - off
ogg-silk-wb-10ms fixture:ogg-silk-wb-10ms.opus 1 - off
ogg-silk-nb-40ms fixture:ogg-silk-nb-40ms.opus 1 - off
ogg-silk-mb-40ms fixture:ogg-silk-mb-40ms.opus 1 - off
ogg-silk-wb-40ms fixture:ogg-silk-wb-40ms.opus 1 - off
ogg-silk-nb-60ms fixture:ogg-silk-nb-60ms.opus 1 - off
ogg-silk-mb-60ms fixture:ogg-silk-mb-60ms.opus 1 - off
ogg-silk-wb-60ms fixture:ogg-silk-wb-60ms.opus 1 - off
ogg-silk-nb-fec fixture:ogg-silk-nb-fec.opus 1 - off
ogg-silk-mb-fec fixture:ogg-silk-mb-fec.opus 1 - off
ogg-silk-wb-fec fixture:ogg-silk-wb-fec.opus 1 - off
ogg-silk-wb-60ms-fec fixture:ogg-silk-wb-60ms-fec.opus 1 - off
ogg-silk-bandwidth-switch fixture:ogg-silk-bandwidth-switch.opus 1 - off
ogg-hybrid-swb fixture:ogg-hybrid-swb.opus 1 - off
ogg-hybrid-swb-10ms fixture:ogg-hybrid-swb-10ms.opus 1 - off
ogg-hybrid-fb-10ms fixture:ogg-hybrid-fb-10ms.opus 1 - off
ogg-hybrid-fb-fec fixture:ogg-hybrid-fb-fec.opus 1 - off
ogg-switch-silk-celt-wb fixture:ogg-switch-silk-celt-wb.opus 1 - off
ogg-silk-nb-fec.lbrr fixture:ogg-silk-nb-fec.opus 1 $drop76 lbrr
ogg-silk-nb-fec.always fixture:ogg-silk-nb-fec.opus 1 $drop76 always
ogg-silk-mb-fec.lbrr fixture:ogg-silk-mb-fec.opus 1 $drop76 lbrr
ogg-silk-mb-fec.always fixture:ogg-silk-mb-fec.opus 1 $drop76 always
ogg-silk-wb-fec.lbrr fixture:ogg-silk-wb-fec.opus 1 $drop151 lbrr
ogg-silk-wb-fec.always fixture:ogg-silk-wb-fec.opus 1 $drop151 always
ogg-silk-wb-fec.c2.lbrr fixture:ogg-silk-wb-fec.opus 2 $drop151 lbrr
ogg-silk-wb-60ms-fec.lbrr fixture:ogg-silk-wb-60ms-fec.opus 1 $drop26 lbrr
ogg-silk-wb-60ms-fec.always fixture:ogg-silk-wb-60ms-fec.opus 1 $drop26 always
ogg-hybrid-fb-fec.lbrr fixture:ogg-hybrid-fb-fec.opus 1 $drop151 lbrr
ogg-hybrid-fb-fec.always fixture:ogg-hybrid-fb-fec.opus 1 $drop151 always
ogg-hybrid-fb-fec.c2.lbrr fixture:ogg-hybrid-fb-fec.opus 2 $drop151 lbrr
channels-silk-fec.c2.lbrr vector:stereo-vectors/channels-silk-fec 2 $drop_channels lbrr
channels-silk-fec.c1.lbrr vector:stereo-vectors/channels-silk-fec 1 $drop_channels lbrr
channels-silk-fec.c2.always vector:stereo-vectors/channels-silk-fec 2 $drop_channels always
channels-hybrid-fec.c2.lbrr vector:stereo-vectors/channels-hybrid-fec 2 $drop_channels lbrr
channels-hybrid-fec.c1.lbrr vector:stereo-vectors/channels-hybrid-fec 1 $drop_channels lbrr
channels-hybrid-fec.c2.always vector:stereo-vectors/channels-hybrid-fec 2 $drop_channels always
transitions-cbr16.always vector:transition-vectors/transitions-cbr16 1 $drop_transitions always
modes.c2.always vector:stereo-vectors/modes 2 $drop_modes always
modes.c1.always vector:stereo-vectors/modes 1 $drop_modes always
loss-silk.always vector:transition-vectors/loss-silk 1 $drop_dtx always
loss-silk-nb10.always vector:transition-vectors/loss-silk-nb10 1 - always
loss-silk-mb60.always vector:transition-vectors/loss-silk-mb60 1 - always
loss-hybrid.always vector:transition-vectors/loss-hybrid 1 $drop_dtx always
loss-celt.always vector:transition-vectors/loss-celt 1 - always
quiet-dtx.c2.always vector:stereo-vectors/quiet-dtx 2 - always
mode-switch.lbrr splice:mode_switch 1 $drop_switch lbrr
mode-switch.always splice:mode_switch 1 $drop_switch always
mode-switch.c2.lbrr splice:mode_switch 2 $drop_switch lbrr
"

sha() { sha256sum "$1" | cut -d' ' -f1; }

# fixture_list fixture bit: writes the packet list of testdata/<fixture> and
# prints its pre-skip and end trimming.
fixture_list() {
	opts=$(go run tools/packets/main.go -o "$2" "testdata/$1" 2>/dev/null)
	# $opts is "-c N -skip N -trim N".
	# shellcheck disable=SC2086
	set -- $opts
	echo "$4 $6"
}

# vector_list name bit checked: writes the packet list of
# opus/testdata/<name>.json, with the final range of every packet for
# decode_float to check when checked is 1. A loss changes the decoder state
# the next packets decode from, and with it their final range.
vector_list() {
	jq -r --argjson checked "$3" '.records[] | select((.result // 0) >= 0) |
		if .missing then "lost \(.frame_size)" else "\(.final_range * $checked) \(.packet_hex)" end' \
		"opus/testdata/$1.json" >"$tmp/records.txt"
	: >"$tmp/records.hex"
	while read -r a b; do
		case $a in
		lost) printf '00000000%08x\n' "$b" >>"$tmp/records.hex" ;;
		*) printf '%08x%08x%s\n' $((${#b} / 2)) "$a" "$b" >>"$tmp/records.hex" ;;
		esac
	done <"$tmp/records.txt"
	xxd -r -p "$tmp/records.hex" >"$2"
}

# slice bit first count: prints the hex of records first to first+count-1.
slice() {
	xxd -p "$1" | tr -d '\n' | awk -v first="$2" -v count="$3" '{
		pos = 1
		for (i = 0; pos < length($0) && i < first + count; i++) {
			len = 0
			for (k = 0; k < 8; k++) len = len * 16 + index("0123456789abcdef", substr($0, pos + k, 1)) - 1
			n = 16 + 2 * len
			if (i >= first) print substr($0, pos, n)
			pos += n
		}
		if (i < first + count) exit 1
	}'
}

# splice_list segments bit: writes the concatenated packet ranges.
splice_list() {
	: >"$tmp/splice.hex"
	for seg in $1; do
		fixture=${seg%%:*} rest=${seg#*:}
		fixture_list "$fixture" "$tmp/segment.bit" >/dev/null
		slice "$tmp/segment.bit" "${rest%%:*}" "${rest#*:}" >>"$tmp/splice.hex"
	done
	xxd -r -p "$tmp/splice.hex" >"$2"
}

# segments_json segments: the splice as a JSON array.
segments_json() {
	for seg in $1; do
		fixture=${seg%%:*} rest=${seg#*:}
		printf '{"fixture":"%s","first":%s,"count":%s}\n' "$fixture" "${rest%%:*}" "${rest#*:}"
	done | jq -s .
}

libopus=$("$generic" -version)
echo '[]' >"$tmp/cases.json"
echo "$cases" | while read -r name source channels drop fec; do
	[ -n "$name" ] || continue
	kind=${source%%:*} what=${source#*:}
	skip=0 trim=0
	case $kind in
	fixture)
		# shellcheck disable=SC2046
		set -- $(fixture_list "$what" "$tmp/case.bit")
		skip=$1 trim=$2
		source_json=$(jq -n --arg f "$what" '{fixture: $f}')
		;;
	vector)
		checked=0
		[ "$drop" = - ] && [ "$fec" = off ] && checked=1
		vector_list "$what" "$tmp/case.bit" "$checked"
		source_json=$(jq -n --arg v "$what" '{vector: $v}')
		;;
	splice)
		eval "segments=\$$what"
		# shellcheck disable=SC2154
		splice_list "$segments" "$tmp/case.bit"
		source_json=$(jq -n --arg n "$what" --argjson s "$(segments_json "$segments")" '{splice: $n, segments: $s}')
		;;
	esac
	opts="-c $channels -skip $skip -trim $trim"
	[ "$drop" = - ] || opts="$opts -drop $drop"
	[ "$fec" = off ] || opts="$opts -fec $fec"
	# shellcheck disable=SC2086
	"$generic" $opts -log "$tmp/generic.txt" "$tmp/case.bit" "$tmp/generic.f32le" 2>/dev/null
	# shellcheck disable=SC2086
	"$default" $opts -log "$tmp/default.txt" "$tmp/case.bit" "$tmp/default.f32le" 2>/dev/null
	if [ "$kind" = fixture ] && [ "$drop" = - ] && [ "$fec" = off ] && [ "$channels" = 1 ]; then
		stem=${what%.*}
		golden=testdata/$stem.48k.f32le
		[ -f "$golden" ] || golden=testdata/$stem.libopus48.f32le
		cmp "$tmp/default.f32le" "$golden"
	fi
	# Kinds, sample counts and final ranges agree; only PCM may differ.
	cut -d' ' -f1-5 "$tmp/generic.txt" >"$tmp/generic.head"
	cut -d' ' -f1-5 "$tmp/default.txt" >"$tmp/default.head"
	cmp "$tmp/generic.head" "$tmp/default.head"
	differs=$(paste -d' ' "$tmp/generic.txt" "$tmp/default.txt" | awk '$6 != $12' | wc -l)
	{
		echo "# tools/libopus-ref/decode_float ($libopus, generic build) $opts on $source: index kind bytes samples final-range fnv1a64"
		cat "$tmp/generic.txt"
	} >"$out/$name.txt"
	jq --arg name "$name" --argjson source "$source_json" --argjson channels "$channels" \
		--argjson skip "$skip" --argjson trim "$trim" --arg drop "$drop" --arg fec "$fec" \
		--argjson records "$(wc -l <"$tmp/generic.txt")" \
		--argjson lost "$(awk '$2 == "lost"' "$tmp/generic.txt" | wc -l)" \
		--argjson recovered "$(awk '$2 == "fec"' "$tmp/generic.txt" | wc -l)" \
		--arg log_sha "$(sha "$out/$name.txt")" --arg pcm_sha "$(sha "$tmp/generic.f32le")" \
		--argjson differs "$differs" \
		'. + [{name: $name, source: $source, channels: $channels, skip: $skip, trim: $trim,
			drop: (if $drop == "-" then "" else $drop end), fec: $fec, records: $records,
			lost_records: $lost, fec_records: $recovered, log_sha256: $log_sha, pcm_sha256: $pcm_sha,
			default_build_pcm_differs: $differs}]' "$tmp/cases.json" >"$tmp/cases.next"
	mv "$tmp/cases.next" "$tmp/cases.json"
	echo "$name: $(wc -l <"$tmp/generic.txt") records, $differs differ between the builds" >&2
done

jq -n --arg libopus "$libopus" --arg cc "$(cc --version | head -n 1)" \
	--arg lib "$(sha "$here/build/generic/libopus.a")" --arg tool "$(sha "$here/decode_float.c")" \
	--arg script "$(sha "$here/decode-logs.sh")" --slurpfile cases "$tmp/cases.json" '{
	schema: 1,
	libopus: $libopus,
	build: "generic: libopus 1.5.2 compiled from source by tools/libopus-ref/Makefile with GENERIC=1 (float, no intrinsics, no run-time CPU detection, -ffp-contract=off)",
	generic_library_sha256: $lib,
	decode_float_c_sha256: $tool,
	script_sha256: $script,
	compiler: $cc,
	command: "make -C tools/libopus-ref decode-logs (tools/libopus-ref/decode-logs.sh)",
	log: "<name>.txt: decode_float -log of the generic build, after one header line: index kind bytes samples final-range fnv1a64. kind is packet, lost (concealed: opus_decode_float(st, NULL, 0, pcm, n, 0)) or fec (opus_decode_float(st, next, len, pcm, n, 1) with the next record); n is the duration of a dropped packet, or the range field of an empty record. fnv1a64 hashes the record'"'"'s interleaved float32 little-endian output.",
	pcm_sha256: "SHA-256 of decode_float'"'"'s float32 little-endian output for the case: after the pre-skip and end trimming tools/packets prints for a fixture source, the whole decode otherwise",
	drop: "records lost as well as the empty ones, each concealed or recovered for its own packet'"'"'s duration (decode_float -drop)",
	fec: "off: losses concealed; lbrr: a lost record whose next record is received and carries LBRR data (opus_packet_has_lbrr, first frame over one byte) decoded with decode_fec = 1; always: every lost record whose next record is received decoded with decode_fec = 1",
	default_build_pcm_differs: "records whose PCM hash differs when the default build ($OPUS_BUILD/libopus.a, SIMD kernels) decodes the case; their kinds, sample counts and final ranges are the same",
	cases: $cases[0]
}' >"$out/manifest.json"
echo "updated $out"
