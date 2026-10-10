# Reference tools built against libopus 1.5.2. See README.md.
#
#   make                  build the tools into build/
#   make GENERIC=1        the same against libopus built from source with only
#                         its generic C paths, into build/generic/
#   make entcode-vectors  regenerate opus/internal/entcode/testdata/entdec.txt
#   make packet-vectors   regenerate opus/testdata/packet-parse/libopus.txt
#   make goldens          regenerate the CELT fixture goldens in testdata/
#   make generic-vectors  the generic build's decode of the sequences in
#                         opus/testdata/{transition,stereo}-vectors
#
# Every target but clean needs OPUS_SRC, the libopus 1.5.2 source tree (git
# tag v1.5.2), and every target without GENERIC=1 also needs OPUS_BUILD, a
# CMake Release build directory of that tree holding the static float
# libopus.a. Set both, as absolute paths, in the environment or on the
# command line; README.md says how to get and build libopus.

BUILD      ?= build
REPO       := $(abspath ../..)

ifneq ($(filter-out clean,$(or $(MAKECMDGOALS),all)),)
ifeq ($(OPUS_SRC),)
$(error OPUS_SRC is not set: point it at the libopus 1.5.2 source tree (git tag v1.5.2), see README.md)
endif
ifeq ($(wildcard $(OPUS_SRC)/celt_sources.mk),)
$(error OPUS_SRC=$(OPUS_SRC) is not a libopus source tree (no celt_sources.mk), see README.md)
endif
ifneq ($(GENERIC),1)
ifeq ($(OPUS_BUILD),)
$(error OPUS_BUILD is not set: point it at a CMake Release build of libopus 1.5.2 that holds libopus.a (or pass GENERIC=1 for the generic build alone), see README.md)
endif
ifeq ($(wildcard $(OPUS_BUILD)/libopus.a),)
$(error OPUS_BUILD=$(OPUS_BUILD) holds no libopus.a: build libopus there first, see README.md)
endif
endif
endif

CC     ?= cc
CFLAGS ?= -O2 -std=c99 -Wall -Wextra

ifeq ($(GENERIC),1)
OUT     := $(BUILD)/generic
LIBOPUS := $(OUT)/libopus.a
else
OUT     := $(BUILD)
LIBOPUS := $(OPUS_BUILD)/libopus.a
endif

.PHONY: all entcode-vectors packet-vectors goldens generic-vectors celt-vectors celt-official-refs clean
all: $(OUT)/decode_float $(OUT)/entcode_ref $(OUT)/packet_ref $(OUT)/celt_decode

$(OUT)/decode_float: decode_float.c $(LIBOPUS)
	@mkdir -p $(OUT)
	$(CC) $(CFLAGS) -I$(OPUS_SRC)/include -o $@ decode_float.c $(LIBOPUS) -lm

# entcode_ref calls libopus's internal ec_enc_*/ec_dec_* functions, which the
# static library exports.
$(OUT)/entcode_ref: entcode_ref.c $(LIBOPUS)
	@mkdir -p $(OUT)
	$(CC) $(CFLAGS) -I$(OPUS_SRC)/include -I$(OPUS_SRC)/celt -o $@ entcode_ref.c $(LIBOPUS) -lm

# packet_ref calls libopus's internal opus_packet_parse_impl.
$(OUT)/packet_ref: packet_ref.c $(LIBOPUS)
	@mkdir -p $(OUT)
	$(CC) $(CFLAGS) -I$(OPUS_SRC)/include -o $@ packet_ref.c $(LIBOPUS) -lm

entcode-vectors: $(OUT)/entcode_ref
	$(OUT)/entcode_ref > $(REPO)/opus/internal/entcode/testdata/entdec.txt

packet-vectors: $(OUT)/packet_ref
	@mkdir -p $(REPO)/opus/testdata/packet-parse
	$(OUT)/packet_ref > $(REPO)/opus/testdata/packet-parse/libopus.txt

goldens: $(OUT)/decode_float
	./gen-goldens.sh $(OUT)/decode_float

generic-vectors: $(BUILD)/decode_float $(BUILD)/generic/decode_float
	./generic-vectors.sh

# The generic build: libopus's own source lists (as its Makefile.unix uses
# them), float, no intrinsics and no run-time CPU detection, and no fused
# multiply-add contraction, so the arithmetic is that of the C source.
-include $(OPUS_SRC)/celt_sources.mk
-include $(OPUS_SRC)/silk_sources.mk
-include $(OPUS_SRC)/opus_sources.mk
GENERIC_SRCS   := $(CELT_SOURCES) $(SILK_SOURCES) $(SILK_SOURCES_FLOAT) $(OPUS_SOURCES) $(OPUS_SOURCES_FLOAT)
GENERIC_OBJS   := $(GENERIC_SRCS:%.c=$(BUILD)/generic/obj/%.o)
GENERIC_CFLAGS := -O2 -std=c99 -w -ffp-contract=off -DOPUS_BUILD -DVAR_ARRAYS -DHAVE_LRINTF \
	-DPACKAGE_VERSION='"1.5.2"' -I$(OPUS_SRC)/include -I$(OPUS_SRC)/celt -I$(OPUS_SRC)/silk \
	-I$(OPUS_SRC)/silk/float -I$(OPUS_SRC)/dnn

$(BUILD)/generic/obj/%.o: $(OPUS_SRC)/%.c
	@mkdir -p $(dir $@)
	$(CC) $(GENERIC_CFLAGS) -c -o $@ $<

$(BUILD)/generic/libopus.a: $(GENERIC_OBJS)
	rm -f $@
	ar rcs $@ $^

# --- CELT (opus/internal/celt) ---------------------------------------------
#
#   make celt-vectors            regenerate opus/internal/celt/testdata/: packet
#                                lists, the generic build's frame logs and the
#                                stage vectors of celt_ref (celt-vectors.sh)
#   make celt-official-refs      libopus references for the CELT-only RFC 8251
#                                vectors in $(BUILD)/celt-official (OPUS_TEST_VECTORS)

# celt_decode calls libopus's internal CELT decoder (celt_decoder_init,
# celt_decode_with_ec, opus_custom_decoder_ctl), which the static library
# exports.
$(OUT)/celt_decode: celt_decode.c $(LIBOPUS)
	@mkdir -p $(OUT)
	$(CC) $(CFLAGS) -I$(OPUS_SRC)/include -I$(OPUS_SRC)/celt -o $@ celt_decode.c $(LIBOPUS) -lm

celt-vectors: $(BUILD)/celt_decode $(BUILD)/generic/celt_decode $(BUILD)/generic/celt_ref
	./celt-vectors.sh

# The libopus references of the CELT-only RFC 8251 vectors, too large to
# commit: the SIMD build's decode_float PCM and the generic build's frame logs
# for mono and stereo output. TestOfficialVectors in opus/internal/celt reads
# them.
CELT_OFFICIAL := 01 07 11
celt-official-refs: $(BUILD)/decode_float $(BUILD)/generic/celt_decode
	@test -n "$(OPUS_TEST_VECTORS)" || (echo "set OPUS_TEST_VECTORS" && exit 1)
	@mkdir -p $(BUILD)/celt-official
	for n in $(CELT_OFFICIAL); do for c in 1 2; do \
		$(BUILD)/decode_float -c $$c $(OPUS_TEST_VECTORS)/testvector$$n.bit $(BUILD)/celt-official/testvector$$n.c$$c.f32le && \
		$(BUILD)/generic/celt_decode -c $$c $(OPUS_TEST_VECTORS)/testvector$$n.bit /dev/null \
			$(BUILD)/celt-official/testvector$$n.c$$c.frames.txt || exit 1; \
	done; done

# Without GENERIC=1, $(OUT)/celt_decode and $(OUT)/decode_float above are the
# SIMD build's.
ifneq ($(GENERIC),1)
$(BUILD)/generic/celt_decode: celt_decode.c $(BUILD)/generic/libopus.a
	@mkdir -p $(BUILD)/generic
	$(CC) $(CFLAGS) -I$(OPUS_SRC)/include -I$(OPUS_SRC)/celt -o $@ celt_decode.c $(BUILD)/generic/libopus.a -lm

$(BUILD)/generic/decode_float: decode_float.c $(BUILD)/generic/libopus.a
	@mkdir -p $(BUILD)/generic
	$(CC) $(CFLAGS) -I$(OPUS_SRC)/include -o $@ decode_float.c $(BUILD)/generic/libopus.a -lm
endif

# celt_ref calls internal CELT functions of the generic build; deemphasis is
# static in celt_decoder.c, so the tool compiles its own copy of that file
# with RESYNTH defined, which exports it.
$(BUILD)/generic/celt_ref: celt_ref.c $(BUILD)/generic/libopus.a
	@mkdir -p $(BUILD)/generic/obj/resynth
	$(CC) $(GENERIC_CFLAGS) -DRESYNTH -c -o $(BUILD)/generic/obj/resynth/celt_decoder.o $(OPUS_SRC)/celt/celt_decoder.c
	$(CC) $(GENERIC_CFLAGS) -o $@ celt_ref.c $(BUILD)/generic/obj/resynth/celt_decoder.o $(BUILD)/generic/libopus.a -lm

clean:
	rm -rf $(BUILD)

# ---- SILK ----
# silk_ref calls libopus's internal SILK decoder functions, and silk_shell_encoder
# and silk_encode_pulses to build its test bitstreams; the static library
# exports them. silk-vectors regenerates the unit, PLC, loss and FEC references of
# opus/internal/silk, silk-ranges the final ranges of the SILK fixtures.
SILK_PLC_FIXTURES := ogg-silk-wb ogg-silk-nb-10ms ogg-silk-mb-60ms
SILK_LOSS_FIXTURES := ogg-silk-nb-10ms ogg-silk-nb ogg-silk-mb ogg-silk-mb-40ms ogg-silk-wb ogg-silk-wb-fec ogg-silk-wb-60ms
SILK_FEC_FIXTURES := ogg-silk-nb-fec ogg-silk-mb-fec ogg-silk-wb-fec ogg-silk-wb-60ms-fec
.PHONY: silk-vectors silk-ranges
all: $(OUT)/silk_ref

$(OUT)/silk_ref: silk_ref.c $(LIBOPUS)
	@mkdir -p $(OUT)
	$(CC) $(CFLAGS) -I$(OPUS_SRC)/include -I$(OPUS_SRC)/celt -I$(OPUS_SRC)/silk -o $@ silk_ref.c $(LIBOPUS) -lm

silk-vectors: $(OUT)/silk_ref
	@mkdir -p $(OUT)/silk
	$(OUT)/silk_ref units > $(REPO)/opus/internal/silk/testdata/units.txt
	for f in $(sort $(SILK_PLC_FIXTURES) $(SILK_LOSS_FIXTURES) $(SILK_FEC_FIXTURES)); do \
		(cd $(REPO) && go run tools/packets/main.go -o $(abspath $(OUT))/silk/$$f.bit testdata/$$f.opus) >/dev/null || exit 1; \
	done
	$(OUT)/silk_ref plc $(foreach f,$(SILK_PLC_FIXTURES),$(f)=$(OUT)/silk/$(f).bit) > $(REPO)/opus/internal/silk/testdata/plc.txt
	$(OUT)/silk_ref loss $(foreach f,$(SILK_LOSS_FIXTURES),$(f)=$(OUT)/silk/$(f).bit) > $(REPO)/opus/internal/silk/testdata/loss.txt
	$(OUT)/silk_ref fec $(foreach f,$(SILK_FEC_FIXTURES),$(f)=$(OUT)/silk/$(f).bit) > $(REPO)/opus/internal/silk/testdata/fec.txt

silk-ranges: $(OUT)/decode_float
	./gen-silk-ranges.sh $(OUT)/decode_float
