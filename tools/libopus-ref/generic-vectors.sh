#!/bin/sh
# generic-vectors.sh writes the generic build's decode of the libopus
# sequences in opus/testdata/transition-vectors and stereo-vectors:
#
#   transition-vectors/<name>.generic.f32le       mono output
#   stereo-vectors/<name>.generic.f32le           stereo output
#   stereo-vectors/<name>.generic.mono.f32le      mono output
#
# and records their SHA-256 in each directory's manifest.json
# (generic_pcm_sha256 per case, or the files map).
#
# Each sequence becomes a packet list for decode_float: packets with their
# final range, which decode_float checks, losses as empty records whose range
# field is the concealed duration, packets libopus rejects left out (they do
# not change its state). The script first checks that the default build's
# decode_float reproduces the committed PCM of every sequence byte for byte,
# so the packet lists are the sequences, then decodes them with the generic
# build (make GENERIC=1: libopus's generic C paths, the code the Go port
# follows).
#
# Usage: tools/libopus-ref/generic-vectors.sh (make -C tools/libopus-ref
# generic-vectors). Needs jq, xxd and both builds of decode_float.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
default=$here/build/decode_float
generic=$here/build/generic/decode_float
data=$repo/opus/testdata

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# packet_list json bit: writes the packet list of a sequence.
packet_list() {
	jq -r '.records[] | select((.result // 0) >= 0) |
		if .missing then "lost \(.frame_size)" else "\(.final_range) \(.packet_hex)" end' "$1" >"$tmp/records.txt"
	: >"$tmp/records.hex"
	while read -r a b; do
		case $a in
		lost) printf '00000000%08x\n' "$b" >>"$tmp/records.hex" ;;
		*) printf '%08x%08x%s\n' $((${#b} / 2)) "$a" "$b" >>"$tmp/records.hex" ;;
		esac
	done <"$tmp/records.txt"
	xxd -r -p "$tmp/records.hex" "$2"
}

# decode build channels bit out
decode() {
	"$1" -c "$2" "$3" "$4" >/dev/null
}

sha() { sha256sum "$1" | cut -d' ' -f1; }

dir=$data/transition-vectors
manifest=$dir/manifest.json
for name in $(jq -r '.cases[].name' "$manifest"); do
	packet_list "$dir/$name.json" "$tmp/$name.bit"
	decode "$default" 1 "$tmp/$name.bit" "$tmp/default.f32le"
	cmp "$tmp/default.f32le" "$dir/$name.f32le"
	decode "$generic" 1 "$tmp/$name.bit" "$dir/$name.generic.f32le"
	jq --arg name "$name" --arg sha "$(sha "$dir/$name.generic.f32le")" \
		'(.cases[] | select(.name == $name)).generic_pcm_sha256 = $sha' "$manifest" >"$tmp/manifest.json"
	mv "$tmp/manifest.json" "$manifest"
done
jq '.generic_pcm = "<name>.generic.f32le: the same packets and losses decoded by decode_float of the generic build (tools/libopus-ref/generic-vectors.sh)"' \
	"$manifest" >"$tmp/manifest.json"
mv "$tmp/manifest.json" "$manifest"

dir=$data/stereo-vectors
manifest=$dir/manifest.json
for json in "$dir"/*.json; do
	name=$(basename "$json" .json)
	[ "$name" = manifest ] && continue
	packet_list "$json" "$tmp/$name.bit"
	for channels in 1 2; do
		suffix=.f32le
		[ "$channels" = 1 ] && suffix=.mono.f32le
		decode "$default" "$channels" "$tmp/$name.bit" "$tmp/default.f32le"
		cmp "$tmp/default.f32le" "$dir/$name$suffix"
		decode "$generic" "$channels" "$tmp/$name.bit" "$dir/$name.generic$suffix"
		jq --arg file "$name.generic$suffix" --arg sha "$(sha "$dir/$name.generic$suffix")" \
			'.files[$file] = $sha' "$manifest" >"$tmp/manifest.json"
		mv "$tmp/manifest.json" "$manifest"
	done
done
jq '.files |= (to_entries | sort_by(.key) | from_entries) |
	.generic_pcm = "<name>.generic.f32le and <name>.generic.mono.f32le: the same packets and losses decoded by decode_float of the generic build (tools/libopus-ref/generic-vectors.sh)"' \
	"$manifest" >"$tmp/manifest.json"
mv "$tmp/manifest.json" "$manifest"
echo "updated $data/transition-vectors and $data/stereo-vectors"
