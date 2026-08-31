package filestore_test

import (
	"bufio"
	"bytes"
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

// sampleLog returns a types.Log with every field set to a distinct, non-zero
// value so tests notice when any field's JSON shape drifts.
func sampleLog() types.Log {
	return types.Log{
		Address: common.HexToAddress("0x000000000000000000000000000000000000bEEF"),
		Topics: []common.Hash{
			common.HexToHash("0x1122222222222222222222222222222222222222222222222222222222222222"),
			common.HexToHash("0x9988888888888888888888888888888888888888888888888888888888888888"),
		},
		Data:           []byte{0xde, 0xad, 0xbe, 0xef},
		BlockNumber:    42,
		TxHash:         common.HexToHash("0x3344444444444444444444444444444444444444444444444444444444444444"),
		TxIndex:        7,
		BlockHash:      common.HexToHash("0x5566666666666666666666666666666666666666666666666666666666666666"),
		BlockTimestamp: 1700000000,
		Index:          3,
		Removed:        false,
	}
}

// TestSlimMatchesFullForKeptKeys guards against geth changing the JSON shape of
// any kept field on a future bump: it compares the slim and full encodings
// key-by-key. Also asserts slim does not leak any non-kept keys.
func TestSlimMatchesFullForKeptKeys(t *testing.T) {
	in := sampleLog()

	fullJSON, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	slimJSON, err := json.Marshal(filestore.NewSlimLog(in))
	if err != nil {
		t.Fatalf("marshal slim: %v", err)
	}

	var full, slim map[string]json.RawMessage
	if err := json.Unmarshal(fullJSON, &full); err != nil {
		t.Fatalf("unmarshal full: %v", err)
	}
	if err := json.Unmarshal(slimJSON, &slim); err != nil {
		t.Fatalf("unmarshal slim: %v", err)
	}

	kept := []string{"address", "topics", "data", "blockNumber", "transactionHash", "logIndex"}
	keptSet := map[string]struct{}{}
	for _, k := range kept {
		keptSet[k] = struct{}{}
		if !bytes.Equal(full[k], slim[k]) {
			t.Errorf("key %q diverged: full=%s slim=%s", k, full[k], slim[k])
		}
	}

	for k := range slim {
		if _, ok := keptSet[k]; !ok {
			t.Errorf("slim leaked unexpected key %q", k)
		}
	}
}

// TestSlimRoundTripsThroughGethDecoder enforces the Bee-side contract: a slim
// record must decode into types.Log via geth's UnmarshalJSON with no missing
// required fields and all kept fields preserved.
func TestSlimRoundTripsThroughGethDecoder(t *testing.T) {
	in := sampleLog()

	b, err := json.Marshal(filestore.NewSlimLog(in))
	if err != nil {
		t.Fatalf("marshal slim: %v", err)
	}

	var out types.Log
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode slim into types.Log: %v", err)
	}

	if out.Address != in.Address {
		t.Errorf("address: got %s want %s", out.Address.Hex(), in.Address.Hex())
	}
	if len(out.Topics) != len(in.Topics) {
		t.Fatalf("topics len: got %d want %d", len(out.Topics), len(in.Topics))
	}
	for i := range in.Topics {
		if out.Topics[i] != in.Topics[i] {
			t.Errorf("topics[%d]: got %s want %s", i, out.Topics[i].Hex(), in.Topics[i].Hex())
		}
	}
	if !bytes.Equal(out.Data, in.Data) {
		t.Errorf("data: got %x want %x", out.Data, in.Data)
	}
	if out.BlockNumber != in.BlockNumber {
		t.Errorf("blockNumber: got %d want %d", out.BlockNumber, in.BlockNumber)
	}
	if out.TxHash != in.TxHash {
		t.Errorf("txHash: got %s want %s", out.TxHash.Hex(), in.TxHash.Hex())
	}
	if out.Index != in.Index {
		t.Errorf("logIndex: got %d want %d", out.Index, in.Index)
	}
}

// TestAppendLogsAsyncWritesSlimShape covers the slim path end to end: a log
// written with slim enabled lands in the file holding only the kept keys.
func TestAppendLogsAsyncWritesSlimShape(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")

	ch := make(chan types.Log, 1)
	ch <- sampleLog()
	close(ch)

	w, err := filestore.CreateWriter(path)
	if err != nil {
		t.Fatalf("CreateWriter() error = %v", err)
	}
	if err := filestore.AppendLogsAsync(t.Context(), ch, w, nil, true); err != nil {
		t.Fatalf("AppendLogsAsync() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", data, err)
	}

	kept := []string{"address", "topics", "data", "blockNumber", "transactionHash", "logIndex"}
	for _, k := range kept {
		if _, ok := got[k]; !ok {
			t.Errorf("slim output is missing key %q", k)
		}
	}
	if len(got) != len(kept) {
		t.Errorf("slim output has %d keys, want %d: %s", len(got), len(kept), data)
	}
}

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
	if err := filestore.AppendLogsAsync(t.Context(), feed(blocks...), w, nil, false); err != nil {
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
	if err := filestore.AppendLogsAsync(t.Context(), feed(3, 4), w, nil, false); err != nil {
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
	if err := filestore.AppendLogsAsync(t.Context(), feed(1, 2, 3, 4), w, skip, false); err != nil {
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
	if err := filestore.AppendLogsAsync(ctx, make(chan types.Log), w, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendLogsAsync() error = %v, want context.Canceled", err)
	}

	// The writer must already be closed; closing it again must fail.
	if err := w.Close(); err == nil {
		t.Error("writer was left open after cancellation")
	}
}

// closeFailWriter accepts all writes but fails on Close, mimicking a file
// whose OS-buffered data cannot be flushed (disk full, quota, network fs).
type closeFailWriter struct{ closeErr error }

func (w *closeFailWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *closeFailWriter) Close() error                { return w.closeErr }

func TestAppendLogsAsyncReportsCloseError(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("flush to disk failed")

	err := filestore.AppendLogsAsync(t.Context(), feed(1), &closeFailWriter{closeErr: closeErr}, nil, false)
	if !errors.Is(err, closeErr) {
		t.Fatalf("AppendLogsAsync() error = %v, want close error %v", err, closeErr)
	}
}

func TestAppendLogsAsyncKeepsContextErrorOnCloseFailure(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("flush to disk failed")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := filestore.AppendLogsAsync(ctx, make(chan types.Log), &closeFailWriter{closeErr: closeErr}, nil, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendLogsAsync() error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("AppendLogsAsync() error = %v, want close error %v", err, closeErr)
	}
}
