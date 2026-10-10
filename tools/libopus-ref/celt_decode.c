/* celt_decode: decode CELT-only Opus packets with libopus's CELT decoder
   directly, frame by frame, as opus_decode_float drives it.

   This is the frame-level reference for the Go CELT decoder in
   opus/internal/celt. It calls the CELT layer of libopus 1.5.2
   (celt_decoder_init, celt_decode_with_ec and the CELT CTLs, internal
   functions that libopus.a exports) the way src/opus_decoder.c does for
   CELT-only packets (opus_decode_native and opus_decode_frame): every frame
   of a packet with CELT_SET_END_BAND from the packet bandwidth,
   CELT_SET_CHANNELS from the packet channels and CELT_SET_START_BAND(0);
   frames of at most one byte and lost packets conceal, split into 20, 10,
   5 or 2.5 ms steps as opus_decode_frame splits them, with the end band
   left as it was; before any frame was decoded concealment outputs zeros
   without calling CELT. For CELT-only streams the PCM equals
   opus_decode_float's (decode_float.c); this tool also reports the CELT
   final range of every frame, concealed frames included, where the Opus
   layer reports one range per packet and 0 after concealment.

   Usage: celt_decode [-c channels] [-r rate] [-s start] packets.bit out.f32le frames.txt

     -c n     output channels, 1 (default) or 2
     -r rate  output sample rate (default 48000)
     -s band  CELT_SET_START_BAND for every frame instead of 0: with 17 the
              CELT frames decode as the CELT layer of hybrid frames does
              (no post-filter, bands from 17, noise concealment)

   packets.bit is the opus_demo bitstream format of decode_float.c; a
   record of length 0 is a lost packet, concealed for the duration in its
   range field, or the duration of the last packet, or 960 samples.
   Non-CELT packets are an error.

   frames.txt receives one line per CELT call or zero fill:
     <record> <frame> <kind> <bytes> <samples> <end_band> <stream_channels> <rng> <hash>
   kind is "frame" (a coded frame), "plc" (concealment) or "zeros" (no
   frame decoded yet); bytes is the frame size, samples the output samples
   per channel, rng the CELT final range in hex (OPUS_GET_FINAL_RANGE on the
   CELT decoder) and hash the 32-bit FNV-1a hash of the output PCM's
   little-endian float bytes, in hex. The PCM and the hashes depend on the
   build: the generic build (make GENERIC=1) computes what the Go code
   computes. */

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "celt.h"
#include "opus.h"

#define MAX_PACKET 1500 * 48
#define MAX_SAMPLES 5760

static int channels = 1;
static opus_int32 fs = 48000;
static int start_band;
static CELTDecoder *celt;
static FILE *out, *frames;
static float pcm[MAX_SAMPLES * 2];

/* The Opus-level state that opus_decode_frame keeps for CELT-only streams. */
static int prev_mode;   /* 0 before the first frame, else 1 */
static int end_band = 21;
static int stream_channels;
static int last_packet_duration;
static int packet_frame_size;
static long record_index;

static void usage(void) {
  fprintf(stderr, "usage: celt_decode [-c channels] [-r rate] [-s start] packets.bit out.f32le frames.txt\n");
  exit(2);
}

static void fail(const char *what, int err) {
  fprintf(stderr, "record %ld: %s: %d\n", record_index, what, err);
  exit(1);
}

static opus_uint32 be32(const unsigned char *p) {
  return (opus_uint32)p[0] << 24 | (opus_uint32)p[1] << 16 | (opus_uint32)p[2] << 8 | p[3];
}

/* put_pcm writes n samples per channel of pcm and returns their FNV-1a
   hash. */
static opus_uint32 put_pcm(int n) {
  int i, k;
  opus_uint32 h = 2166136261u;
  for (i = 0; i < n * channels; i++) {
    unsigned char b[4];
    opus_uint32 u;
    memcpy(&u, &pcm[i], 4);
    b[0] = (unsigned char)u;
    b[1] = (unsigned char)(u >> 8);
    b[2] = (unsigned char)(u >> 16);
    b[3] = (unsigned char)(u >> 24);
    fwrite(b, 1, 4, out);
    for (k = 0; k < 4; k++) h = (h ^ b[k]) * 16777619u;
  }
  return h;
}

/* decode_frame is opus_decode_frame for a CELT-only stream. */
static int decode_frame(int frame, const unsigned char *data, int len, int frame_size) {
  int F20 = fs / 50, F10 = F20 >> 1, F5 = F10 >> 1;
  int audiosize, ret;
  opus_uint32 rng;
  if (len <= 1) {
    data = NULL;
    frame_size = frame_size < packet_frame_size ? frame_size : packet_frame_size;
  }
  if (data != NULL) {
    audiosize = packet_frame_size;
  } else {
    audiosize = frame_size;
    if (prev_mode == 0) {
      memset(pcm, 0, sizeof(float) * audiosize * channels);
      fprintf(frames, "%ld %d zeros 0 %d %d %d 00000000 %08x\n", record_index, frame, audiosize, end_band,
              stream_channels, put_pcm(audiosize));
      return audiosize;
    }
    if (audiosize > F20) {
      int done = 0;
      while (done < frame_size) {
        int n = frame_size - done < F20 ? frame_size - done : F20;
        done += decode_frame(frame, NULL, 0, n);
      }
      return frame_size;
    } else if (audiosize < F20) {
      if (audiosize > F10)
        audiosize = F10;
      else if (audiosize > F5 && audiosize < F10)
        audiosize = F5;
    }
  }
  if (data != NULL) {
    ret = celt_decoder_ctl(celt, CELT_SET_END_BAND(end_band));
    if (ret != OPUS_OK) fail("CELT_SET_END_BAND", ret);
  }
  ret = celt_decoder_ctl(celt, CELT_SET_CHANNELS(stream_channels));
  if (ret != OPUS_OK) fail("CELT_SET_CHANNELS", ret);
  ret = celt_decoder_ctl(celt, CELT_SET_START_BAND(start_band));
  if (ret != OPUS_OK) fail("CELT_SET_START_BAND", ret);
  ret = celt_decode_with_ec(celt, data, len, pcm, audiosize < F20 ? audiosize : F20, NULL, 0);
  if (ret < 0) fail("celt_decode_with_ec", ret);
  ret = celt_decoder_ctl(celt, OPUS_GET_FINAL_RANGE(&rng));
  if (ret != OPUS_OK) fail("OPUS_GET_FINAL_RANGE", ret);
  prev_mode = 1;
  fprintf(frames, "%ld %d %s %d %d %d %d %08x %08x\n", record_index, frame, data ? "frame" : "plc", data ? len : 0,
          audiosize, end_band, stream_channels, rng, put_pcm(audiosize));
  return audiosize;
}

int main(int argc, char **argv) {
  static unsigned char packet[MAX_PACKET];
  FILE *in;
  int i, err;

  for (i = 1; i < argc && argv[i][0] == '-' && argv[i][1]; i++) {
    if (i + 1 >= argc) usage();
    if (!strcmp(argv[i], "-c")) {
      channels = atoi(argv[++i]);
      if (channels != 1 && channels != 2) usage();
    } else if (!strcmp(argv[i], "-r")) {
      fs = atoi(argv[++i]);
    } else if (!strcmp(argv[i], "-s")) {
      start_band = atoi(argv[++i]);
    } else {
      usage();
    }
  }
  if (argc - i != 3) usage();
  in = fopen(argv[i], "rb");
  out = fopen(argv[i + 1], "wb");
  frames = fopen(argv[i + 2], "w");
  if (!in || !out || !frames) {
    perror("open");
    return 1;
  }
  celt = malloc(celt_decoder_get_size(channels));
  err = celt_decoder_init(celt, fs, channels);
  if (err != OPUS_OK) {
    fprintf(stderr, "celt_decoder_init: %d\n", err);
    return 1;
  }
  /* opus_decoder_init sets CELT_SET_SIGNALLING(0). */
  celt_decoder_ctl(celt, CELT_SET_SIGNALLING(0));
  stream_channels = channels;
  packet_frame_size = fs / 400;
  fprintf(frames,
          "# record frame kind bytes samples end_band stream_channels rng hash (%s, celt_decode -c %d -r %d -s %d)\n",
          opus_get_version_string(), channels, fs, start_band);

  for (record_index = 0;; record_index++) {
    unsigned char head[8], toc;
    const unsigned char *frame_data[48];
    opus_int16 size[48];
    opus_uint32 len, range;
    int count, offset, f;
    size_t got = fread(head, 1, 8, in);
    if (got == 0 && feof(in)) break;
    if (got != 8) fail("truncated record header", 0);
    len = be32(head);
    range = be32(head + 4);
    if (len > MAX_PACKET || fread(packet, 1, len, in) != len) fail("truncated record", (int)len);
    if (len == 0) {
      /* opus_decode_native with data == NULL. */
      int duration = (int)range, done = 0;
      if (duration == 0) duration = last_packet_duration;
      if (duration == 0) duration = 960 * fs / 48000;
      while (done < duration) done += decode_frame(0, NULL, 0, duration - done);
      last_packet_duration = duration;
      continue;
    }
    if ((packet[0] >> 3) < 16) fail("not a CELT-only packet", packet[0] >> 3);
    count = opus_packet_parse(packet, (opus_int32)len, &toc, frame_data, size, &offset);
    if (count < 0) fail("opus_packet_parse", count);
    switch (opus_packet_get_bandwidth(packet)) {
    case OPUS_BANDWIDTH_NARROWBAND:
      end_band = 13;
      break;
    case OPUS_BANDWIDTH_MEDIUMBAND:
    case OPUS_BANDWIDTH_WIDEBAND:
      end_band = 17;
      break;
    case OPUS_BANDWIDTH_SUPERWIDEBAND:
      end_band = 19;
      break;
    default:
      end_band = 21;
      break;
    }
    packet_frame_size = opus_packet_get_samples_per_frame(packet, fs);
    stream_channels = opus_packet_get_nb_channels(packet);
    for (f = 0; f < count; f++) decode_frame(f, frame_data[f], size[f], packet_frame_size);
    last_packet_duration = count * packet_frame_size;
  }
  if (fclose(out) || fclose(frames)) {
    perror("close");
    return 1;
  }
  fclose(in);
  free(celt);
  return 0;
}
