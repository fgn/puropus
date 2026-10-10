/* packet_ref: reference results of libopus's packet parser for the Go
   Parse and HasLBRR in package opus.

   For every TOC byte it prints opus_packet_get_bandwidth,
   opus_packet_get_samples_per_frame and opus_packet_get_nb_channels. It then
   builds pseudo-random packets of every framing code, mostly well formed and
   sometimes truncated, extended or corrupted, plus random bytes, and prints
   what opus_packet_parse_impl returns for them in both framings, and what
   opus_packet_has_lbrr returns. The output is deterministic.

   Usage: packet_ref > opus/testdata/packet-parse/libopus.txt

   Output format, one record per line, all numbers decimal:
     t <toc> <bandwidth> <samples_per_frame> <channels>
         for every TOC byte; bandwidth is OPUS_BANDWIDTH_NARROWBAND (1101)
         to OPUS_BANDWIDTH_FULLBAND (1105), samples per frame at 48 kHz
     p <len> <hex>
         a packet of len bytes: the hex bytes, then zeros up to len
     r <self_delimited> <ret> [<payload_offset> <packet_offset> <padding_len> <size>...]
         opus_packet_parse_impl of the packet before it: the frame count or a
         negative error, and for a frame count its outputs, one size per frame
     l <has_lbrr>
         opus_packet_has_lbrr of the packet before it */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "opus.h"

/* From src/opus_private.h, which needs libopus's internal headers. */
int opus_packet_parse_impl(const unsigned char *data, opus_int32 len, int self_delimited,
                           unsigned char *out_toc, const unsigned char *frames[48], opus_int16 size[48],
                           int *payload_offset, opus_int32 *packet_offset, const unsigned char **padding,
                           opus_int32 *padding_len);

static opus_uint32 seed = 0x9E3779B9u;

static opus_uint32 rnd(void) {
  seed ^= seed << 13;
  seed ^= seed >> 17;
  seed ^= seed << 5;
  return seed;
}

static int rnd_range(int lo, int hi) {
  return lo + (int)(((unsigned long long)rnd() * (unsigned long long)(hi - lo + 1)) >> 32);
}

#define MAXLEN 70000
/* Bytes past PREFIX are zeroed before parsing, so a long packet prints
   compactly; the headers and the first frame's first bytes stay random. */
#define PREFIX 40

static unsigned char pkt[MAXLEN];

/* frame_size draws a frame length, often near the edges that matter. */
static int frame_size(int small) {
  switch (rnd() % 8) {
  case 0: return rnd_range(0, 1);
  case 1: return small ? rnd_range(0, 8) : rnd_range(1270, 1280);
  case 2: return small ? rnd_range(0, 8) : rnd_range(248, 260);
  case 3: return small ? rnd_range(0, 16) : rnd_range(0, 1275);
  default: return rnd_range(2, small ? 12 : 60);
  }
}

/* put_size writes a frame length as RFC 6716 §3.2.1 codes it. */
static int put_size(unsigned char *p, int s) {
  if (s < 252) {
    p[0] = (unsigned char)s;
    return 1;
  }
  p[0] = (unsigned char)(252 + ((s - 252) & 3));
  p[1] = (unsigned char)((s - p[0]) >> 2);
  return 2;
}

static void fill(unsigned char *p, int n) {
  int i;
  for (i = 0; i < n; i++) p[i] = (unsigned char)rnd();
}

/* build writes a packet of a random framing code, self-delimited or not,
   and returns its length. */
static int build(void) {
  int code = (int)(rnd() % 4), self_delimited = rnd() % 3 == 0;
  int n = 1, i, spf, count = 1, sizes[64], cbr = 1, pad = -1;
  pkt[0] = (unsigned char)(rnd_range(0, 31) << 3 | (rnd() & 4) | code);
  spf = opus_packet_get_samples_per_frame(pkt, 48000);
  switch (code) {
  case 0:
    sizes[0] = frame_size(0);
    break;
  case 1:
    count = 2;
    sizes[0] = sizes[1] = frame_size(0);
    break;
  case 2:
    count = 2;
    cbr = 0;
    sizes[0] = frame_size(0);
    sizes[1] = frame_size(0);
    break;
  default: {
    int most = 5760 / spf;
    count = rnd() % 16 == 0 ? rnd_range(0, 63) : rnd_range(1, most);
    cbr = (int)(rnd() & 1);
    if (rnd() % 3 == 0) pad = rnd() % 4 == 0 ? rnd_range(250, 800) : rnd_range(0, 20);
    pkt[n++] = (unsigned char)(count | (pad >= 0 ? 0x40 : 0) | (cbr ? 0 : 0x80));
    if (pad >= 0) {
      int left = pad;
      while (left >= 255) {
        pkt[n++] = 255;
        left -= 254;
      }
      pkt[n++] = (unsigned char)left;
    }
    for (i = 0; i < count; i++) sizes[i] = cbr && i > 0 ? sizes[0] : frame_size(count > 3);
    break;
  }
  }
  if (!cbr)
    for (i = 0; i < count - 1; i++) n += put_size(pkt + n, sizes[i]);
  if (self_delimited && count > 0) n += put_size(pkt + n, sizes[count - 1]);
  for (i = 0; i < count; i++) {
    fill(pkt + n, sizes[i]);
    n += sizes[i];
  }
  if (pad > 0) {
    fill(pkt + n, pad);
    n += pad;
  }
  /* Damage some packets. */
  switch (rnd() % 8) {
  case 0: n -= rnd_range(1, 3); break;
  case 1: fill(pkt + n, 3); n += rnd_range(1, 3); break;
  case 2: pkt[rnd_range(0, n < 4 ? n - 1 : 3)] = (unsigned char)rnd(); break;
  }
  return n < 1 ? 1 : n;
}

static void report(int len) {
  int sd, i;
  int n = len < PREFIX ? len : PREFIX;
  memset(pkt + n, 0, (size_t)(len - n));
  printf("p %d ", len);
  for (i = 0; i < n; i++) printf("%02x", pkt[i]);
  printf("\n");
  for (sd = 0; sd < 2; sd++) {
    const unsigned char *frames[48], *padding;
    opus_int16 size[48];
    int payload_offset = -1;
    opus_int32 packet_offset = -1, padding_len = -1;
    unsigned char toc;
    int ret = opus_packet_parse_impl(pkt, len, sd, &toc, frames, size, &payload_offset, &packet_offset,
                                     &padding, &padding_len);
    printf("r %d %d", sd, ret);
    if (ret > 0) {
      printf(" %d %d %d", payload_offset, packet_offset, padding_len);
      for (i = 0; i < ret; i++) printf(" %d", size[i]);
    }
    printf("\n");
  }
  printf("l %d\n", opus_packet_has_lbrr(pkt, len));
}

int main(void) {
  int t, c;
  printf("# Packet parser results from libopus 1.5.2 (git tag v1.5.2) src/opus.c and\n");
  printf("# src/opus_decoder.c, written by tools/libopus-ref/packet_ref.c\n");
  printf("# (make -C tools/libopus-ref packet-vectors). The format is documented there.\n");
  for (t = 0; t < 256; t++) {
    unsigned char b = (unsigned char)t;
    printf("t %d %d %d %d\n", t, opus_packet_get_bandwidth(&b), opus_packet_get_samples_per_frame(&b, 48000),
           opus_packet_get_nb_channels(&b));
  }
  /* Every TOC byte alone and with a short payload. */
  for (t = 0; t < 256; t++) {
    pkt[0] = (unsigned char)t;
    report(1);
    pkt[0] = (unsigned char)t;
    fill(pkt + 1, 8);
    report(rnd_range(2, 9));
  }
  /* Built packets of every framing code. */
  for (c = 0; c < 2400; c++) report(build());
  /* Random bytes. */
  for (c = 0; c < 400; c++) {
    int len = rnd_range(1, 24);
    fill(pkt, len);
    report(len);
  }
  return 0;
}
