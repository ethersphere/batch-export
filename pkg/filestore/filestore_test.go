package filestore_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/filestore"
)

// blocksIn returns the block number of every log line in the file at path.
func blocksIn(t *testing.T, path string) []uint64 {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	var blocks []uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var l types.Log
		if err := json.Unmarshal(scanner.Bytes(), &l); err != nil {
			t.Fatalf("unmarshal %q: %v", scanner.Text(), err)
		}
		blocks = append(blocks, l.BlockNumber)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}

	return blocks
}

// seed writes logs for the given blocks to a fresh file at path.
func seed(t *testing.T, path string, blocks ...uint64) {
	t.Helper()

	w, err := filestore.CreateWriter(path)
	if err != nil {
		t.Fatalf("CreateWriter() error = %v", err)
	}
	if err := filestore.AppendLogsAsync(t.Context(), feed(blocks...), w, nil); err != nil {
		t.Fatalf("AppendLogsAsync() error = %v", err)
	}
}

// feed returns a closed channel already holding logs for the given blocks.
// Topics must stay a non-nil empty slice: go-ethereum's generated
// Log.UnmarshalJSON rejects a null "topics" as a missing required field.
func feed(blocks ...uint64) <-chan types.Log {
	ch := make(chan types.Log, len(blocks))
	for _, b := range blocks {
		ch <- types.Log{BlockNumber: b, Topics: []common.Hash{}}
	}
	close(ch)

	return ch
}

func TestCreateWriterReplacesExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}

	seed(t, path, 1, 2)

	want := []uint64{1, 2}
	if got := blocksIn(t, path); !slices.Equal(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
}

func TestAppendLogsAsyncKeepsExistingContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	seed(t, path, 1, 2)

	w, err := filestore.AppendWriter(path)
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}
	if err := filestore.AppendLogsAsync(t.Context(), feed(3, 4), w, nil); err != nil {
		t.Fatalf("AppendLogsAsync() error = %v", err)
	}

	want := []uint64{1, 2, 3, 4}
	if got := blocksIn(t, path); !slices.Equal(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
}

func TestAppendLogsAsyncSkipsFilteredLogs(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	seed(t, path, 1, 2)

	w, err := filestore.AppendWriter(path)
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}
	skip := func(l types.Log) bool { return l.BlockNumber <= 2 }
	if err := filestore.AppendLogsAsync(t.Context(), feed(1, 2, 3, 4), w, skip); err != nil {
		t.Fatalf("AppendLogsAsync() error = %v", err)
	}

	want := []uint64{1, 2, 3, 4}
	if got := blocksIn(t, path); !slices.Equal(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
}

func TestAppendLogsAsyncClosesWriterOnCancel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	seed(t, path, 1)

	w, err := filestore.AppendWriter(path)
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// An open channel that never delivers, so cancellation is the only exit.
	if err := filestore.AppendLogsAsync(ctx, make(chan types.Log), w, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendLogsAsync() error = %v, want context.Canceled", err)
	}

	// The writer must already be closed; closing it again must fail.
	if err := w.Close(); err == nil {
		t.Error("writer was left open after cancellation")
	}
}
