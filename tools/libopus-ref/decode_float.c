/* decode_float: decode a packet list with libopus's float API.

   This is the reference decode for the Go decoder: opus_decoder_create(48000,
   channels) and opus_decode_float() for every packet in order, writing
   interleaved 32-bit little-endian float PCM at 48 kHz.

   Usage: decode_float [-c channels] [-skip n] [-trim n] [-lost n]
                       [-drop list] [-fec lbrr|always] [-log records.txt]
                       packets.bit out.f32le [ranges.txt]
          decode_float -version

     -c n     output channels, 1 (default) or 2, whatever the coded channels
     -skip n  drop the first n samples per channel of the output (pre-skip)
     -trim n  drop the last n samples per channel of the output (end trimming)
     -lost n  conceal a lost packet as n samples per channel (see below)
     -drop l  also lose the records listed in l, comma-separated indices and
              inclusive ranges such as 4,9-11 (see below)
     -fec p   decode lost records from the in-band FEC of the next record,
              p lbrr or always (see below)
     -log f   write a line per record with the hash of its output to f
     -version print opus_get_version_string() and exit

   packets.bit is the opus_demo bitstream format: a sequence of records, each
   a 4-byte big-endian payload length, a 4-byte big-endian final range, and
   the payload. A record of length 0 is a lost packet: it decodes as
   opus_decode_float(st, NULL, 0, pcm, n, 0) with n taken from its range
   field when nonzero, else from -lost, else the duration of the last packet
   (OPUS_GET_LAST_PACKET_DURATION), else 960. For other records a nonzero
   range field is compared with OPUS_GET_FINAL_RANGE, as opus_demo does; a
   mismatch is reported and makes the exit status 1.

   A record that -drop lists is lost too, with n the duration of its own
   packet (opus_packet_get_nb_samples), the way a container-level decoder
   conceals a packet it received but cannot decode; its range field is not
   checked.

   With -fec, a lost record whose next record is received (neither empty nor
   dropped) decodes as opus_decode_float(st, next, len, pcm, n, 1): libopus
   conceals all but the last frame of the n samples and decodes that frame
   from the LBRR data of the next packet where it can. -fec lbrr does so only
   when the first frame of the next packet carries LBRR data
   (opus_packet_has_lbrr, a first frame of more than one byte), as the
   puropus Decoder does in Tolerant mode and as applications that check
   opus_packet_has_lbrr do; -fec always does so for every such loss, as
   opus_demo does with in-band FEC enabled.

   ranges.txt, when given, receives one line per record: the record index,
   the payload length, the samples per channel decoded and the final range in
   hex. records.txt (-log) receives one line per record: the index, how it
   was decoded (packet, lost or fec), the payload length, the samples per
   channel, the final range in hex and the 64-bit FNV-1a hash (hex) of the
   record's output as float32 little-endian bytes. Decoding stops with exit
   status 1 at the first packet libopus rejects. */

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "opus.h"

#define MAX_PACKET 1500 * 48 /* well above a 120 ms packet of 1275-byte frames */
#define MAX_SAMPLES 5760     /* 120 ms at 48 kHz */
#define MAX_RECORDS 100000

/* record is one record of packets.bit. */
struct record {
  opus_uint32 len, range;
  unsigned char *data;
};

static void usage(void) {
  fprintf(stderr,
          "usage: decode_float [-c channels] [-skip n] [-trim n] [-lost n] [-drop list] "
          "[-fec lbrr|always] [-log records.txt] packets.bit out.f32le [ranges.txt]\n"
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

/* parse_drop marks the records listed in s ("4,9-11") in dropped. */
static void parse_drop(const char *s, unsigned char *dropped) {
  while (*s) {
    char *end;
    long a, b;
    errno = 0;
    a = strtol(s, &end, 10);
    if (errno || end == s || a < 0 || a >= MAX_RECORDS) usage();
    b = a;
    if (*end == '-') {
      s = end + 1;
      b = strtol(s, &end, 10);
      if (errno || end == s || b < a || b >= MAX_RECORDS) usage();
    }
    for (; a <= b; a++) dropped[a] = 1;
    if (*end == ',') end++;
    else if (*end) usage();
    s = end;
  }
}

/* has_lbrr is opus.HasLBRR: opus_packet_has_lbrr for a packet that parses
   with a first frame of more than one byte, which opus_decode_frame decodes
   rather than conceals. */
static int has_lbrr(const unsigned char *data, opus_int32 len) {
  const unsigned char *frames[48];
  opus_int16 size[48];
  if (opus_packet_parse(data, len, NULL, frames, size, NULL) <= 0 || size[0] <= 1) return 0;
  return opus_packet_has_lbrr(data, len) == 1;
}

/* fnv1a is the 64-bit FNV-1a hash of n floats as little-endian bytes. */
static unsigned long long fnv1a(const float *pcm, size_t n) {
  unsigned long long h = 0xcbf29ce484222325ull;
  size_t i;
  int b;
  for (i = 0; i < n; i++) {
    opus_uint32 u;
    memcpy(&u, &pcm[i], 4);
    for (b = 0; b < 4; b++) {
      h ^= (u >> (8 * b)) & 0xff;
      h *= 0x100000001b3ull;
    }
  }
  return h;
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
  int channels = 1, fec = 0; /* fec: 0 off, 1 lbrr, 2 always */
  long skip = 0, trim = 0, lost_samples = 0;
  const char *in_path, *out_path, *ranges_path = NULL, *log_path = NULL;
  FILE *in, *out, *ranges = NULL, *log = NULL;
  OpusDecoder *dec;
  static struct record recs[MAX_RECORDS];
  static unsigned char dropped[MAX_RECORDS];
  static float pcm[MAX_SAMPLES * 2];
  unsigned char *all = NULL; /* every decoded sample, as f32le */
  size_t all_len = 0, all_cap = 0;
  long records = 0, r, lost = 0, recovered = 0, mismatches = 0;
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
    } else if (!strcmp(argv[i], "-drop")) {
      parse_drop(argv[++i], dropped);
    } else if (!strcmp(argv[i], "-fec")) {
      i++;
      if (!strcmp(argv[i], "lbrr")) fec = 1;
      else if (!strcmp(argv[i], "always")) fec = 2;
      else usage();
    } else if (!strcmp(argv[i], "-log")) {
      log_path = argv[++i];
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
  /* Read every record first: FEC decodes a loss from the record after it. */
  for (;;) {
    unsigned char head[8];
    size_t got = fread(head, 1, 8, in);
    if (got == 0 && feof(in)) break;
    if (got != 8) {
      fprintf(stderr, "%s: truncated record header %ld\n", in_path, records);
      return 1;
    }
    if (records == MAX_RECORDS) {
      fprintf(stderr, "%s: more than %d records\n", in_path, MAX_RECORDS);
      return 1;
    }
    recs[records].len = be32(head);
    recs[records].range = be32(head + 4);
    if (recs[records].len > MAX_PACKET) {
      fprintf(stderr, "%s: record %ld: invalid length %u\n", in_path, records, recs[records].len);
      return 1;
    }
    recs[records].data = malloc(recs[records].len + 1);
    if (!recs[records].data) {
      fprintf(stderr, "out of memory\n");
      return 1;
    }
    if (fread(recs[records].data, 1, recs[records].len, in) != recs[records].len) {
      fprintf(stderr, "%s: truncated record %ld\n", in_path, records);
      return 1;
    }
    records++;
  }
  fclose(in);

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
  if (log_path) {
    log = fopen(log_path, "w");
    if (!log) {
      perror(log_path);
      return 1;
    }
  }

  for (r = 0; r < records; r++) {
    const struct record *rec = &recs[r];
    const struct record *next = r + 1 < records ? &recs[r + 1] : NULL;
    opus_uint32 final_range = 0;
    const char *kind = "packet";
    int n;
    if (rec->len == 0 || dropped[r]) {
      opus_int32 duration;
      if (rec->len == 0) {
        duration = (opus_int32)rec->range;
        if (duration == 0) duration = (opus_int32)lost_samples;
        if (duration == 0) opus_decoder_ctl(dec, OPUS_GET_LAST_PACKET_DURATION(&duration));
        if (duration == 0) duration = 960;
      } else {
        duration = opus_packet_get_nb_samples(rec->data, (opus_int32)rec->len, 48000);
        if (duration < 0) {
          fprintf(stderr, "record %ld: dropped packet: %s\n", r, opus_strerror(duration));
          return 1;
        }
      }
      if (duration > MAX_SAMPLES) {
        fprintf(stderr, "record %ld: lost duration %d exceeds 120 ms\n", r, duration);
        return 1;
      }
      if (fec && next && next->len > 0 && !dropped[r + 1] &&
          (fec == 2 || has_lbrr(next->data, (opus_int32)next->len))) {
        n = opus_decode_float(dec, next->data, (opus_int32)next->len, pcm, duration, 1);
        kind = "fec";
        recovered++;
      } else {
        n = opus_decode_float(dec, NULL, 0, pcm, duration, 0);
        kind = "lost";
      }
      lost++;
    } else {
      n = opus_decode_float(dec, rec->data, (opus_int32)rec->len, pcm, MAX_SAMPLES, 0);
    }
    if (n < 0) {
      fprintf(stderr, "record %ld: opus_decode_float: %s\n", r, opus_strerror(n));
      return 1;
    }
    opus_decoder_ctl(dec, OPUS_GET_FINAL_RANGE(&final_range));
    if (rec->len > 0 && !dropped[r] && rec->range != 0 && rec->range != final_range) {
      fprintf(stderr, "record %ld: final range %08x, expected %08x\n", r, final_range, rec->range);
      mismatches++;
    }
    if (ranges) fprintf(ranges, "%ld %u %d %08x\n", r, rec->len, n, final_range);
    if (log)
      fprintf(log, "%ld %s %u %d %08x %016llx\n", r, kind, rec->len, n, final_range,
              fnv1a(pcm, (size_t)n * channels));
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
  }
  opus_decoder_destroy(dec);
  if (ranges && fclose(ranges)) {
    perror(ranges_path);
    return 1;
  }
  if (log && fclose(log)) {
    perror(log_path);
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
  for (r = 0; r < records; r++) free(recs[r].data);
  fprintf(stderr, "%s: %ld records (%ld lost, %ld from FEC), %zu samples decoded, %zu written\n", in_path, records,
          lost, recovered, total, last - first);
  if (mismatches) {
    fprintf(stderr, "%ld final range mismatches\n", mismatches);
    return 1;
  }
  return 0;
}
