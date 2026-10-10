/* celt_ref: reference vectors for the stages of the Go CELT decoder in
   opus/internal/celt.

   The program calls libopus 1.5.2's CELT decoder functions on seeded
   pseudo-random input and prints their results: decode_pulses (cwrsi),
   alg_unquant, init_caps, clt_compute_allocation, the coarse, fine and final
   energy decoding, quant_all_bands, anti_collapse, denormalise_bands,
   clt_mdct_backward_c for every size and stride the decoder uses,
   comb_filter, deemphasis, and the concealment helpers pitch_downsample,
   pitch_search, _celt_autocorr, _celt_lpc, celt_fir_c and celt_iir. Range
   decoder input is random bytes, or pulse vectors coded with libopus's
   encode_pulses. It is meant for the generic build (make GENERIC=1), whose
   arithmetic is that of the C source the Go code follows: the SIMD build
   computes the comb filter, the inner products and the pitch correlation
   in another order. deemphasis is static in celt_decoder.c; the Makefile
   compiles celt_decoder.c with RESYNTH defined to reach it.

   Usage: celt_ref | gzip -9 -n > opus/internal/celt/testdata/units.txt.gz
   (make -C tools/libopus-ref celt-vectors, which runs celt-vectors.sh)

   Output: one case per line, "<kind> <inputs> = <outputs>". Integers are
   decimal, floats are the 8 hex digits of their IEEE bits, and byte strings
   are hex. The kinds, with [n] marking n values:
     cwrs N K bytes = Ryy rng y[N]
     unquant N K spread B gain bytes = cm rng X[N]
     caps LM C = cap[21]
     alloc start end C LM trim total offsets[21] bytes
       = codedBands intensity dual balance rng tellfrac pulses[21] ebits[21] prio[21]
     coarse start end C LM intra old[42] bytes = rng tell old[42]
     fine start end C fq[21] old[42] bytes = rng old[42]
     final start end C bitsleft fq[21] prio[21] old[42] bytes = rng old[42]
     bands start end C LM trim total spread short seed disableinv tf[21] bytes
       = rng seed cm[C*21] X[C*N]   (X: the coded bands of each channel, N=120<<LM)
     anti C LM start end seed masks[C*21] pulses[21] logE[42] prev1[42] prev2[42] X[C*N]
       = X[C*N]
     denorm LM start end downsample silence logE[21] X[N] = freq[N]
     imdct shift stride in[stride*N2] hist[60] = out[N2+60]   (N2 = 960>>shift)
     comb T0 T1 N g0 g1 tapset0 tapset1 overlap inplace H x[H+N] = y[N]
       (x[H] is the first sample; the window is the mode's when overlap > 0)
     deemph N C downsample coef mem[C] in[C*N] = pcm[C*N/downsample] mem[C]
     plcpitch C x[C*2048] = xlp[1024] pitch   (pitch_downsample, pitch_search)
     autocorr n lag overlap x[n] = ac[lag+1]
     lpc p ac[p+1] = lpc[p]
     fir N ord num[ord] x[ord+N] = y[N]       (x[ord] is the first sample)
     iir N ord den[ord] mem[ord] x[N] = y[N] mem[ord] */

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "bands.h"
#include "celt.h"
#include "celt_lpc.h"
#include "cwrs.h"
#include "entdec.h"
#include "entenc.h"
#include "mdct.h"
#include "modes.h"
#include "pitch.h"
#include "quant_bands.h"
#include "rate.h"
#include "vq.h"

/* deemphasis from celt_decoder.c, built with RESYNTH. */
void deemphasis(celt_sig *in[], opus_val16 *pcm, int N, int C, int downsample, const opus_val16 *coef,
                celt_sig *mem, int accum);

/* xorshift32 with a fixed seed, so the vectors never change. */
static opus_uint32 seed = 0x6A09E667u;

static opus_uint32 rnd(void) {
  seed ^= seed << 13;
  seed ^= seed >> 17;
  seed ^= seed << 5;
  return seed;
}

/* rnd_range returns a value in [lo, hi]. */
static int rnd_range(int lo, int hi) {
  return lo + (int)(((unsigned long long)rnd() * (unsigned long long)(hi - lo + 1)) >> 32);
}

/* rnd_float returns a float in [-1, 1). */
static float rnd_float(void) { return (float)(opus_int32)rnd() * (1.f / 2147483648.f); }

static const CELTMode *mode;

static void put_int(int v) { printf(" %d", v); }
static void put_ints(const int *v, int n) {
  int i;
  for (i = 0; i < n; i++) printf(" %d", v[i]);
}
static void put_float(float f) {
  opus_uint32 u;
  memcpy(&u, &f, 4);
  printf(" %08x", u);
}
static void put_floats(const float *v, int n) {
  int i;
  for (i = 0; i < n; i++) put_float(v[i]);
}
static void put_bytes(const unsigned char *b, int n) {
  int i;
  printf(" ");
  for (i = 0; i < n; i++) printf("%02x", b[i]);
}
static void rnd_bytes(unsigned char *b, int n) {
  int i;
  for (i = 0; i < n; i++) b[i] = (unsigned char)rnd();
}

/* get_pulses from rate.h. */
static int pulses_of(int q) { return q < 8 ? q : (8 + (q & 7)) << ((q >> 3) - 1); }

/* cwrs_case decodes the pulses in buf, of size n bytes. */
static void cwrs_case(int N, int K, unsigned char *buf, int n) {
  ec_dec dec;
  int y[176];
  opus_val32 ryy;
  ec_dec_init(&dec, buf, n);
  ryy = decode_pulses(y, N, K, &dec);
  printf("cwrs %d %d", N, K);
  put_bytes(buf, n);
  printf(" =");
  put_float(ryy);
  printf(" %u", dec.rng);
  put_ints(y, N);
  printf("\n");
}

static void cwrs_cases(void) {
  /* Every band size and pulse count the pulse cache allows. */
  int lm, j, q;
  for (lm = 0; lm <= 4; lm++) {
    for (j = 0; j < mode->nbEBands; j++) {
      const unsigned char *cache;
      int N = (mode->eBands[j + 1] - mode->eBands[j]) << lm >> 1;
      if (N < 2 || mode->cache.index[lm * mode->nbEBands + j] < 0) continue;
      /* Skip sizes seen in an earlier band of the same row. */
      if (j > 0 && mode->cache.index[lm * mode->nbEBands + j] == mode->cache.index[lm * mode->nbEBands + j - 1])
        continue;
      cache = mode->cache.bits + mode->cache.index[lm * mode->nbEBands + j];
      for (q = 1; q <= cache[0]; q++) {
        int K = pulses_of(q), t;
        unsigned char buf[16];
        rnd_bytes(buf, sizeof(buf));
        cwrs_case(N, K, buf, sizeof(buf));
        /* Coded vectors: all pulses at one end, or random signed ones. */
        for (t = 0; t < 4; t++) {
          if (t == 2 || (t < 2 && (rnd() & 1))) continue;
          int y[176], i, k;
          ec_enc enc;
          memset(y, 0, sizeof(y));
          if (t == 0)
            y[0] = K;
          else if (t == 1)
            y[N - 1] = -K;
          else
            for (k = 0; k < K; k++) y[rnd_range(0, N - 1)] += 1;
          if (t == 3)
            for (i = 0; i < N; i++)
              if (rnd() & 1) y[i] = -y[i];
          memset(buf, 0, sizeof(buf));
          ec_enc_init(&enc, buf, sizeof(buf));
          encode_pulses(y, N, K, &enc);
          ec_enc_done(&enc);
          cwrs_case(N, K, buf, sizeof(buf));
        }
      }
    }
  }
}

static void unquant_cases(void) {
  int t;
  for (t = 0; t < 400; t++) {
    static const int sizes[] = {2, 3, 4, 6, 8, 9, 11, 12, 16, 18, 22, 24, 32, 36, 44, 48, 64, 72, 88, 96, 144, 176};
    int N = sizes[rnd_range(0, (int)(sizeof(sizes) / sizeof(sizes[0])) - 1)];
    int B = 1 << rnd_range(0, 3), K, spread = rnd_range(0, 3), lm, j, maxq = 0;
    unsigned char buf[48];
    float X[176], gain = rnd_range(0, 3) == 0 ? 1.f : (float)rnd_range(1, 32767) / 32768.f;
    ec_dec dec;
    unsigned cm;
    if (N % B) B = 1;
    /* The largest pulse count the cache allows for this size. */
    for (lm = 0; lm <= 4; lm++)
      for (j = 0; j < mode->nbEBands; j++)
        if (((mode->eBands[j + 1] - mode->eBands[j]) << lm >> 1) == N &&
            mode->cache.index[lm * mode->nbEBands + j] >= 0) {
          const unsigned char *cache = mode->cache.bits + mode->cache.index[lm * mode->nbEBands + j];
          if (cache[0] > maxq) maxq = cache[0];
        }
    if (maxq == 0) continue;
    K = pulses_of(rnd_range(1, maxq));
    rnd_bytes(buf, sizeof(buf));
    ec_dec_init(&dec, buf, sizeof(buf));
    cm = alg_unquant(X, N, K, spread, B, &dec, gain);
    printf("unquant %d %d %d %d", N, K, spread, B);
    put_float(gain);
    put_bytes(buf, sizeof(buf));
    printf(" = %u %u", cm, dec.rng);
    put_floats(X, N);
    printf("\n");
  }
}

static void caps_cases(void) {
  int lm, c;
  for (lm = 0; lm <= 3; lm++)
    for (c = 1; c <= 2; c++) {
      int cap[21];
      init_caps(mode, cap, lm, c);
      printf("caps %d %d =", lm, c);
      put_ints(cap, 21);
      printf("\n");
    }
}

/* rnd_frame picks a band range, channels, LM and a frame size. */
static void rnd_frame(int *start, int *end, int *C, int *LM, int *nbytes) {
  static const int ends[] = {13, 17, 19, 21};
  *start = rnd_range(0, 3) == 0 ? 17 : 0;
  *end = ends[rnd_range(*start ? 2 : 0, 3)];
  if (rnd_range(0, 7) == 0) *end = rnd_range(*start + 1, 21);
  *C = rnd_range(1, 2);
  *LM = rnd_range(0, 3);
  *nbytes = rnd_range(2, 3) == 2 ? rnd_range(2, 40) : rnd_range(41, 1275);
}

static void alloc_cases(void) {
  int t;
  for (t = 0; t < 1000; t++) {
    int start, end, C, LM, n, trim, total, i;
    int offsets[21], cap[21], pulses[21], ebits[21], prio[21];
    int intensity = 0, dual = 0, coded;
    opus_int32 balance = 0;
    unsigned char buf[1275];
    ec_dec dec;
    rnd_frame(&start, &end, &C, &LM, &n);
    trim = rnd_range(0, 10);
    total = rnd_range(0, n * 64);
    n = 8;
    memset(offsets, 0, sizeof(offsets));
    for (i = start; i < end; i++)
      if (rnd_range(0, 5) == 0) offsets[i] = rnd_range(1, 4) * C * ((mode->eBands[i + 1] - mode->eBands[i]) << LM) * 8;
    memset(pulses, 0, sizeof(pulses));
    memset(ebits, 0, sizeof(ebits));
    memset(prio, 0, sizeof(prio));
    rnd_bytes(buf, n);
    init_caps(mode, cap, LM, C);
    ec_dec_init(&dec, buf, n);
    coded = clt_compute_allocation(mode, start, end, offsets, cap, trim, &intensity, &dual, total, &balance, pulses,
                                   ebits, prio, C, LM, &dec, 0, 0, 0);
    printf("alloc %d %d %d %d %d %d", start, end, C, LM, trim, total);
    put_ints(offsets, 21);
    put_bytes(buf, n);
    printf(" = %d %d %d %d %u %u", coded, intensity, dual, balance, dec.rng, ec_tell_frac(&dec));
    put_ints(pulses, 21);
    put_ints(ebits, 21);
    put_ints(prio, 21);
    printf("\n");
  }
}

static void energy_cases(void) {
  int t, i;
  for (t = 0; t < 300; t++) {
    int start, end, C, LM, n, intra = rnd_range(0, 1);
    float old[42], before[42];
    unsigned char buf[1275];
    ec_dec dec;
    rnd_frame(&start, &end, &C, &LM, &n);
    n = rnd_range(1, 80);
    for (i = 0; i < 42; i++) old[i] = rnd_range(0, 9) == 0 ? -28.f : 12.f * rnd_float();
    memcpy(before, old, sizeof(old));
    rnd_bytes(buf, n);
    ec_dec_init(&dec, buf, n);
    unquant_coarse_energy(mode, start, end, old, intra, &dec, C, LM);
    printf("coarse %d %d %d %d %d", start, end, C, LM, intra);
    put_floats(before, 42);
    put_bytes(buf, n);
    printf(" = %u %d", dec.rng, ec_tell(&dec));
    put_floats(old, 42);
    printf("\n");
  }
  for (t = 0; t < 150; t++) {
    int start, end, C, LM, n, fq[21];
    float old[42], before[42];
    unsigned char buf[1275];
    ec_dec dec;
    rnd_frame(&start, &end, &C, &LM, &n);
    n = rnd_range(1, 40);
    for (i = 0; i < 21; i++) fq[i] = rnd_range(0, 3) == 0 ? 0 : rnd_range(1, 8);
    for (i = 0; i < 42; i++) old[i] = 12.f * rnd_float();
    memcpy(before, old, sizeof(old));
    rnd_bytes(buf, n);
    ec_dec_init(&dec, buf, n);
    unquant_fine_energy(mode, start, end, old, fq, &dec, C);
    printf("fine %d %d %d", start, end, C);
    put_ints(fq, 21);
    put_floats(before, 42);
    put_bytes(buf, n);
    printf(" = %u", dec.rng);
    put_floats(old, 42);
    printf("\n");
  }
  for (t = 0; t < 150; t++) {
    int start, end, C, LM, n, fq[21], prio[21], bitsleft;
    float old[42], before[42];
    unsigned char buf[1275];
    ec_dec dec;
    rnd_frame(&start, &end, &C, &LM, &n);
    n = rnd_range(1, 12);
    for (i = 0; i < 21; i++) {
      fq[i] = rnd_range(0, 8);
      prio[i] = rnd_range(0, 1);
    }
    for (i = 0; i < 42; i++) old[i] = 12.f * rnd_float();
    memcpy(before, old, sizeof(old));
    bitsleft = rnd_range(0, 60);
    rnd_bytes(buf, n);
    ec_dec_init(&dec, buf, n);
    unquant_energy_finalise(mode, start, end, old, fq, prio, bitsleft, &dec, C);
    printf("final %d %d %d %d", start, end, C, bitsleft);
    put_ints(fq, 21);
    put_ints(prio, 21);
    put_floats(before, 42);
    put_bytes(buf, n);
    printf(" = %u", dec.rng);
    put_floats(old, 42);
    printf("\n");
  }
}

static void bands_cases(void) {
  int t, i, c;
  for (t = 0; t < 240; t++) {
    int start, end, C, LM, n, trim, total, spread, shortBlocks, disable_inv, M, N;
    int offsets[21], cap[21], pulses[21], ebits[21], prio[21], tf[21];
    int intensity = 0, dual = 0, coded;
    opus_int32 balance = 0;
    opus_uint32 bseed, seed_in;
    unsigned char buf[1275], masks[42];
    static float X[2 * 960];
    ec_dec dec;
    rnd_frame(&start, &end, &C, &LM, &n);
    M = 1 << LM;
    N = M * mode->shortMdctSize;
    trim = rnd_range(0, 10);
    spread = rnd_range(0, 3);
    shortBlocks = LM > 0 && rnd_range(0, 2) == 0;
    disable_inv = rnd_range(0, 3) == 0;
    seed_in = bseed = rnd();
    for (i = 0; i < 21; i++) tf[i] = tf_select_table[LM][4 * shortBlocks + rnd_range(0, 3)];
    memset(offsets, 0, sizeof(offsets));
    memset(masks, 0, sizeof(masks));
    for (i = 0; i < 2 * 960; i++) X[i] = 0;
    rnd_bytes(buf, n);
    init_caps(mode, cap, LM, C);
    ec_dec_init(&dec, buf, n);
    total = (n * 8 << BITRES) - ec_tell_frac(&dec) - 1;
    coded = clt_compute_allocation(mode, start, end, offsets, cap, trim, &intensity, &dual, total, &balance, pulses,
                                   ebits, prio, C, LM, &dec, 0, 0, 0);
    quant_all_bands(0, mode, start, end, X, C == 2 ? X + N : NULL, masks, NULL, pulses, shortBlocks ? M : 0, spread,
                    dual, intensity, tf, n * (8 << BITRES), balance, &dec, LM, coded, &bseed, 0, 0, disable_inv);
    (void)coded;
    printf("bands %d %d %d %d %d %d %d %d %u %d", start, end, C, LM, trim, total, spread, shortBlocks, seed_in,
           disable_inv);
    put_ints(tf, 21);
    put_bytes(buf, n);
    printf(" = %u %u", dec.rng, bseed);
    for (i = 0; i < C * 21; i++) printf(" %d", masks[i]);
    for (c = 0; c < C; c++) put_floats(X + c * N + M * mode->eBands[start], M * (mode->eBands[end] - mode->eBands[start]));
    printf("\n");
  }
}

static void anti_cases(void) {
  int t, i;
  for (t = 0; t < 40; t++) {
    int C = rnd_range(1, 2), LM = rnd_range(0, 3), start = rnd_range(0, 3) == 0 ? 17 : 0, end = rnd_range(start + 1, 21);
    int N = mode->shortMdctSize << LM, pulses[21];
    opus_uint32 s = rnd();
    unsigned char masks[42];
    float logE[42], p1[42], p2[42];
    static float X[2 * 960];
    for (i = 0; i < 42; i++) {
      masks[i] = (unsigned char)rnd();
      logE[i] = 10.f * rnd_float();
      p1[i] = 10.f * rnd_float();
      p2[i] = 10.f * rnd_float();
    }
    for (i = 0; i < 21; i++) pulses[i] = rnd_range(0, 400);
    for (i = 0; i < C * N; i++) X[i] = rnd_float();
    printf("anti %d %d %d %d %u", C, LM, start, end, s);
    for (i = 0; i < C * 21; i++) printf(" %d", masks[i]);
    put_ints(pulses, 21);
    put_floats(logE, 42);
    put_floats(p1, 42);
    put_floats(p2, 42);
    put_floats(X, C * N);
    anti_collapse(mode, X, masks, LM, C, N, start, end, logE, p1, p2, pulses, s, 0);
    printf(" =");
    put_floats(X, C * N);
    printf("\n");
  }
}

static void denorm_cases(void) {
  int t, i;
  for (t = 0; t < 40; t++) {
    int LM = rnd_range(0, 3), start = rnd_range(0, 3) == 0 ? 17 : 0, end = rnd_range(start + 1, 21);
    int ds = rnd_range(0, 3) == 0 ? rnd_range(2, 6) : 1, silence = rnd_range(0, 7) == 0;
    int M = 1 << LM, N = M * mode->shortMdctSize;
    float logE[21], X[960], freq[960];
    if (ds == 5) ds = 6;
    for (i = 0; i < 21; i++) logE[i] = rnd_range(0, 9) == 0 ? 30.f + 4.f * rnd_float() : 12.f * rnd_float();
    for (i = 0; i < N; i++) X[i] = rnd_float();
    for (i = 0; i < N; i++) freq[i] = 123.f;
    denormalise_bands(mode, X, freq, logE, start, end, M, ds, silence);
    printf("denorm %d %d %d %d %d", LM, start, end, ds, silence);
    put_floats(logE, 21);
    put_floats(X, N);
    printf(" =");
    put_floats(freq, N);
    printf("\n");
  }
}

static void imdct_cases(void) {
  int shift, t, i;
  for (shift = 0; shift <= 3; shift++) {
    int strides[4] = {1, 2, 4, 8}, ns = shift == 3 ? 4 : 1, si;
    for (si = 0; si < ns; si++)
      for (t = 0; t < 2; t++) {
        int stride = strides[si], N2 = 960 >> shift;
        static float in[960], out[960 + 120];
        float hist[60];
        for (i = 0; i < stride * N2; i++) in[i] = 3000.f * rnd_float();
        for (i = 0; i < 60; i++) hist[i] = 3000.f * rnd_float();
        for (i = 0; i < N2 + 120; i++) out[i] = 0;
        memcpy(out, hist, sizeof(hist));
        clt_mdct_backward_c(&mode->mdct, in, out, mode->window, mode->overlap, shift, stride, 0);
        printf("imdct %d %d", shift, stride);
        put_floats(in, stride * N2);
        put_floats(hist, 60);
        printf(" =");
        put_floats(out, N2 + 60);
        printf("\n");
      }
  }
}

static void comb_cases(void) {
  int t, i;
  for (t = 0; t < 60; t++) {
    int T0 = rnd_range(0, 3) == 0 ? 0 : rnd_range(15, 1022), T1 = rnd_range(0, 3) == 0 ? 0 : rnd_range(15, 1022);
    int N = rnd_range(0, 1) ? 120 : 120 << rnd_range(0, 3), tap0 = rnd_range(0, 2), tap1 = rnd_range(0, 2);
    int overlap = rnd_range(0, 1) ? 120 : 0, inplace = rnd_range(0, 1), H;
    float g0 = rnd_range(0, 4) == 0 ? 0 : 0.09375f * rnd_range(1, 8), g1 = rnd_range(0, 4) == 0 ? 0 : 0.09375f * rnd_range(1, 8);
    static float x[1026 + 960], y[960];
    if (rnd_range(0, 4) == 0) {
      T1 = T0;
      g1 = g0;
      tap1 = tap0;
    }
    if (rnd_range(0, 5) == 0) {
      g0 = -g0;
      g1 = -g1;
    }
    H = (T0 > T1 ? T0 : T1);
    if (H < 15) H = 15;
    H += 2;
    for (i = 0; i < H + N; i++) x[i] = 10000.f * rnd_float();
    printf("comb %d %d %d", T0, T1, N);
    put_float(g0);
    put_float(g1);
    printf(" %d %d %d %d %d", tap0, tap1, overlap, inplace, H);
    put_floats(x, H + N);
    if (inplace) {
      comb_filter(x + H, x + H, T0, T1, N, g0, g1, tap0, tap1, overlap ? mode->window : NULL, overlap, 0);
      printf(" =");
      put_floats(x + H, N);
    } else {
      comb_filter(y, x + H, T0, T1, N, g0, g1, tap0, tap1, overlap ? mode->window : NULL, overlap, 0);
      printf(" =");
      put_floats(y, N);
    }
    printf("\n");
  }
}

static void deemph_cases(void) {
  int t, i;
  for (t = 0; t < 20; t++) {
    int C = rnd_range(1, 2), ds = rnd_range(0, 2) == 0 ? rnd_range(2, 6) : 1, N = 120 << rnd_range(0, 3);
    static float in[2][960], pcm[2 * 960];
    float mem[2], coef[4];
    celt_sig *inp[2] = {in[0], in[1]};
    if (ds == 5) ds = 6;
    memcpy(coef, mode->preemph, sizeof(coef));
    for (i = 0; i < 2; i++) mem[i] = 20000.f * rnd_float();
    for (i = 0; i < N; i++) {
      in[0][i] = 20000.f * rnd_float();
      in[1][i] = 20000.f * rnd_float();
    }
    printf("deemph %d %d %d", N, C, ds);
    put_float(coef[0]);
    put_floats(mem, C);
    put_floats(in[0], N);
    if (C == 2) put_floats(in[1], N);
    deemphasis(inp, pcm, N, C, ds, coef, mem, 0);
    printf(" =");
    put_floats(pcm, C * N / ds);
    put_floats(mem, C);
    printf("\n");
  }
}

static void plc_cases(void) {
  int t, i, c;
  for (t = 0; t < 12; t++) {
    int C = rnd_range(1, 2), pitch, period = rnd_range(60, 900);
    static float x[2][2048], xlp[1024];
    celt_sig *xp[2] = {x[0], x[1]};
    float amp = 10000.f * (float)rnd_range(1, 3), noise = (float)rnd_range(0, 2000);
    for (c = 0; c < 2; c++)
      for (i = 0; i < 2048; i++)
        x[c][i] = amp * (float)sin(2 * 3.14159265358979 * i / period + c) +
                  0.3f * amp * (float)sin(4 * 3.14159265358979 * i / period) + noise * rnd_float();
    pitch_downsample(xp, xlp, 2048, C, 0);
    pitch_search(xlp + (720 >> 1), xlp, 2048 - 720, 720 - 100, &pitch, 0);
    printf("plcpitch %d", C);
    put_floats(x[0], 2048);
    if (C == 2) put_floats(x[1], 2048);
    printf(" =");
    put_floats(xlp, 1024);
    printf(" %d\n", pitch);
  }
  for (t = 0; t < 20; t++) {
    int n = rnd_range(0, 1) ? 1024 : rnd_range(64, 1024), lag = rnd_range(0, 1) ? 24 : rnd_range(1, 24);
    int overlap = n >= 240 && rnd_range(0, 1) ? 120 : 0;
    float xv[1024], ac[25];
    for (i = 0; i < n; i++) xv[i] = 8000.f * rnd_float() + 4000.f * (float)sin(i * 0.05 * (t + 1));
    _celt_autocorr(xv, ac, mode->window, overlap, lag, n, 0);
    printf("autocorr %d %d %d", n, lag, overlap);
    put_floats(xv, n);
    printf(" =");
    put_floats(ac, lag + 1);
    printf("\n");
  }
  for (t = 0; t < 40; t++) {
    int p = rnd_range(0, 1) ? 24 : rnd_range(1, 24), n = 512;
    float xv[512], ac[25], lpc[24];
    for (i = 0; i < n; i++) xv[i] = 1000.f * rnd_float() + 3000.f * (float)sin(i * 0.031 * (t + 1));
    _celt_autocorr(xv, ac, NULL, 0, p, n, 0);
    if (t % 10 == 0) ac[0] = rnd_range(0, 1) ? 0 : 1e-11f;
    _celt_lpc(lpc, ac, p);
    printf("lpc %d", p);
    put_floats(ac, p + 1);
    printf(" =");
    put_floats(lpc, p);
    printf("\n");
  }
  for (t = 0; t < 20; t++) {
    int N = rnd_range(0, 1) ? 4 * rnd_range(1, 256) : rnd_range(1, 1024), ord = 24;
    static float xv[24 + 1024], y[1024];
    float num[24];
    for (i = 0; i < ord; i++) num[i] = 0.5f * rnd_float();
    for (i = 0; i < ord + N; i++) xv[i] = 10000.f * rnd_float();
    celt_fir_c(xv + ord, num, y, N, ord, 0);
    printf("fir %d %d", N, ord);
    put_floats(num, ord);
    put_floats(xv, ord + N);
    printf(" =");
    put_floats(y, N);
    printf("\n");
  }
  for (t = 0; t < 20; t++) {
    int N = rnd_range(0, 1) ? 120 * rnd_range(2, 9) : rnd_range(1, 1080), ord = 24;
    static float xv[1080], y[1080];
    float den[24], mem[24], ac[25], sig[512];
    /* A stable filter: the LPC of a random signal. */
    for (i = 0; i < 512; i++) sig[i] = 1000.f * rnd_float() + 3000.f * (float)sin(i * 0.07 * (t + 1));
    _celt_autocorr(sig, ac, NULL, 0, ord, 512, 0);
    ac[0] *= 1.0001f;
    _celt_lpc(den, ac, ord);
    for (i = 0; i < ord; i++) mem[i] = 5000.f * rnd_float();
    for (i = 0; i < N; i++) xv[i] = 3000.f * rnd_float();
    printf("iir %d %d", N, ord);
    put_floats(den, ord);
    put_floats(mem, ord);
    put_floats(xv, N);
    celt_iir(xv, den, y, N, ord, mem, 0);
    printf(" =");
    put_floats(y, N);
    put_floats(mem, ord);
    printf("\n");
  }
}

int main(void) {
  mode = opus_custom_mode_create(48000, 960, NULL);
  printf("# CELT stage vectors from libopus 1.5.2 (git tag v1.5.2), %s, generic C build, written by\n",
         opus_get_version_string());
  printf("# tools/libopus-ref/celt_ref.c (make -C tools/libopus-ref celt-vectors).\n");
  printf("# The format is documented there.\n");
  cwrs_cases();
  unquant_cases();
  caps_cases();
  alloc_cases();
  energy_cases();
  bands_cases();
  anti_cases();
  denorm_cases();
  imdct_cases();
  comb_cases();
  deemph_cases();
  plc_cases();
  return 0;
}
