package filestore_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/filestore"
)

func TestSaveLogsAsyncWritesNDJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.ndjson")

	logChan := make(chan types.Log, 2)
	for i := uint64(1); i <= 2; i++ {
		logChan <- types.Log{
			Address:     common.HexToAddress("0x000000000000000000000000000000000000bEEF"),
			Topics:      []common.Hash{common.HexToHash("0x11")},
			Data:        []byte{0xde, 0xad},
			BlockNumber: i,
		}
	}
	close(logChan)

	if err := filestore.SaveLogsAsync(context.Background(), logChan, path); err != nil {
		t.Fatalf("SaveLogsAsync: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer file.Close()

	var got []types.Log
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var l types.Log
		if err := json.Unmarshal(scanner.Bytes(), &l); err != nil {
			t.Fatalf("decode line %d: %v", len(got)+1, err)
		}
		got = append(got, l)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan output: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d logs, want 2", len(got))
	}
	for i, l := range got {
		if l.BlockNumber != uint64(i+1) {
			t.Errorf("log %d: blockNumber got %d want %d", i, l.BlockNumber, i+1)
		}
	}
}
