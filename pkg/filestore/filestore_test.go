package filestore_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

	if err := filestore.SaveLogsAsync(context.Background(), logChan, path, false); err != nil {
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
