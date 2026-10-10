# resample-measure

Black-box measurements behind `internal/resample`: `measure.py` runs an ffmpeg
binary on crafted mono 48 kHz f32le streams (unit impulses at every position,
constants of thousands of lengths, random signals) and prints the impulse
response, the windowed-sinc parameters that reproduce it, the virtual samples
at both stream ends, the output count against the input length and the float
summation order. `internal/resample/DESIGN.md` quotes its output.

```sh
python3 -I measure.py /path/to/ffmpeg --alt /path/to/another/ffmpeg
```

It needs Python 3 and numpy, and uses only the binaries' output.
