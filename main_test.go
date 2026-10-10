package puropus

import (
	"fmt"
	"os"
	"testing"
)

// TestMain skips the package's tests when the test data is absent. Each data
// directory is a module of its own so that the module zip "go get" downloads
// leaves it out; the tests run from a clone of the repository.
func TestMain(m *testing.M) {
	for _, dir := range []string{"testdata", "opus/testdata"} {
		if _, err := os.Stat(dir); err != nil {
			fmt.Fprintf(os.Stderr, "skipping the tests: %s is missing (the module zip leaves the test data out; test from a clone)\n", dir)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
