package gzipstore

import (
	"errors"
	"strings"
	"testing"
)

// headerOnlyWriter accepts the first write (the gzip header) and fails every
// write after it, so the compressed payload flush at Close is what fails.
type headerOnlyWriter struct {
	writes int
	err    error
}

func (w *headerOnlyWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, w.err
	}
	return len(p), nil
}

func TestCompressReportsFlushError(t *testing.T) {
	writeErr := errors.New("disk full")

	err := compress(&headerOnlyWriter{err: writeErr}, strings.NewReader("payload"))
	if !errors.Is(err, writeErr) {
		t.Fatalf("got %v, want flush error %v", err, writeErr)
	}
}
