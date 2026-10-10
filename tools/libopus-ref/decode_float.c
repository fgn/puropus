/* decode_float: decode a packet list with libopus's float API.

   This is the reference decode for the Go decoder: opus_decoder_create(48000,
   channels) and opus_decode_float() with decode_fec = 0, every packet in
   order, writing interleaved 32-bit little-endian float PCM at 48 kHz.

   Usage: decode_float [-c channels] [-skip n] [-trim n] [-lost n]
                       packets.bit out.f32le [ranges.txt]
          decode_float -version

     -c n     output channels, 1 (default) or 2, whatever the coded channels
     -skip n  drop the first n samples per channel of the output (pre-skip)
     -trim n  drop the last n samples per channel of the output (end trimming)
     -lost n  conceal a lost packet as n samples per channel (see below)
     -version print opus_get_version_string() and exit

   packets.bit is the opus_demo bitstream format: a sequence of records, each
   a 4-byte big-endian payload length, a 4-byte big-endian final range, and
   the payload. A record of length 0 is a lost packet: it decodes as
   opus_decode_float(st, NULL, 0, pcm, n, 0) with n taken from its range
   field when nonzero, else from -lost, else the duration of the last packet
   (OPUS_GET_LAST_PACKET_DURATION), else 960. For other records a nonzero
   range field is compared with OPUS_GET_FINAL_RANGE, as opus_demo does; a
   mismatch is reported and makes the exit status 1.

   ranges.txt, when given, receives one line per record: the record index,
   the payload length, the samples per channel decoded and the final range in
   hex. Decoding stops with exit status 1 at the first packet libopus
   rejects. */

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "opus.h"

#define MAX_PACKET 1500 * 48 /* well above a 120 ms packet of 1275-byte frames */
#define MAX_SAMPLES 5760     /* 120 ms at 48 kHz */

static void usage(void) {
  fprintf(stderr,
          "usage: decode_float [-c channels] [-skip n] [-trim n] [-lost n] "
          "packets.bit out.f32le [ranges.txt]\n"
          "       decode_float -version\n");
  exit(2);
}

static long parse_count(const char *s) {
  char *end;
  long v;
  errno = 0;
  v = strtol(s, &end, 10);
  if (errno || *end || v < 0) usage();
  return v;
}

static opus_uint32 be32(const unsigned char *p) {
  return (opus_uint32)p[0] << 24 | (opus_uint32)p[1] << 16 | (opus_uint32)p[2] << 8 | p[3];
}

/* put_f32le appends little-endian floats whatever the host byte order. */
static void put_f32le(unsigned char *dst, const float *src, size_t n) {
  size_t i;
  for (i = 0; i < n; i++) {
    opus_uint32 u;
    memcpy(&u, &src[i], 4);
    dst[4 * i] = (unsigned char)u;
    dst[4 * i + 1] = (unsigned char)(u >> 8);
    dst[4 * i + 2] = (unsigned char)(u >> 16);
    dst[4 * i + 3] = (unsigned char)(u >> 24);
  }
}

int main(int argc, char **argv) {
  int channels = 1;
  long skip = 0, trim = 0, lost_samples = 0;
  const char *in_path, *out_path, *ranges_path = NULL;
  FILE *in, *out, *ranges = NULL;
  OpusDecoder *dec;
  static unsigned char packet[MAX_PACKET];
  static float pcm[MAX_SAMPLES * 2];
  unsigned char *all = NULL; /* every decoded sample, as f32le */
  size_t all_len = 0, all_cap = 0;
  long records = 0, lost = 0, mismatches = 0;
  size_t total, first, last;
  int err, i;

  if (argc == 2 && !strcmp(argv[1], "-version")) {
    printf("%s\n", opus_get_version_string());
    return 0;
  }
  for (i = 1; i < argc && argv[i][0] == '-' && argv[i][1]; i++) {
    if (i + 1 >= argc) usage();
    if (!strcmp(argv[i], "-c")) {
      channels = (int)parse_count(argv[++i]);
      if (channels != 1 && channels != 2) usage();
    } else if (!strcmp(argv[i], "-skip")) {
      skip = parse_count(argv[++i]);
    } else if (!strcmp(argv[i], "-trim")) {
      trim = parse_count(argv[++i]);
    } else if (!strcmp(argv[i], "-lost")) {
      lost_samples = parse_count(argv[++i]);
    } else {
      usage();
    }
  }
  if (argc - i < 2 || argc - i > 3) usage();
  in_path = argv[i];
  out_path = argv[i + 1];
  if (argc - i == 3) ranges_path = argv[i + 2];

  in = fopen(in_path, "rb");
  if (!in) {
    perror(in_path);
    return 1;
  }
  dec = opus_decoder_create(48000, channels, &err);
  if (err != OPUS_OK) {
    fprintf(stderr, "opus_decoder_create: %s\n", opus_strerror(err));
    return 1;
  }
  if (ranges_path) {
    ranges = fopen(ranges_path, "w");
    if (!ranges) {
      perror(ranges_path);
      return 1;
    }
    fprintf(ranges, "# record bytes samples final_range (%s, %d channel(s))\n", opus_get_version_string(),
            channels);
  }

  for (;;) {
    unsigned char head[8];
    size_t got = fread(head, 1, 8, in);
    opus_uint32 len, range, final_range = 0;
    int n;
    if (got == 0 && feof(in)) break;
    if (got != 8) {
      fprintf(stderr, "%s: truncated record header %ld\n", in_path, records);
      return 1;
    }
    len = be32(head);
    range = be32(head + 4);
    if (len > MAX_PACKET) {
      fprintf(stderr, "%s: record %ld: invalid length %u\n", in_path, records, len);
      return 1;
    }
    if (fread(packet, 1, len, in) != len) {
      fprintf(stderr, "%s: truncated record %ld\n", in_path, records);
      return 1;
    }
    if (len == 0) {
      opus_int32 duration = (opus_int32)range;
      if (duration == 0) duration = (opus_int32)lost_samples;
      if (duration == 0) opus_decoder_ctl(dec, OPUS_GET_LAST_PACKET_DURATION(&duration));
      if (duration == 0) duration = 960;
      if (duration > MAX_SAMPLES) {
        fprintf(stderr, "record %ld: lost duration %d exceeds 120 ms\n", records, duration);
        return 1;
      }
      n = opus_decode_float(dec, NULL, 0, pcm, duration, 0);
      lost++;
    } else {
      n = opus_decode_float(dec, packet, (opus_int32)len, pcm, MAX_SAMPLES, 0);
    }
    if (n < 0) {
      fprintf(stderr, "record %ld: opus_decode_float: %s\n", records, opus_strerror(n));
      return 1;
    }
    opus_decoder_ctl(dec, OPUS_GET_FINAL_RANGE(&final_range));
    if (len > 0 && range != 0 && range != final_range) {
      fprintf(stderr, "record %ld: final range %08x, expected %08x\n", records, final_range, range);
      mismatches++;
    }
    if (ranges) fprintf(ranges, "%ld %u %d %08x\n", records, len, n, final_range);
    if (all_len + (size_t)n * channels * 4 > all_cap) {
      all_cap = 2 * all_cap + (size_t)MAX_SAMPLES * 2 * 4;
      all = realloc(all, all_cap);
      if (!all) {
        fprintf(stderr, "out of memory\n");
        return 1;
      }
    }
    put_f32le(all + all_len, pcm, (size_t)n * channels);
    all_len += (size_t)n * channels * 4;
    records++;
  }
  fclose(in);
  opus_decoder_destroy(dec);
  if (ranges && fclose(ranges)) {
    perror(ranges_path);
    return 1;
  }

  total = all_len / (4 * (size_t)channels);
  if ((size_t)skip + (size_t)trim > total) {
    fprintf(stderr, "skip %ld and trim %ld exceed the %zu decoded samples\n", skip, trim, total);
    return 1;
  }
  first = (size_t)skip;
  last = total - (size_t)trim;
  out = fopen(out_path, "wb");
  if (!out) {
    perror(out_path);
    return 1;
  }
  if (last > first &&
      fwrite(all + first * channels * 4, 4 * (size_t)channels, last - first, out) != last - first) {
    perror(out_path);
    return 1;
  }
  if (fclose(out)) {
    perror(out_path);
    return 1;
  }
  free(all);
  fprintf(stderr, "%s: %ld records (%ld lost), %zu samples decoded, %zu written\n", in_path, records, lost,
          total, last - first);
  if (mismatches) {
    fprintf(stderr, "%ld final range mismatches\n", mismatches);
    return 1;
  }
  return 0;
}
