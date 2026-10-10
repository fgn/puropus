package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fgn/puropus"
)

func TestReadIntervals(t *testing.T) {
	t.Parallel()
	path := writeIntervalsFile(t, "# comment\r\n\r\n  0.25 1.5 # trailing comment\r\n2 3\r\n")

	got, err := readIntervals(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []puropus.Interval{
		{Start: 250 * time.Millisecond, End: 1500 * time.Millisecond},
		{Start: 2 * time.Second, End: 3 * time.Second},
	}
	if len(got) != len(want) {
		t.Fatalf("read %d intervals, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("interval %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadIntervalsRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"1\n", "1 2 3\n", "start 2\n"} {
		t.Run(strings.TrimSpace(text), func(t *testing.T) {
			t.Parallel()
			path := writeIntervalsFile(t, "# heading\n"+text)
			_, err := readIntervals(path)
			if err == nil {
				t.Fatal("readIntervals succeeded")
			}
			if want := path + ":2:"; !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		})
	}
}

func TestReadIntervalsRejectsNonFiniteValues(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"inf 2\n", "1 nan\n"} {
		t.Run(strings.TrimSpace(text), func(t *testing.T) {
			t.Parallel()
			path := writeIntervalsFile(t, "\n"+text)
			_, err := readIntervals(path)
			if err == nil {
				t.Fatal("readIntervals succeeded")
			}
			if want := path + ":2:"; !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
			if !strings.Contains(err.Error(), "finite") {
				t.Errorf("error %q does not explain the finite-value requirement", err)
			}
		})
	}
}

func TestReadIntervalsRejectsNonIncreasingIntervals(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"1 1\n", "2 1\n"} {
		t.Run(strings.TrimSpace(text), func(t *testing.T) {
			t.Parallel()
			path := writeIntervalsFile(t, "# heading\n"+text)
			_, err := readIntervals(path)
			if err == nil {
				t.Fatal("readIntervals succeeded")
			}
			if want := path + ":2:"; !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
			if !strings.Contains(err.Error(), "greater than") {
				t.Errorf("error %q does not explain interval ordering", err)
			}
		})
	}
}

func TestReadIntervalsMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.txt")
	_, err := readIntervals(path)
	if err == nil {
		t.Fatal("readIntervals succeeded")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not contain path %q", err, path)
	}
}

func TestFormatBitrate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		bytes    int
		duration time.Duration
		want     string
	}{
		{name: "empty stream", want: "n/a"},
		{name: "positive duration", bytes: 20_000, duration: 2 * time.Second, want: "80.0 kb/s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := formatBitrate(tc.bytes, tc.duration); got != tc.want {
				t.Errorf("formatBitrate(%d, %v) = %q, want %q", tc.bytes, tc.duration, got, tc.want)
			}
		})
	}
}

func TestFormatPacketDurationsSortsAscending(t *testing.T) {
	t.Parallel()
	counts := map[int]int{2880: 2, 480: 4, 960: 3}
	if got, want := formatPacketDurations(counts), " 10ms×4 20ms×3 60ms×2"; got != want {
		t.Errorf("formatPacketDurations() = %q, want %q", got, want)
	}
}

func writeIntervalsFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "intervals.txt")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fixtureCopy copies a test fixture into a new directory and returns its
// path. The tests that need one skip without the test data, which the module
// zip leaves out.
func fixtureCopy(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("testdata is missing (the module zip leaves it out; test from a clone)")
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// dirNames lists the names in a directory.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// decode and cut refuse an output that is their input, whether it is named
// by the same path, a hard link or a symbolic link, and leave the input as
// it was. Opening the input first does not protect it: creating the output
// used to truncate the file before a byte of it was read.
func TestOutputThatIsTheInputIsRefused(t *testing.T) {
	input := fixtureCopy(t, "webm-celt-60ms.webm")
	want, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(input)
	keep := writeIntervalsFile(t, "0 1\n")
	hard := filepath.Join(dir, "hard.webm")
	if err := os.Link(input, hard); err != nil {
		t.Logf("no hard link: %v", err)
		hard = ""
	}
	soft := filepath.Join(dir, "soft.webm")
	if err := os.Symlink(input, soft); err != nil {
		t.Logf("no symbolic link: %v", err)
		soft = ""
	}
	for _, output := range []string{input, hard, soft} {
		if output == "" {
			continue
		}
		for name, run := range map[string]func() error{
			"decode": func() error { return decode([]string{"-o", output, input}) },
			"cut":    func() error { return cut([]string{"-keep", keep, "-o", output, input}) },
		} {
			if err := run(); !errors.Is(err, errOutputIsInput) {
				t.Fatalf("%s -o %s: %v, want errOutputIsInput", name, filepath.Base(output), err)
			}
			got, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s -o %s changed the input: %d bytes, was %d", name, filepath.Base(output), len(got), len(want))
			}
		}
	}
	if names := dirNames(t, dir); len(names) != 1+len(slices.DeleteFunc([]string{hard, soft}, func(s string) bool { return s == "" })) {
		t.Fatalf("files left behind: %v", names)
	}
}

// A run that fails leaves an existing output untouched and no temporary
// file behind; a run that succeeds replaces the output, keeping its
// permissions, and writes through a symbolic link as os.Create would.
func TestOutputIsReplacedOnlyOnSuccess(t *testing.T) {
	input := fixtureCopy(t, "ogg-celt-fb.opus")
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	truncated := filepath.Join(dir, "truncated.opus")
	if err := os.WriteFile(truncated, data[:len(data)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "out.f32le")
	if err := os.WriteFile(output, []byte("previous"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := decode([]string{"-o", output, truncated}); !errors.Is(err, puropus.ErrTruncated) {
		t.Fatalf("decoding a truncated file: %v", err)
	}
	if got, _ := os.ReadFile(output); string(got) != "previous" {
		t.Fatalf("a failed decode left %d bytes in the output", len(got))
	}
	keep := writeIntervalsFile(t, "10 11\n") // past the end: an empty cut fails
	if err := cut([]string{"-keep", keep, "-o", output, input}); !errors.Is(err, puropus.ErrEmptyStream) {
		t.Fatalf("an empty cut: %v", err)
	}
	if got, _ := os.ReadFile(output); string(got) != "previous" {
		t.Fatalf("a failed cut left %d bytes in the output", len(got))
	}
	if names := dirNames(t, dir); len(names) != 2 {
		t.Fatalf("files left behind: %v", names)
	}

	link := filepath.Join(dir, "link.f32le")
	if err := os.Symlink(output, link); err != nil {
		t.Skipf("no symbolic link: %v", err)
	}
	if err := decode([]string{"-o", link, input}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 144000*4 {
		t.Fatalf("the output has %d bytes, want 144000 samples", len(got))
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("the symbolic link was replaced: %v", err)
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("output mode %v, want 0640 kept (%v)", info.Mode().Perm(), err)
	}
}
