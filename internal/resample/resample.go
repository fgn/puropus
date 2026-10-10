// Package resample converts a 48 kHz float32 stream to 16 kHz.
//
// The converter is a 100-tap Kaiser-windowed sinc low-pass FIR evaluated at
// every third input sample (see filter.go for the design). Output sample m
// is centred on input sample 3m: it is the dot product of the taps with
// inputs 3m-49 .. 3m+50.
//
// Windows that reach past either end of the stream read virtual samples:
//
//   - before the start, the stream is mirrored about its first sample,
//     x[-k] = x[k];
//   - after the end, it is mirrored about the point half a sample past its
//     last sample, x[N+j] = x[N-1-j].
//
// A stream of N input samples yields
//
//	0             for N < 67,
//	N/2 - 16      for 67 <= N <= 100 (integer division),
//	(N+1)/3       for N >= 101 (N/3 rounded to nearest),
//
// output samples in total. Output does not depend on how the input is split
// across Process calls.
package resample

import "slices"

// Output sample m reads inputs 3m-preTaps .. 3m+postTaps.
const (
	preTaps  = 49
	postTaps = 50
	// minStream is the shortest stream that produces any output.
	minStream = 67
)

// Resampler48To16 converts one channel from 48 kHz to 16 kHz. The zero value
// is ready to use.
type Resampler48To16 struct {
	// buf holds the input the next output's window starts at, through the
	// newest sample. Until the stream is primed it holds the stream from its
	// first sample; priming prepends the mirrored samples before it.
	buf []float32

	in      int64 // input samples received in this stream
	emitted int64 // output samples returned in this stream
	primed  bool  // buf starts with the mirrored stream start

	flushing bool  // Flush has started draining the stream end
	total    int64 // output count of the stream once flushing
}

// outputCount returns the number of output samples a stream of n input
// samples produces in total.
func outputCount(n int64) int64 {
	switch {
	case n < minStream:
		return 0
	case n <= 100:
		return n/2 - 16
	default:
		return (n + 1) / 3
	}
}

// Process consumes in and writes the output samples that have become
// available to out, returning how many it wrote. It writes at most len(out)
// samples; outputs that do not fit stay pending for the next call. An out of
// len(in)/3 + 256 samples always takes everything available.
//
// Calling Process after Flush starts a new stream; outputs of a Flush that was
// not called until it returned 0 are discarded.
func (r *Resampler48To16) Process(in, out []float32) int {
	if r.flushing {
		r.reset()
	}
	r.in += int64(len(in))
	r.buf = append(r.buf, in...)
	if !r.primed {
		if r.in < minStream {
			return 0
		}
		r.prime()
	}
	// Every output whose window lies inside the received input is final.
	n := 0
	if len(r.buf) >= nTaps {
		n = min((len(r.buf)-nTaps)/3+1, len(out))
	}
	return r.emit(out[:n])
}

// Flush drains the end of the stream into out and returns the number of
// samples written. Call it with a non-empty out until it returns 0; the
// resampler is then back in its zero state, ready for a new stream. Flush on a
// resampler without input returns 0.
func (r *Resampler48To16) Flush(out []float32) int {
	if !r.flushing {
		total := outputCount(r.in)
		if r.emitted >= total {
			r.reset()
			return 0
		}
		if !r.primed {
			r.prime()
		}
		// Mirror the stream end: x[N+j] = x[N-1-j]. No window of the
		// remaining outputs reaches further than j = postTaps-1, and buf
		// always holds at least postTaps real samples here.
		last := len(r.buf) - 1
		for j := range postTaps {
			r.buf = append(r.buf, r.buf[last-j])
		}
		r.flushing = true
		r.total = total
	}
	n := r.emit(out[:min(int64(len(out)), r.total-r.emitted)])
	if r.emitted == r.total {
		r.reset()
	}
	return n
}

// emit computes len(out) outputs from buf and drops the input no later
// output needs.
func (r *Resampler48To16) emit(out []float32) int {
	n := len(out)
	if n == 0 {
		return 0
	}
	filterBlock(out, r.buf[:3*(n-1)+nTaps])
	used := 3 * n
	r.buf = r.buf[:copy(r.buf, r.buf[used:])]
	r.emitted += int64(n)
	return n
}

// prime prepends the mirror image of the stream start, x[-k] = x[k] for
// k = 1..preTaps, so that buf starts at the window of output 0. The stream
// holds at least minStream samples.
func (r *Resampler48To16) prime() {
	n := len(r.buf)
	r.buf = slices.Grow(r.buf, preTaps)[:n+preTaps]
	copy(r.buf[preTaps:], r.buf[:n])
	for k := 1; k <= preTaps; k++ {
		r.buf[preTaps-k] = r.buf[preTaps+k]
	}
	r.primed = true
}

// reset returns to the zero state, keeping the buffer's storage.
func (r *Resampler48To16) reset() {
	*r = Resampler48To16{buf: r.buf[:0]}
}
