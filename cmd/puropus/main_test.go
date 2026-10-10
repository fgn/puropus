package main

import (
	"os"
	"path/filepath"
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
