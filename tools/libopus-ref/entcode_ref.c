/* entcode_ref: reference vectors for the Go range decoder in
   opus/internal/entcode.

   The program encodes pseudo-random symbol sequences of every kind with
   libopus's range encoder (celt/entenc.c), decodes them with libopus's range
   decoder (celt/entdec.c), and prints every decoded value together with
   ec_tell(), ec_tell_frac(), rng and the error flag after that step. It also
   decodes truncated encodings and random bytes with random operations, where
   only libopus's behaviour defines the expected result, and exercises the two
   state changes the Opus decoder makes directly: shrinking the storage (the
   redundancy split in opus_decode_frame) and advancing nbits_total (the
   silence case of celt_decode_with_ec). The output is deterministic.

   Usage: entcode_ref > opus/internal/entcode/testdata/entdec.txt

   Output format, one record per line:
     t8 <id> <ftb> <n> <v0> ... <v(n-1)>    an 8-bit inverse CDF table
     t16 <id> <ftb> <n> <v0> ... <v(n-1)>   a 16-bit inverse CDF table
     case <kind> <storage> <hex bytes or ->  a new decoder over these bytes
     <op> <a> <b> <c> <result> <tell> <tell_frac> <rng> <error>
   where <op> is one of
     init    0 0 0          ec_dec_init (result 0)
     dec     ft fl fh       result = ec_decode(ft); then ec_dec_update(fl, fh, ft)
     bin     bits fl fh     result = ec_decode_bin(bits); then ec_dec_update(fl, fh, 1<<bits)
     logp    logp 0 0       result = ec_dec_bit_logp(logp)
     icdf    table ftb 0    result = ec_dec_icdf(t8[table], ftb)
     icdf16  table ftb 0    result = ec_dec_icdf16(t16[table], ftb)
     uint    ft 0 0         result = ec_dec_uint(ft)
     bits    n 0 0          result = ec_dec_bits(n)
     shrink  n 0 0          storage -= n; result = storage
     settell t 0 0          nbits_total += t - ec_tell(); result 0
   and the last four fields are the decoder state after the operation. All
   numbers are decimal. */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "entdec.h"
#include "entenc.h"

/* xorshift32 with a fixed seed, so the vectors never change. */
static opus_uint32 seed = 0x2545F491u;

static opus_uint32 rnd(void) {
  seed ^= seed << 13;
  seed ^= seed >> 17;
  seed ^= seed << 5;
  return seed;
}

/* rnd_range returns a value in [lo, hi]. */
static opus_uint32 rnd_range(opus_uint32 lo, opus_uint32 hi) {
  unsigned long long span = (unsigned long long)hi - lo + 1;
  return lo + (opus_uint32)(((unsigned long long)rnd() * span) >> 32);
}

enum { K_DEC, K_BIN, K_LOGP, K_ICDF, K_ICDF16, K_UINT, K_BITS, K_SHRINK, K_SETTELL };
static const char *const names[] = {"dec",    "bin",  "logp",   "icdf",   "icdf16",
                                    "uint",   "bits", "shrink", "settell"};

typedef struct {
  int kind;
  opus_uint32 a, b, c; /* parameters, see the format above */
  opus_uint32 v;       /* the encoded value of a valid sequence */
} op_t;

#define NTABLES 10
#define MAXSYMS 16
static unsigned char t8[NTABLES][MAXSYMS];
static int t8_ftb[NTABLES], t8_n[NTABLES];
static opus_uint16 t16[NTABLES][MAXSYMS];
static int t16_ftb[NTABLES], t16_n[NTABLES];

static int cmp_desc(const void *a, const void *b) {
  opus_uint32 x = *(const opus_uint32 *)a, y = *(const opus_uint32 *)b;
  return x < y ? 1 : x > y ? -1 : 0;
}

/* make_table fills a strictly decreasing inverse CDF ending in 0 whose first
   value is below 1<<ftb and at most maxv, so that every symbol has a nonzero
   probability. It returns the number of entries. */
static int make_table(opus_uint32 *v, int ftb, opus_uint32 maxv) {
  opus_uint32 top = (1u << ftb) - 1;
  int n, i, j;
  if (top > maxv) top = maxv;
  n = (int)rnd_range(2, top + 1 < MAXSYMS ? top + 1 : MAXSYMS);
  for (i = 0; i < n - 1; i++) {
    for (;;) {
      opus_uint32 x = rnd_range(1, top);
      for (j = 0; j < i && v[j] != x; j++) {
      }
      if (j == i) {
        v[i] = x;
        break;
      }
    }
  }
  qsort(v, n - 1, sizeof(*v), cmp_desc);
  v[n - 1] = 0;
  return n;
}

static void make_tables(void) {
  static const int ftb8[NTABLES] = {1, 2, 3, 4, 5, 6, 7, 8, 8, 8};
  static const int ftb16[NTABLES] = {2, 4, 8, 9, 10, 12, 13, 14, 15, 15};
  opus_uint32 v[MAXSYMS];
  int t, i;
  for (t = 0; t < NTABLES; t++) {
    t8_ftb[t] = ftb8[t];
    t8_n[t] = make_table(v, ftb8[t], 255);
    for (i = 0; i < t8_n[t]; i++) t8[t][i] = (unsigned char)v[i];
    printf("t8 %d %d %d", t, t8_ftb[t], t8_n[t]);
    for (i = 0; i < t8_n[t]; i++) printf(" %u", t8[t][i]);
    printf("\n");
  }
  for (t = 0; t < NTABLES; t++) {
    t16_ftb[t] = ftb16[t];
    t16_n[t] = make_table(v, ftb16[t], 65535);
    for (i = 0; i < t16_n[t]; i++) t16[t][i] = (opus_uint16)v[i];
    printf("t16 %d %d %d", t, t16_ftb[t], t16_n[t]);
    for (i = 0; i < t16_n[t]; i++) printf(" %u", t16[t][i]);
    printf("\n");
  }
}

/* random_ft returns a total frequency for ec_decode: mostly small alphabets
   as CELT and SILK use them, sometimes up to 1<<16. */
static opus_uint32 random_ft(void) {
  switch (rnd() % 3) {
  case 0: return rnd_range(2, 16);
  case 1: return rnd_range(17, 256);
  default: return rnd_range(257, 1u << 16);
  }
}

/* random_uint_ft returns a range for ec_dec_uint across both of its paths. */
static opus_uint32 random_uint_ft(void) {
  switch (rnd() % 4) {
  case 0: return rnd_range(2, 256);
  case 1: return rnd_range(257, 1u << 16);
  case 2: return rnd_range((1u << 16) + 1, 1u << 24);
  default: return rnd_range((1u << 24) + 1, 0xFFFFFFFFu);
  }
}

/* random_op draws an operation with parameters and, for encoding, a value. */
static op_t random_op(int allow_state_ops) {
  op_t op;
  memset(&op, 0, sizeof(op));
  for (;;) {
    op.kind = (int)(rnd() % 9);
    if (op.kind != K_SHRINK && op.kind != K_SETTELL) break;
    if (allow_state_ops && rnd() % 3 == 0) break;
  }
  switch (op.kind) {
  case K_DEC:
    op.a = random_ft();
    op.b = rnd_range(0, op.a - 1);
    op.c = rnd() % 2 ? op.b + 1 : rnd_range(op.b + 1, op.a);
    break;
  case K_BIN:
    op.a = rnd_range(1, 15);
    op.b = rnd_range(0, (1u << op.a) - 1);
    op.c = rnd() % 2 ? op.b + 1 : rnd_range(op.b + 1, 1u << op.a);
    break;
  case K_LOGP:
    op.a = rnd_range(1, 15);
    /* Mostly the likely value, as real streams code it. */
    op.v = rnd() % 4 == 0 ? rnd() % 2 : (rnd() % (1u << op.a)) == 0;
    break;
  case K_ICDF:
    op.a = rnd_range(0, NTABLES - 1);
    op.b = (opus_uint32)t8_ftb[op.a];
    op.v = rnd_range(0, (opus_uint32)t8_n[op.a] - 1);
    break;
  case K_ICDF16:
    op.a = rnd_range(0, NTABLES - 1);
    op.b = (opus_uint32)t16_ftb[op.a];
    op.v = rnd_range(0, (opus_uint32)t16_n[op.a] - 1);
    break;
  case K_UINT:
    op.a = random_uint_ft();
    op.v = rnd_range(0, op.a - 1);
    break;
  case K_BITS:
    op.a = rnd_range(1, 25);
    op.v = rnd() & ((1u << op.a) - 1);
    break;
  case K_SHRINK:
    op.a = rnd_range(0, 3);
    break;
  case K_SETTELL:
    op.a = rnd_range(0, 64); /* advance by this much past ec_tell() */
    break;
  }
  return op;
}

static void encode(ec_enc *enc, const op_t *ops, int nops) {
  int i;
  for (i = 0; i < nops; i++) {
    const op_t *op = &ops[i];
    switch (op->kind) {
    case K_DEC: ec_encode(enc, op->b, op->c, op->a); break;
    case K_BIN: ec_encode_bin(enc, op->b, op->c, op->a); break;
    case K_LOGP: ec_enc_bit_logp(enc, (int)op->v, op->a); break;
    case K_ICDF: ec_enc_icdf(enc, (int)op->v, t8[op->a], op->b); break;
    case K_ICDF16: ec_enc_icdf16(enc, (int)op->v, t16[op->a], op->b); break;
    case K_UINT: ec_enc_uint(enc, op->v, op->a); break;
    case K_BITS: ec_enc_bits(enc, op->v, op->a); break;
    default: fprintf(stderr, "state operation in an encoded sequence\n"); exit(1);
    }
  }
}

static void print_state(ec_dec *dec, const char *name, opus_uint32 a, opus_uint32 b, opus_uint32 c,
                        opus_uint32 result) {
  printf("%s %u %u %u %u %d %u %u %d\n", name, a, b, c, result, ec_tell(dec),
         ec_tell_frac(dec), dec->rng, ec_get_error(dec));
}

/* decode runs ops on a decoder over buf and prints every step. With check
   set, the sequence was encoded into buf and every value must come back. */
static void decode(const char *kind, unsigned char *buf, opus_uint32 storage, op_t *ops, int nops,
                   int check) {
  ec_dec dec;
  opus_uint32 i;
  int n;
  printf("case %s %u ", kind, storage);
  if (storage == 0) printf("-");
  for (i = 0; i < storage; i++) printf("%02x", buf[i]);
  printf("\n");
  ec_dec_init(&dec, buf, storage);
  print_state(&dec, "init", 0, 0, 0, 0);
  for (n = 0; n < nops; n++) {
    op_t *op = &ops[n];
    opus_uint32 r = 0;
    switch (op->kind) {
    case K_DEC:
    case K_BIN: {
      opus_uint32 ft = op->kind == K_DEC ? op->a : 1u << op->a;
      r = op->kind == K_DEC ? ec_decode(&dec, op->a) : ec_decode_bin(&dec, op->a);
      if (r < op->b || r >= op->c) {
        if (check) {
          fprintf(stderr, "%s: decoded %u outside [%u, %u)\n", names[op->kind], r, op->b, op->c);
          exit(1);
        }
        /* Any symbol containing the decoded frequency is a valid update. */
        op->b = rnd() % 2 ? r : rnd_range(0, r);
        op->c = rnd() % 2 ? r + 1 : rnd_range(r + 1, ft);
      }
      ec_dec_update(&dec, op->b, op->c, ft);
      break;
    }
    case K_LOGP: r = (opus_uint32)ec_dec_bit_logp(&dec, op->a); break;
    case K_ICDF: r = (opus_uint32)ec_dec_icdf(&dec, t8[op->a], op->b); break;
    case K_ICDF16: r = (opus_uint32)ec_dec_icdf16(&dec, t16[op->a], op->b); break;
    case K_UINT: r = ec_dec_uint(&dec, op->a); break;
    case K_BITS: r = ec_dec_bits(&dec, op->a); break;
    case K_SHRINK:
      if (op->a > dec.storage) op->a = dec.storage;
      dec.storage -= op->a;
      r = dec.storage;
      break;
    case K_SETTELL: {
      int tell = ec_tell(&dec) + (int)op->a;
      op->a = (opus_uint32)tell;
      dec.nbits_total += tell - ec_tell(&dec);
      break;
    }
    }
    if (check && op->kind != K_DEC && op->kind != K_BIN && r != op->v) {
      fprintf(stderr, "%s: decoded %u, encoded %u\n", names[op->kind], r, op->v);
      exit(1);
    }
    print_state(&dec, names[op->kind], op->a, op->b, op->c, r);
  }
}

#define MAXOPS 2048
#define MAXBUF 16384

/* encode_case encodes ops into the smallest buffer libopus accepts plus a few
   bytes of slack and returns its size. */
static opus_uint32 encode_case(unsigned char *buf, const op_t *ops, int nops) {
  ec_enc enc;
  opus_uint32 storage;
  ec_enc_init(&enc, buf, MAXBUF);
  encode(&enc, ops, nops);
  storage = ((opus_uint32)ec_tell(&enc) + 7) >> 3;
  storage += rnd_range(0, 2);
  for (;; storage++) {
    if (storage > MAXBUF) {
      fprintf(stderr, "encoding does not fit\n");
      exit(1);
    }
    ec_enc_init(&enc, buf, storage);
    encode(&enc, ops, nops);
    ec_enc_done(&enc);
    if (!ec_get_error(&enc)) return storage;
  }
}

int main(void) {
  static op_t ops[MAXOPS];
  static unsigned char buf[MAXBUF];
  int c, i, nops;
  opus_uint32 storage;

  printf("# Range decoder reference vectors from libopus 1.5.2 (git tag v1.5.2) celt/entenc.c,\n");
  printf("# celt/entdec.c and celt/entcode.c, written by tools/libopus-ref/entcode_ref.c\n");
  printf("# (make -C tools/libopus-ref entcode-vectors). The format is documented there.\n");
  make_tables();

  /* Encoded sequences, decoded in full. */
  for (c = 0; c < 64; c++) {
    nops = c == 63 ? 1200 : (int)rnd_range(1, c < 8 ? 4 : 96);
    for (i = 0; i < nops; i++) ops[i] = random_op(0);
    storage = encode_case(buf, ops, nops);
    decode("valid", buf, storage, ops, nops, 1);
  }
  /* The same, decoded from a truncated buffer and then past its end. */
  for (c = 0; c < 16; c++) {
    nops = (int)rnd_range(8, 96);
    for (i = 0; i < nops; i++) ops[i] = random_op(0);
    storage = encode_case(buf, ops, nops);
    storage = rnd_range(0, storage - 1);
    for (; i < nops + 16; i++) ops[i] = random_op(0);
    decode("truncated", buf, storage, ops, nops + 16, 0);
  }
  /* Random bytes with random operations, including the state changes. */
  for (c = 0; c < 40; c++) {
    storage = c < 2 ? (opus_uint32)c : rnd_range(0, 48);
    for (i = 0; i < (int)storage; i++) buf[i] = (unsigned char)rnd();
    nops = (int)rnd_range(1, 64);
    for (i = 0; i < nops; i++) ops[i] = random_op(1);
    decode("random", buf, storage, ops, nops, 0);
  }
  return 0;
}
