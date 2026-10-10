package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
)

// errOutputIsInput reports an output path that names the input file.
var errOutputIsInput = errors.New("the output file is the input file")

// outputFile is a command's output, written to a temporary file beside the
// destination and renamed over it only when the command succeeds, so that a
// failed run leaves the destination as it was, and the input is never
// truncated before it is read.
type outputFile struct {
	*os.File
	target string // the destination, symbolic links resolved
	done   bool
}

// createOutput prepares to write path. It refuses a path that names the
// input file, whatever the route: the same path, a hard link, or a symbolic
// link to it. in is the open input, or nil when the input has no file to
// compare (a pipe, say).
func createOutput(path string, in *os.File) (*outputFile, error) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved // write through a link, as os.Create would
	}
	existing, err := os.Stat(target)
	switch {
	case err == nil:
		if in != nil {
			if info, err := in.Stat(); err == nil && os.SameFile(info, existing) {
				return nil, fmt.Errorf("%s: %w", path, errOutputIsInput)
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	dir, base := filepath.Split(target)
	for attempt := 0; ; attempt++ {
		name := filepath.Join(dir, fmt.Sprintf(".%s.%08x.tmp", base, rand.Uint32()))
		// 0666 before the umask, as os.Create.
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			if errors.Is(err, fs.ErrExist) && attempt < 100 {
				continue
			}
			return nil, err
		}
		if existing != nil {
			if err := f.Chmod(existing.Mode().Perm()); err != nil {
				_ = f.Close()
				_ = os.Remove(name)
				return nil, err
			}
		}
		return &outputFile{File: f, target: target}, nil
	}
}

// commit closes the temporary file and renames it over the destination.
func (o *outputFile) commit() error {
	o.done = true
	if err := o.Close(); err != nil {
		_ = os.Remove(o.Name())
		return err
	}
	if err := os.Rename(o.Name(), o.target); err != nil {
		_ = os.Remove(o.Name())
		return err
	}
	return nil
}

// abort discards the temporary file unless commit was called; it is safe to
// defer.
func (o *outputFile) abort() {
	if o.done {
		return
	}
	o.done = true
	_ = o.Close()
	_ = os.Remove(o.Name())
}
