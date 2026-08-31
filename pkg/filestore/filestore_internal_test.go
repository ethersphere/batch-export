package filestore

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

// closeFailWriter accepts all writes but fails on Close, mimicking a file
// whose OS-buffered data cannot be flushed (disk full, quota, network fs).
type closeFailWriter struct{ closeErr error }

func (w *closeFailWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *closeFailWriter) Close() error                { return w.closeErr }

func TestSaveLogsReportsCloseError(t *testing.T) {
	closeErr := errors.New("flush to disk failed")

	logChan := make(chan types.Log, 1)
	logChan <- types.Log{BlockNumber: 1}
	close(logChan)

	err := saveLogs(context.Background(), logChan, &closeFailWriter{closeErr: closeErr}, false)
	if !errors.Is(err, closeErr) {
		t.Fatalf("got %v, want close error %v", err, closeErr)
	}
}

func TestSaveLogsKeepsContextErrorOnCloseFailure(t *testing.T) {
	closeErr := errors.New("flush to disk failed")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := saveLogs(ctx, make(chan types.Log), &closeFailWriter{closeErr: closeErr}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("got %v, want close error %v", err, closeErr)
	}
}
