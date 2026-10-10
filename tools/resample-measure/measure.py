#!/usr/bin/env python3
"""Black-box measurements of FFmpeg's default 48-to-16 kHz float resampling.

Runs an ffmpeg binary on crafted mono f32le streams and prints what
internal/resample/DESIGN.md reports: the impulse response, the windowed-sinc
design that reproduces it, the virtual samples at both stream ends, the
output count as a function of the input length, and the summation order.

    python3 -I measure.py /path/to/ffmpeg [--alt /path/to/other/ffmpeg]

Needs numpy. Only the binary's behaviour is used.
"""

import argparse
import os
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor

import numpy as np

ARGS = ["-nostdin", "-hide_banner", "-loglevel", "error", "-y",
        "-f", "f32le", "-ar", "48000", "-ac", "1", "-i", "IN",
        "-ar", "16000", "-f", "f32le", "OUT"]


def run(ffmpeg, x, work, extra=()):
    x = np.asarray(x, dtype="<f4")
    fd, inp = tempfile.mkstemp(suffix=".f32le", dir=work)
    os.close(fd)
    out = inp[:-6] + ".out.f32le"
    x.tofile(inp)
    args = [inp if a == "IN" else out if a == "OUT" else a for a in ARGS]
    args[-5:-5] = extra  # before the output options
    subprocess.run([ffmpeg, *args], check=True)
    y = np.fromfile(out, dtype="<f4")
    os.remove(inp)
    os.remove(out)
    return y


def pmap(f, xs):
    with ThreadPoolExecutor(16) as ex:
        return list(ex.map(f, xs))


def design(width, fc, beta, d):
    """Kaiser-windowed sinc at distances d, scaled to unit sum."""
    x = np.clip(d / width, -1, 1)
    h = np.sinc(fc * d) * np.i0(beta * np.sqrt(1 - x * x))
    return h / h.sum()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("ffmpeg")
    ap.add_argument("--alt", help="second binary to compare byte for byte")
    a = ap.parse_args()
    work = tempfile.mkdtemp()
    ff = lambda x: run(a.ffmpeg, x, work)

    # 1. Unit impulses at every position of a 600-sample stream.
    N = 600

    def imp(p):
        x = np.zeros(N, np.float32)
        x[p] = 1
        return ff(x)
    Y = np.array(pmap(imp, range(N)), dtype=np.float64)
    M = Y.shape[1]
    H, clash = {}, 0
    for p in range(150, 450):
        for m in range(M):
            d = 3 * m - p
            if Y[p, m] != 0 or abs(d) <= 60:
                if d in H and H[d] != Y[p, m]:
                    clash += 1
                H.setdefault(d, Y[p, m])
    nz = sorted(d for d in H if H[d] != 0)
    print(f"impulse response: nonzero for d = 3m-p in [{nz[0]}, {nz[-1]}] "
          f"({len(nz)} taps), shift-variant values: {clash}")
    d = np.arange(-50, 50)
    h = np.array([H[k] for k in d])
    sym = max(abs(H[k] - H[-k]) for k in range(1, 50))
    print(f"  h(d) - h(-d) max for |d| <= 49: {sym:g}; h(-50) = {H[-50]:.6g}, "
          f"h(50) = {H.get(50, 0):g}; h(0) = {H[0]:.8f}; sum = {h.sum():.9f}")

    # 2. Fit the design: vary one parameter at a time around the best fit.
    best = (50, 0.97, 9)
    trials = [(w, 0.97, 9) for w in (48, 49, 49.5, 50.5, 51)]
    trials += [(50, c, 9) for c in (0.95, 0.96, 0.965, 0.975, 0.98)]
    trials += [(50, 0.97, b) for b in (8, 8.5, 8.9, 9.1, 9.5, 10)]
    for width, cut, beta in [best] + trials:
        g = design(width, cut / 3, beta, d)
        err = np.abs(g - h).max()
        exact = int((g.astype(np.float32) == h.astype(np.float32)).sum())
        print(f"  half-width {width}, cutoff {cut}/3, beta {beta}: max error "
              f"{err:.3g}, float32 taps equal to the measured ones: {exact}/100")

    def Hf(t):
        t = np.asarray(t)
        o = np.zeros(t.shape)
        ok = (t >= -50) & (t <= 49)
        o[ok] = h[t[ok] + 50]
        return o
    m = np.arange(M)

    # 3. Stream start: a sample p also appears at virtual position c - p?
    worst = {}
    for c in (-1, 0, 1):
        worst[c] = max(np.abs(Y[p] - Hf(3 * m - p) - Hf(3 * m - (c - p))).max()
                       for p in range(1, 50))
    print("start: residual if sample p reappears at c-p:",
          {c: f"{v:.2g}" for c, v in worst.items()})
    # 4. Stream end: a sample p also appears at virtual position c - p?
    worst = {}
    for c in range(2 * N - 3, 2 * N + 2):
        worst[c - 2 * N] = max(np.abs(Y[p] - Hf(3 * m - p) - Hf(3 * m + p) -
                                      Hf(3 * m - (c - p))).max() for p in range(N - 50, N))
    print("end: residual if sample p reappears at 2N+k-p, by k:",
          {k: f"{v:.2g}" for k, v in worst.items()})

    # 5. Output count against input length.
    lengths = list(range(0, 3000)) + list(range(4090, 4300)) + [8191, 8192, 8193, 48000, 48001, 48002]
    counts = dict(zip(lengths, pmap(lambda n: len(ff(np.full(n, 0.5, np.float32))), lengths)))

    def formula(n):
        return 0 if n < 67 else n // 2 - 16 if n <= 100 else (n + 1) // 3
    bad = [n for n in lengths if counts[n] != formula(n)]
    print(f"counts: {len(lengths)} lengths, mismatches with the formula: {bad[:10]}")
    print("  short streams:", {n: counts[n] for n in (66, 67, 68, 99, 100, 101, 102, 103, 104)})

    # 6. Mirror model against random signals, all lengths 60..400 and more.
    def model(x, count):
        # Output k reads inputs 3k-49 .. 3k+50; xe[i] is input i-50.
        n = len(x)
        idx = np.arange(-50, n + 60)
        j = np.where(idx < 0, -idx, np.where(idx >= n, 2 * n - 1 - idx, idx))
        xe = x.astype(np.float64)[j]
        return np.array([np.dot(h[::-1], xe[3 * k + 1: 3 * k + 101]) for k in range(count)])

    def check(n):
        rng = np.random.default_rng(n)
        x = rng.uniform(-1, 1, n).astype(np.float32)
        y = ff(x)
        if len(y) != formula(n):
            return n, None
        if len(y) == 0:
            return n, 0.0
        return n, float(np.abs(y - model(x, len(y))).max())
    res = pmap(check, list(range(60, 400)) + [1000, 4095, 4096, 4097, 10000, 48001])
    print("mirror model on random signals: count mismatches", [n for n, e in res if e is None],
          f"max abs error {max(e for _, e in res if e is not None):.3g}")

    # 7. Summation order: 8 lanes, fused multiply-add, lanes folded by halves.
    rng = np.random.default_rng(7)
    x = rng.uniform(-1, 1, 3000).astype(np.float32)
    y = ff(x)
    idx = np.arange(-49, 3 * (len(y) - 1) + 51)
    j = np.where(idx < 0, -idx, np.where(idx >= len(x), 2 * len(x) - 1 - idx, idx))
    xe = x[j].astype(np.float64)
    taps = h[::-1].astype(np.float32).astype(np.float64)  # weight of input 3m-49+i
    W = np.stack([xe[3 * k: 3 * k + 100] for k in range(len(y))])
    P = W * taps  # exact products of float32 values
    lanes = np.zeros((len(y), 8))
    for i in range(0, 100, 8):
        blk = P[:, i:i + 8]
        lanes[:, :blk.shape[1]] = (lanes[:, :blk.shape[1]] + blk).astype(np.float32)
    v = lanes.astype(np.float32)
    while v.shape[1] > 1:
        half = v.shape[1] // 2
        v = (v[:, :half] + v[:, half:]).astype(np.float32)
    print(f"8-lane fused multiply-add, folded by halves: {(v[:, 0] != y).sum()} of {len(y)} "
          f"outputs differ from the binary")

    # 8. Filter length against filter_size.
    spans = {}
    for fs in (8, 16, 24, 31, 32, 33, 40, 64):
        ds = set()
        for p in (300, 301, 302):
            x = np.zeros(N, np.float32)
            x[p] = 1
            y = run(a.ffmpeg, x, work, ("-af", f"aresample=filter_size={fs}"))
            ds.update(3 * k - p for k in np.nonzero(y)[0])
        spans[fs] = (min(ds), max(ds), len(ds), 2 * int(np.ceil(fs * 3 / 0.97 / 2)))
    print("filter_size: (first d, last d, taps, smallest even >= F*3/0.97):", spans)

    if a.alt:
        diff = 0
        for n in (67, 100, 1000, 5000, 48001):
            x = np.random.default_rng(n).uniform(-1, 1, n).astype(np.float32)
            diff += int(not np.array_equal(ff(x), run(a.alt, x, work)))
        x = np.zeros(N, np.float32)
        x[300] = 1
        diff += int(not np.array_equal(Y[300], run(a.alt, x, work)))
        print(f"{a.alt}: streams with different bytes: {diff}")


if __name__ == "__main__":
    sys.exit(main())
