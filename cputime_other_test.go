//go:build !linux

package puropus

import "time"

// processCPUTime is unavailable on this platform; benchmarks skip the metric.
func processCPUTime() time.Duration { return 0 }
