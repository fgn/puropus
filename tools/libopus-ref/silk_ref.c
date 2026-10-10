/* silk_ref: reference vectors for the Go SILK decoder in opus/internal/silk.

   Usage:
     silk_ref units > opus/internal/silk/testdata/units.txt
     silk_ref plc name=packets.bit ... > opus/internal/silk/testdata/plc.txt
     silk_ref loss name=packets.bit ... > opus/internal/silk/testdata/loss.txt
     silk_ref fec name=packets.bit ... > opus/internal/silk/testdata/fec.txt

   "units" calls libopus's SILK decoder stages on seeded pseudo-random and
   edge-case input and prints input and output, one case per line (all
   numbers decimal, byte strings hex):

     nlsf2a d nlsf[d] a_Q12[d]                    silk_NLSF2A
     nlsfdec cb indices[order+1] nlsf[order]      silk_NLSF_decode, cb 0 NB/MB, 1 WB
     stabilize L deltamin[L+1] in[L] out[L]       silk_NLSF_stabilize
     invgain order a_Q12[order] result            silk_LPC_inverse_pred_gain
     gains conditional nb prev ind[nb] prev' g[nb] silk_gains_dequant
     pitch fs nb lagIndex contour lags[nb]        silk_decode_pitch
     shell n sums[n] bytes hex out[16n] tell rng  silk_shell_decoder of n blocks
     pulses type offset len bytes hex out[len16] tell rng
                                                  silk_decode_pulses (len16 is len
                                                  rounded up to 16)
     ms2lr fs len pred[2] state[6] x1[len+2] x2[len+2] x1'[len+2] x2'[len+2] state'[6]
                                                  silk_stereo_MS_to_LR; state is
                                                  pred_prev_Q13[2] sMid[2] sSide[2]
     resample in out nchunks chunk[n] x[sum] y[sum*out/in]
                                                  silk_resampler_init(in, out, 0)
                                                  and silk_resampler per chunk

   The shell and pulse cases encode random pulses with libopus's encoder
   (silk_shell_encoder, silk_encode_pulses) into exactly as many bytes as
   needed and print what libopus's decoder makes of them, with ec_tell() and
   the range after decoding.

   "plc" decodes each SILK-only packet list (opus_demo format, code 0 packets)
   with silk_Decode for one output channel at 48 kHz, as opus_decode_frame
   does, and takes snapshots of channel 0 after the first packet past the
   fifth whose frame was voiced, unvoiced and inactive. From each snapshot,
   and from freshly reset states, it conceals four lost frames with
   silk_decode_frame (lostFlag FLAG_PACKET_LOST: silk_PLC, silk_CNG and
   silk_PLC_glue_frames) and prints the decoder state before the first and
   after the last frame, and the concealed samples:

     state case step field values...              silk_decoder_state field (step
                                                  0 before, 4 after)
     plcout case step n out[n]                    concealed frame `step`

   "loss" decodes each packet list with libopus's float API, as decode_float
   does (opus_decoder_create(48000, 1), opus_decode_float with decode_fec 0),
   with the records whose index i has i % 9 == 4 or 30 <= i < 34 lost: they
   decode as opus_decode_float(st, NULL, 0, pcm, n, 0) with n the last packet
   duration (OPUS_GET_LAST_PACKET_DURATION). For every record it prints

     loss name index lost samples final-range fnv

   where fnv is the 64-bit FNV-1a hash (hex) of the record's float32
   little-endian output.

   "fec" is "loss" with in-band FEC: a lost record followed by a received
   one decodes as opus_decode_float(st, next, len, pcm, n, 1), which decodes
   the LBRR copy of the lost frame from the next packet (silk_Decode with
   FLAG_DECODE_LBRR); its lines start with "fec".

   Pointer fields print as table numbers: psNLSF_CB 0 (NB/MB) or 1 (WB),
   pitch_lag_low_bits_iCDF 4, 6 or 8 (uniformN), pitch_contour_iCDF 0 (20 ms),
   1 (20 ms NB), 2 (10 ms), 3 (10 ms NB), -1 for NULL. The output is
   deterministic (fixed seed) and identical for the default and the generic
   build: the SILK decoder is integer code. */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "API.h"
#include "opus.h"
#include "entdec.h"
#include "entenc.h"
#include "main.h"
#include "resampler_rom.h"

/* xorshift32 with a fixed seed, so the vectors never change. */
static opus_uint32 seed = 0x51C0DE5u;

static opus_uint32 rnd(void) {
  seed ^= seed << 13;
  seed ^= seed >> 17;
  seed ^= seed << 5;
  return seed;
}

/* rnd_range returns a value in [lo, hi]. */
static int rnd_range(int lo, int hi) {
  unsigned long long span = (unsigned long long)((long long)hi - lo + 1);
  return lo + (int)(((unsigned long long)rnd() * span) >> 32);
}

static opus_uint32 be32(const unsigned char *p) {
  return (opus_uint32)p[0] << 24 | (opus_uint32)p[1] << 16 | (opus_uint32)p[2] << 8 | p[3];
}

static void print_ints(const int *v, int n) {
  int i;
  for (i = 0; i < n; i++) printf(" %d", v[i]);
}

static void print_i16(const opus_int16 *v, int n) {
  int i;
  for (i = 0; i < n; i++) printf(" %d", v[i]);
}

static void print_i32(const opus_int32 *v, int n) {
  int i;
  for (i = 0; i < n; i++) printf(" %d", (int)v[i]);
}

static void print_hex(const unsigned char *b, int n) {
  int i;
  printf(" %d ", n);
  if (n == 0) printf("-");
  for (i = 0; i < n; i++) printf("%02x", b[i]);
}

static int cmp_i16(const void *a, const void *b) {
  return *(const opus_int16 *)a - *(const opus_int16 *)b;
}

/* ---- NLSF to LPC ---- */

static void nlsf2a_case(const opus_int16 *nlsf, int d) {
  opus_int16 a[SILK_MAX_ORDER_LPC];
  silk_NLSF2A(a, nlsf, d, 0);
  printf("nlsf2a %d", d);
  print_i16(nlsf, d);
  print_i16(a, d);
  printf("\n");
}

static void nlsf2a_vectors(void) {
  static const int orders[2] = {10, 16};
  opus_int16 nlsf[16];
  int o, d, i, k;
  for (o = 0; o < 2; o++) {
    d = orders[o];
    /* Random sorted vectors over the whole range. */
    for (k = 0; k < 150; k++) {
      for (i = 0; i < d; i++) nlsf[i] = (opus_int16)rnd_range(0, 32767);
      qsort(nlsf, d, sizeof(nlsf[0]), cmp_i16);
      nlsf2a_case(nlsf, d);
    }
    /* Random unsorted vectors. */
    for (k = 0; k < 50; k++) {
      for (i = 0; i < d; i++) nlsf[i] = (opus_int16)rnd_range(0, 32767);
      nlsf2a_case(nlsf, d);
    }
    /* Tight clusters, which need bandwidth expansion to become stable. */
    for (k = 0; k < 50; k++) {
      int start = rnd_range(0, 32767 - 4 * d), step = rnd_range(0, 4);
      for (i = 0; i < d; i++) nlsf[i] = (opus_int16)(start + i * step);
      nlsf2a_case(nlsf, d);
    }
    /* Edges: all zero, all maximal, evenly spaced, extremes. */
    for (i = 0; i < d; i++) nlsf[i] = 0;
    nlsf2a_case(nlsf, d);
    for (i = 0; i < d; i++) nlsf[i] = 32767;
    nlsf2a_case(nlsf, d);
    for (i = 0; i < d; i++) nlsf[i] = (opus_int16)((i + 1) * 32767 / (d + 1));
    nlsf2a_case(nlsf, d);
    for (i = 0; i < d; i++) nlsf[i] = (opus_int16)(i < d / 2 ? i : 32767 - (d - i));
    nlsf2a_case(nlsf, d);
    for (i = 0; i < d; i++) nlsf[i] = (opus_int16)(i & 1 ? 32767 : 0);
    nlsf2a_case(nlsf, d);
  }
}

/* ---- NLSF decoding ---- */

static void nlsfdec_vectors(void) {
  const silk_NLSF_CB_struct *cbs[2] = {&silk_NLSF_CB_NB_MB, &silk_NLSF_CB_WB};
  opus_int8 ind[MAX_LPC_ORDER + 1];
  opus_int16 nlsf[MAX_LPC_ORDER];
  int c, k, i, order;
  for (c = 0; c < 2; c++) {
    order = cbs[c]->order;
    for (k = 0; k < 200; k++) {
      ind[0] = (opus_int8)rnd_range(0, cbs[c]->nVectors - 1);
      for (i = 1; i <= order; i++) {
        /* Mostly small residuals, sometimes the extended range. */
        ind[i] = (opus_int8)(rnd_range(0, 7) ? rnd_range(-4, 4) : rnd_range(-10, 10));
      }
      silk_NLSF_decode(nlsf, ind, cbs[c]);
      printf("nlsfdec %d", c);
      for (i = 0; i <= order; i++) printf(" %d", ind[i]);
      print_i16(nlsf, order);
      printf("\n");
    }
  }
}

/* ---- NLSF stabilization ---- */

static void stabilize_case(const opus_int16 *dmin, const opus_int16 *in, int L) {
  opus_int16 out[MAX_LPC_ORDER];
  memcpy(out, in, L * sizeof(out[0]));
  silk_NLSF_stabilize(out, dmin, L);
  printf("stabilize %d", L);
  print_i16(dmin, L + 1);
  print_i16(in, L);
  print_i16(out, L);
  printf("\n");
}

static void stabilize_vectors(void) {
  const silk_NLSF_CB_struct *cbs[2] = {&silk_NLSF_CB_NB_MB, &silk_NLSF_CB_WB};
  opus_int16 dmin[MAX_LPC_ORDER + 1], in[MAX_LPC_ORDER];
  int c, k, i, L, total;
  for (c = 0; c < 4; c++) {
    L = c & 1 ? 16 : 10;
    for (k = 0; k < 100; k++) {
      if (c < 2) {
        memcpy(dmin, cbs[c]->deltaMin_Q15, (L + 1) * sizeof(dmin[0]));
      } else {
        /* Random minimum distances that still fit. */
        do {
          total = 0;
          for (i = 0; i <= L; i++) {
            dmin[i] = (opus_int16)rnd_range(1, rnd_range(0, 3) ? 300 : 3000);
            total += dmin[i];
          }
        } while (total >= 32768);
      }
      switch (k % 4) {
        case 0: /* sorted, often too close */
          for (i = 0; i < L; i++) in[i] = (opus_int16)rnd_range(0, 32767);
          qsort(in, L, sizeof(in[0]), cmp_i16);
          break;
        case 1: /* unsorted */
          for (i = 0; i < L; i++) in[i] = (opus_int16)rnd_range(0, 32767);
          break;
        case 2: /* clustered, near a border */
          total = rnd_range(0, 1) ? rnd_range(0, 200) : rnd_range(32500, 32767);
          for (i = 0; i < L; i++) in[i] = (opus_int16)(total + rnd_range(-50, 50));
          break;
        default: /* reversed, which takes the fallback after MAX_LOOPS */
          for (i = 0; i < L; i++) in[i] = (opus_int16)(32000 - i * rnd_range(0, 2000));
          break;
      }
      stabilize_case(dmin, in, L);
    }
  }
}

/* ---- Inverse prediction gain ---- */

static void invgain_case(const opus_int16 *a, int order) {
  opus_int32 r = silk_LPC_inverse_pred_gain_c(a, order);
  printf("invgain %d", order);
  print_i16(a, order);
  printf(" %d\n", (int)r);
}

static void invgain_vectors(void) {
  opus_int16 a[SILK_MAX_ORDER_LPC], nlsf[16];
  int order, k, i, scale;
  for (order = 1; order <= SILK_MAX_ORDER_LPC; order++) {
    for (k = 0; k < 20; k++) {
      /* Coefficients from small (stable) to large (unstable). */
      scale = 64 << rnd_range(0, 9);
      for (i = 0; i < order; i++) a[i] = (opus_int16)rnd_range(-scale, scale - 1);
      invgain_case(a, order);
    }
  }
  /* Stable filters from NLSFs, as silk_NLSF2A and the PLC see them. */
  for (k = 0; k < 100; k++) {
    order = k & 1 ? 16 : 10;
    for (i = 0; i < order; i++) nlsf[i] = (opus_int16)rnd_range(0, 32767);
    qsort(nlsf, order, sizeof(nlsf[0]), cmp_i16);
    silk_NLSF2A(a, nlsf, order, 0);
    invgain_case(a, order);
    /* Scaled up beyond stability. */
    for (i = 0; i < order; i++) a[i] = (opus_int16)(a[i] * 3 / 2);
    invgain_case(a, order);
  }
  /* DC response at the limit. */
  for (i = 0; i < 10; i++) a[i] = 410;
  invgain_case(a, 10);
  for (i = 0; i < 10; i++) a[i] = 409;
  invgain_case(a, 10);
}

/* ---- Gain dequantization ---- */

static void gains_vectors(void) {
  opus_int32 g[MAX_NB_SUBFR];
  opus_int8 ind[MAX_NB_SUBFR], prev, prev0;
  int k, i, nb, cond;
  for (k = 0; k < 400; k++) {
    nb = k & 1 ? 4 : 2;
    cond = (k >> 1) & 1;
    prev0 = (opus_int8)rnd_range(0, N_LEVELS_QGAIN - 1);
    for (i = 0; i < nb; i++) {
      if (i == 0 && !cond) {
        ind[i] = (opus_int8)rnd_range(0, N_LEVELS_QGAIN - 1);
      } else {
        ind[i] = (opus_int8)rnd_range(0, MAX_DELTA_GAIN_QUANT - MIN_DELTA_GAIN_QUANT);
      }
    }
    prev = prev0;
    silk_gains_dequant(g, ind, &prev, cond, nb);
    printf("gains %d %d %d", cond, nb, prev0);
    for (i = 0; i < nb; i++) printf(" %d", ind[i]);
    printf(" %d", prev);
    print_i32(g, nb);
    printf("\n");
  }
}

/* ---- Pitch lags ---- */

static void pitch_vectors(void) {
  static const int fss[3] = {8, 12, 16};
  int f, nb, c, l, k, ncontours, lags[MAX_NB_SUBFR], maxIndex;
  for (f = 0; f < 3; f++) {
    for (nb = 2; nb <= 4; nb += 2) {
      if (fss[f] == 8) {
        ncontours = nb == 4 ? 11 : 3;
      } else {
        ncontours = nb == 4 ? 34 : 12;
      }
      /* The largest absolutely coded index, then deltas beyond both ends. */
      maxIndex = 32 * (fss[f] >> 1) - 1;
      {
        const int ls[] = {-30, -9, -1, 0, 1, 2, 7, 50, 100, maxIndex - 1, maxIndex, maxIndex + 1, maxIndex + 11, 300};
        for (c = 0; c < ncontours; c++) {
          for (l = 0; l < (int)(sizeof(ls) / sizeof(ls[0])); l++) {
            silk_decode_pitch((opus_int16)ls[l], (opus_int8)c, lags, fss[f], nb);
            printf("pitch %d %d %d %d", fss[f], nb, ls[l], c);
            for (k = 0; k < nb; k++) printf(" %d", lags[k]);
            printf("\n");
          }
        }
      }
    }
  }
}

/* ---- Shell decoding ---- */

#define MAXBUF 4096

/* encode_exact runs an encoder over a buffer of the smallest size that holds
   it and returns that size. */
static int encode_exact(unsigned char *buf, void (*enc_fn)(ec_enc *, void *), void *arg) {
  ec_enc enc;
  int storage;
  ec_enc_init(&enc, buf, MAXBUF);
  enc_fn(&enc, arg);
  storage = (ec_tell(&enc) + 7) >> 3;
  for (;; storage++) {
    ec_enc_init(&enc, buf, storage);
    enc_fn(&enc, arg);
    ec_enc_done(&enc);
    if (!ec_get_error(&enc)) return storage;
  }
}

typedef struct {
  int n;
  opus_int pulses[8][SHELL_CODEC_FRAME_LENGTH];
} shell_arg;

static void shell_encode(ec_enc *enc, void *a) {
  shell_arg *s = a;
  int b;
  for (b = 0; b < s->n; b++) silk_shell_encoder(enc, s->pulses[b]);
}

static void shell_vectors(void) {
  shell_arg s;
  unsigned char buf[MAXBUF];
  opus_int16 out[8 * SHELL_CODEC_FRAME_LENGTH];
  int sums[8];
  int k, b, i, storage;
  ec_dec dec;
  for (k = 0; k < 300; k++) {
    s.n = rnd_range(1, 8);
    for (b = 0; b < s.n; b++) {
      memset(s.pulses[b], 0, sizeof(s.pulses[b]));
      sums[b] = rnd_range(1, SILK_MAX_PULSES);
      for (i = 0; i < sums[b]; i++) {
        /* Clustered or spread pulses. */
        int pos = k & 1 ? rnd_range(0, 15) : rnd_range(0, 3) * 4 + rnd_range(0, 1);
        s.pulses[b][pos]++;
      }
    }
    storage = encode_exact(buf, shell_encode, &s);
    ec_dec_init(&dec, buf, storage);
    for (b = 0; b < s.n; b++) silk_shell_decoder(&out[b * SHELL_CODEC_FRAME_LENGTH], &dec, sums[b]);
    printf("shell %d", s.n);
    print_ints(sums, s.n);
    print_hex(buf, storage);
    print_i16(out, s.n * SHELL_CODEC_FRAME_LENGTH);
    printf(" %d %u\n", ec_tell(&dec), (unsigned)dec.rng);
  }
}

/* ---- Pulse decoding ---- */

typedef struct {
  int type, offset, len;
  opus_int8 pulses[MAX_FRAME_LENGTH + SHELL_CODEC_FRAME_LENGTH];
} pulses_arg;

static void pulses_encode(ec_enc *enc, void *a) {
  pulses_arg *p = a;
  opus_int8 copy[MAX_FRAME_LENGTH + SHELL_CODEC_FRAME_LENGTH];
  memcpy(copy, p->pulses, sizeof(copy));
  silk_encode_pulses(enc, p->type, p->offset, copy, p->len);
}

static void pulses_vectors(void) {
  static const int lens[5] = {80, 120, 160, 240, 320};
  pulses_arg p;
  unsigned char buf[MAXBUF];
  opus_int16 out[MAX_FRAME_LENGTH];
  int k, i, storage, len16, big;
  ec_dec dec;
  for (k = 0; k < 120; k++) {
    p.type = rnd_range(0, 2);
    p.offset = rnd_range(0, 1);
    p.len = lens[rnd_range(0, 4)];
    big = rnd_range(0, 3); /* how often large pulses (LSB coding) occur */
    memset(p.pulses, 0, sizeof(p.pulses));
    for (i = 0; i < p.len; i++) {
      int r = rnd_range(0, 99);
      if (r < 50) {
        p.pulses[i] = 0;
      } else if (r < 97 - 3 * big) {
        p.pulses[i] = (opus_int8)rnd_range(-3, 3);
      } else {
        p.pulses[i] = (opus_int8)rnd_range(-127, 127);
      }
    }
    storage = encode_exact(buf, pulses_encode, &p);
    ec_dec_init(&dec, buf, storage);
    silk_decode_pulses(&dec, out, p.type, p.offset, p.len);
    len16 = (p.len + SHELL_CODEC_FRAME_LENGTH - 1) & ~(SHELL_CODEC_FRAME_LENGTH - 1);
    printf("pulses %d %d %d", p.type, p.offset, p.len);
    print_hex(buf, storage);
    print_i16(out, len16);
    printf(" %d %u\n", ec_tell(&dec), (unsigned)dec.rng);
  }
}

/* ---- Mid/side to left/right ---- */

static opus_int16 rnd_sample(int loud) {
  if (loud && rnd_range(0, 9) == 0) return rnd_range(0, 1) ? 32767 : -32768;
  return (opus_int16)rnd_range(loud ? -32768 : -3000, loud ? 32767 : 3000);
}

static void ms2lr_vectors(void) {
  static const int fss[3] = {8, 12, 16};
  stereo_dec_state st;
  opus_int16 x1[MAX_FRAME_LENGTH + 2], x2[MAX_FRAME_LENGTH + 2];
  opus_int32 pred[2];
  int k, i, fs, len, loud;
  for (k = 0; k < 24; k++) {
    fs = fss[k % 3];
    len = fs * ((k / 3) & 1 ? 20 : 10);
    loud = (k / 6) & 1;
    for (i = 0; i < 2; i++) {
      /* Quantized predictors as silk_stereo_decode_pred makes them, or any
         value, which silk_SMULBB truncates. */
      if (k % 3) {
        pred[i] = silk_stereo_pred_quant_Q13[rnd_range(0, STEREO_QUANT_TAB_SIZE - 1)];
      } else {
        pred[i] = rnd_range(-30000, 30000);
      }
      st.pred_prev_Q13[i] = (opus_int16)rnd_range(-14000, 14000);
      st.sMid[i] = rnd_sample(loud);
      st.sSide[i] = rnd_sample(loud);
    }
    pred[0] -= pred[1];
    for (i = 0; i < len + 2; i++) {
      x1[i] = rnd_sample(loud);
      x2[i] = rnd_sample(loud);
    }
    printf("ms2lr %d %d %d %d", fs, len, (int)pred[0], (int)pred[1]);
    print_i16(st.pred_prev_Q13, 2);
    print_i16(st.sMid, 2);
    print_i16(st.sSide, 2);
    print_i16(x1, len + 2);
    print_i16(x2, len + 2);
    silk_stereo_MS_to_LR(&st, x1, x2, pred, fs, len);
    print_i16(x1, len + 2);
    print_i16(x2, len + 2);
    print_i16(st.pred_prev_Q13, 2);
    print_i16(st.sMid, 2);
    print_i16(st.sSide, 2);
    printf("\n");
  }
}

/* ---- Resampler ---- */

static void resample_vectors(void) {
  static const int pairs[][2] = {{8, 12}, {8, 16}, {8, 24}, {8, 48}, {12, 16}, {12, 24}, {12, 48},
                                 {16, 24}, {16, 48}, {8, 8}, {12, 12}, {16, 16}};
  static const int chunks_ms[] = {10, 20, 10};
  silk_resampler_state_struct S;
  opus_int16 x[40 * 16], y[40 * 48];
  int p, c, i, nchunks = 3, total, fin, fout, pos, outpos;
  double phase = 0;
  for (p = 0; p < (int)(sizeof(pairs) / sizeof(pairs[0])); p++) {
    fin = pairs[p][0];
    fout = pairs[p][1];
    total = 0;
    for (c = 0; c < nchunks; c++) total += chunks_ms[c] * fin;
    /* A chirp with noise and a clipped burst. */
    for (i = 0; i < total; i++) {
      phase += 0.02 + 0.6 * i / total;
      x[i] = (opus_int16)(12000 * ((phase - (int)phase) - 0.5) + rnd_range(-2000, 2000));
      if (i > total / 2 && i < total / 2 + 3 * fin) x[i] = (opus_int16)(i & 2 ? 32767 : -32768);
    }
    if (silk_resampler_init(&S, fin * 1000, fout * 1000, 0) != 0) {
      fprintf(stderr, "silk_resampler_init(%d, %d) failed\n", fin, fout);
      exit(1);
    }
    pos = outpos = 0;
    for (c = 0; c < nchunks; c++) {
      int n = chunks_ms[c] * fin;
      silk_resampler(&S, &y[outpos], &x[pos], n);
      pos += n;
      outpos += chunks_ms[c] * fout;
    }
    printf("resample %d %d %d", fin * 1000, fout * 1000, nchunks);
    for (c = 0; c < nchunks; c++) printf(" %d", chunks_ms[c] * fin);
    print_i16(x, total);
    print_i16(y, outpos);
    printf("\n");
  }
}

/* ---- PLC and CNG on recorded state ---- */

static int contour_table(const opus_uint8 *t) {
  if (t == NULL) return -1;
  if (t == silk_pitch_contour_iCDF) return 0;
  if (t == silk_pitch_contour_NB_iCDF) return 1;
  if (t == silk_pitch_contour_10_ms_iCDF) return 2;
  if (t == silk_pitch_contour_10_ms_NB_iCDF) return 3;
  fprintf(stderr, "unknown pitch contour table\n");
  exit(1);
}

static int lag_table(const opus_uint8 *t) {
  if (t == NULL) return -1;
  if (t == silk_uniform4_iCDF) return 4;
  if (t == silk_uniform6_iCDF) return 6;
  if (t == silk_uniform8_iCDF) return 8;
  fprintf(stderr, "unknown pitch lag table\n");
  exit(1);
}

static const char *cur_case;
static int cur_step;

static void field_begin(const char *name) { printf("state %s %d %s", cur_case, cur_step, name); }

static void f1(const char *name, int v) {
  field_begin(name);
  printf(" %d\n", v);
}

static void fi16(const char *name, const opus_int16 *v, int n) {
  field_begin(name);
  print_i16(v, n);
  printf("\n");
}

static void fi32(const char *name, const opus_int32 *v, int n) {
  field_begin(name);
  print_i32(v, n);
  printf("\n");
}

static void fint(const char *name, const int *v, int n) {
  field_begin(name);
  print_ints(v, n);
  printf("\n");
}

static void fi8(const char *name, const opus_int8 *v, int n) {
  int i;
  field_begin(name);
  for (i = 0; i < n; i++) printf(" %d", v[i]);
  printf("\n");
}

static void dump_state(const silk_decoder_state *s) {
  const silk_resampler_state_struct *r = &s->resampler_state;
  const SideInfoIndices *x = &s->indices;
  f1("prev_gain_Q16", s->prev_gain_Q16);
  fi32("exc_Q14", s->exc_Q14, MAX_FRAME_LENGTH);
  fi32("sLPC_Q14_buf", s->sLPC_Q14_buf, MAX_LPC_ORDER);
  fi16("outBuf", s->outBuf, MAX_FRAME_LENGTH + 2 * MAX_SUB_FRAME_LENGTH);
  f1("lagPrev", s->lagPrev);
  f1("LastGainIndex", s->LastGainIndex);
  f1("fs_kHz", s->fs_kHz);
  f1("fs_API_hz", s->fs_API_hz);
  f1("nb_subfr", s->nb_subfr);
  f1("frame_length", s->frame_length);
  f1("subfr_length", s->subfr_length);
  f1("ltp_mem_length", s->ltp_mem_length);
  f1("LPC_order", s->LPC_order);
  fi16("prevNLSF_Q15", s->prevNLSF_Q15, MAX_LPC_ORDER);
  f1("first_frame_after_reset", s->first_frame_after_reset);
  f1("pitch_lag_low_bits_iCDF", lag_table(s->pitch_lag_low_bits_iCDF));
  f1("pitch_contour_iCDF", contour_table(s->pitch_contour_iCDF));
  f1("nFramesDecoded", s->nFramesDecoded);
  f1("nFramesPerPacket", s->nFramesPerPacket);
  f1("ec_prevSignalType", s->ec_prevSignalType);
  f1("ec_prevLagIndex", s->ec_prevLagIndex);
  fint("VAD_flags", s->VAD_flags, MAX_FRAMES_PER_PACKET);
  f1("LBRR_flag", s->LBRR_flag);
  fint("LBRR_flags", s->LBRR_flags, MAX_FRAMES_PER_PACKET);
  fi32("resampler.sIIR", r->sIIR, SILK_RESAMPLER_MAX_IIR_ORDER);
  fi16("resampler.sFIR", r->sFIR.i16, RESAMPLER_ORDER_FIR_12);
  fi16("resampler.delayBuf", r->delayBuf, 48);
  f1("resampler.resampler_function", r->resampler_function);
  f1("resampler.batchSize", r->batchSize);
  f1("resampler.invRatio_Q16", r->invRatio_Q16);
  f1("resampler.Fs_in_kHz", r->Fs_in_kHz);
  f1("resampler.Fs_out_kHz", r->Fs_out_kHz);
  f1("resampler.inputDelay", r->inputDelay);
  f1("psNLSF_CB", s->psNLSF_CB == NULL ? -1 : s->psNLSF_CB == &silk_NLSF_CB_WB ? 1 : 0);
  fi8("indices.GainsIndices", x->GainsIndices, MAX_NB_SUBFR);
  fi8("indices.LTPIndex", x->LTPIndex, MAX_NB_SUBFR);
  fi8("indices.NLSFIndices", x->NLSFIndices, MAX_LPC_ORDER + 1);
  f1("indices.lagIndex", x->lagIndex);
  f1("indices.contourIndex", x->contourIndex);
  f1("indices.signalType", x->signalType);
  f1("indices.quantOffsetType", x->quantOffsetType);
  f1("indices.NLSFInterpCoef_Q2", x->NLSFInterpCoef_Q2);
  f1("indices.PERIndex", x->PERIndex);
  f1("indices.LTP_scaleIndex", x->LTP_scaleIndex);
  f1("indices.Seed", x->Seed);
  fi32("sCNG.CNG_exc_buf_Q14", s->sCNG.CNG_exc_buf_Q14, MAX_FRAME_LENGTH);
  fi16("sCNG.CNG_smth_NLSF_Q15", s->sCNG.CNG_smth_NLSF_Q15, MAX_LPC_ORDER);
  fi32("sCNG.CNG_synth_state", s->sCNG.CNG_synth_state, MAX_LPC_ORDER);
  f1("sCNG.CNG_smth_Gain_Q16", s->sCNG.CNG_smth_Gain_Q16);
  f1("sCNG.rand_seed", s->sCNG.rand_seed);
  f1("sCNG.fs_kHz", s->sCNG.fs_kHz);
  f1("lossCnt", s->lossCnt);
  f1("prevSignalType", s->prevSignalType);
  f1("sPLC.pitchL_Q8", s->sPLC.pitchL_Q8);
  fi16("sPLC.LTPCoef_Q14", s->sPLC.LTPCoef_Q14, LTP_ORDER);
  fi16("sPLC.prevLPC_Q12", s->sPLC.prevLPC_Q12, MAX_LPC_ORDER);
  f1("sPLC.last_frame_lost", s->sPLC.last_frame_lost);
  f1("sPLC.rand_seed", s->sPLC.rand_seed);
  f1("sPLC.randScale_Q14", s->sPLC.randScale_Q14);
  f1("sPLC.conc_energy", s->sPLC.conc_energy);
  f1("sPLC.conc_energy_shift", s->sPLC.conc_energy_shift);
  f1("sPLC.prevLTP_scale_Q14", s->sPLC.prevLTP_scale_Q14);
  fi32("sPLC.prevGain_Q16", s->sPLC.prevGain_Q16, 2);
  f1("sPLC.fs_kHz", s->sPLC.fs_kHz);
  f1("sPLC.nb_subfr", s->sPLC.nb_subfr);
  f1("sPLC.subfr_length", s->sPLC.subfr_length);
}

/* conceal dumps state s as case name, conceals four lost frames and dumps
   the state after them. */
static void conceal(const char *name, silk_decoder_state *s) {
  opus_int16 out[MAX_FRAME_LENGTH];
  opus_int32 n;
  int step;
  cur_case = name;
  cur_step = 0;
  dump_state(s);
  for (step = 1; step <= 4; step++) {
    silk_decode_frame(s, NULL, out, &n, FLAG_PACKET_LOST, CODE_INDEPENDENTLY, 0);
    cur_step = step;
    printf("plcout %s %d %d", name, step, (int)n);
    print_i16(out, n);
    printf("\n");
  }
  dump_state(s);
}

static void plc_stream(const char *name, const char *path) {
  static const char *kinds[3] = {"inactive", "unvoiced", "voiced"};
  static unsigned char packet[1500];
  FILE *f = fopen(path, "rb");
  int size;
  void *dec;
  silk_DecControlStruct ctl;
  opus_int16 pcm[5760];
  int index = 0, done[3] = {0, 0, 0};
  unsigned char head[8];
  char case_name[256];
  if (f == NULL || silk_Get_Decoder_Size(&size) != 0) {
    fprintf(stderr, "%s: cannot open\n", path);
    exit(1);
  }
  dec = calloc(1, size);
  silk_InitDecoder(dec);
  memset(&ctl, 0, sizeof(ctl));
  ctl.nChannelsAPI = 1;
  ctl.API_sampleRate = 48000;
  while (fread(head, 1, 8, f) == 8) {
    int len = (int)be32(head), config, ms, decoded = 0, frame48;
    ec_dec ec;
    silk_decoder_state *s0 = (silk_decoder_state *)dec; /* channel_state[0] */
    if (len < 2 || len > (int)sizeof(packet) || fread(packet, 1, len, f) != (size_t)len) {
      fprintf(stderr, "%s: unsupported record %d\n", path, index);
      exit(1);
    }
    config = packet[0] >> 3;
    if (config >= 12 || (packet[0] & 3) != 0) {
      fprintf(stderr, "%s: record %d is not a SILK-only code 0 packet\n", path, index);
      exit(1);
    }
    ms = 10 << (config & 3);
    if (ms == 80) ms = 60;
    frame48 = ms * 48;
    ctl.nChannelsInternal = packet[0] & 4 ? 2 : 1;
    ctl.internalSampleRate = config < 4 ? 8000 : config < 8 ? 12000 : 16000;
    ctl.payloadSize_ms = ms;
    ec_dec_init(&ec, packet + 1, len - 1);
    do {
      opus_int32 n;
      if (silk_Decode(dec, &ctl, FLAG_DECODE_NORMAL, decoded == 0, &ec, pcm, &n, 0) != 0) {
        fprintf(stderr, "%s: record %d: silk_Decode failed\n", path, index);
        exit(1);
      }
      decoded += n;
    } while (decoded < frame48);
    if (index >= 5 && !done[s0->prevSignalType]) {
      silk_decoder_state copy = *s0;
      done[s0->prevSignalType] = 1;
      snprintf(case_name, sizeof(case_name), "%s/%s@%d", name, kinds[s0->prevSignalType], index);
      conceal(case_name, &copy);
    }
    index++;
  }
  fclose(f);
  free(dec);
}

/* ---- Loss on whole streams ---- */

#define MAX_RECORDS 2000

/* is_lost is the loss pattern of "loss" and "fec". */
static int is_lost(int index) { return index % 9 == 4 || (index >= 30 && index < 34); }

static void loss_stream(const char *kind, const char *name, const char *path, int fec) {
  static unsigned char packets[MAX_RECORDS][1500];
  static int lens[MAX_RECORDS];
  unsigned char head[8];
  float pcm[5760];
  FILE *f = fopen(path, "rb");
  int err, index, count;
  OpusDecoder *st = opus_decoder_create(48000, 1, &err);
  if (f == NULL || err != OPUS_OK) {
    fprintf(stderr, "%s: cannot open\n", path);
    exit(1);
  }
  for (count = 0; count < MAX_RECORDS && fread(head, 1, 8, f) == 8; count++) {
    lens[count] = (int)be32(head);
    if (lens[count] > 1500 || fread(packets[count], 1, lens[count], f) != (size_t)lens[count]) {
      fprintf(stderr, "%s: bad record %d\n", path, count);
      exit(1);
    }
  }
  fclose(f);
  for (index = 0; index < count; index++) {
    int lost = is_lost(index), n, i, b;
    opus_uint32 rng = 0;
    unsigned long long h = 0xcbf29ce484222325ull;
    if (lost) {
      opus_int32 duration = 0;
      opus_decoder_ctl(st, OPUS_GET_LAST_PACKET_DURATION(&duration));
      if (duration <= 0) duration = 960;
      if (fec && index + 1 < count && !is_lost(index + 1)) {
        n = opus_decode_float(st, packets[index + 1], lens[index + 1], pcm, duration, 1);
        opus_decoder_ctl(st, OPUS_GET_FINAL_RANGE(&rng));
      } else {
        n = opus_decode_float(st, NULL, 0, pcm, duration, 0);
      }
    } else {
      n = opus_decode_float(st, packets[index], lens[index], pcm, 5760, 0);
      opus_decoder_ctl(st, OPUS_GET_FINAL_RANGE(&rng));
    }
    if (n < 0) {
      fprintf(stderr, "%s: record %d: %s\n", path, index, opus_strerror(n));
      exit(1);
    }
    for (i = 0; i < n; i++) {
      opus_uint32 u;
      memcpy(&u, &pcm[i], 4);
      for (b = 0; b < 4; b++) {
        h ^= (u >> (8 * b)) & 0xff;
        h *= 0x100000001b3ull;
      }
    }
    printf("%s %s %d %d %d %u %016llx\n", kind, name, index, lost, n, (unsigned)rng, h);
  }
  opus_decoder_destroy(st);
}

static void plc_reset(int fs_kHz, int nb_subfr) {
  silk_decoder_state s;
  char case_name[64];
  silk_init_decoder(&s);
  s.nb_subfr = nb_subfr;
  silk_decoder_set_fs(&s, fs_kHz, 48000);
  snprintf(case_name, sizeof(case_name), "reset/%dkHz-%dms", fs_kHz, nb_subfr * 5);
  conceal(case_name, &s);
}

int main(int argc, char **argv) {
  int i;
  if (argc >= 2 && strcmp(argv[1], "units") == 0) {
    printf("# tools/libopus-ref/silk_ref units (%s): see silk_ref.c for the format\n", opus_get_version_string());
    nlsf2a_vectors();
    nlsfdec_vectors();
    stabilize_vectors();
    invgain_vectors();
    gains_vectors();
    pitch_vectors();
    shell_vectors();
    pulses_vectors();
    ms2lr_vectors();
    resample_vectors();
    return 0;
  }
  if (argc >= 2 && strcmp(argv[1], "plc") == 0) {
    printf("# tools/libopus-ref/silk_ref plc (%s): see silk_ref.c for the format\n", opus_get_version_string());
    plc_reset(16, 4);
    plc_reset(12, 4);
    plc_reset(8, 2);
    for (i = 2; i < argc; i++) {
      char *eq = strchr(argv[i], '=');
      if (eq == NULL) {
        fprintf(stderr, "usage: silk_ref plc name=packets.bit ...\n");
        return 2;
      }
      *eq = 0;
      plc_stream(argv[i], eq + 1);
    }
    return 0;
  }
  if (argc >= 2 && (strcmp(argv[1], "loss") == 0 || strcmp(argv[1], "fec") == 0)) {
    printf("# tools/libopus-ref/silk_ref %s (%s): see silk_ref.c for the format\n", argv[1], opus_get_version_string());
    for (i = 2; i < argc; i++) {
      char *eq = strchr(argv[i], '=');
      if (eq == NULL) {
        fprintf(stderr, "usage: silk_ref %s name=packets.bit ...\n", argv[1]);
        return 2;
      }
      *eq = 0;
      loss_stream(argv[1], argv[i], eq + 1, strcmp(argv[1], "fec") == 0);
    }
    return 0;
  }
  fprintf(stderr, "usage: silk_ref units | plc|loss|fec name=packets.bit ...\n");
  return 2;
}
