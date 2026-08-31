package verify_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/filestore"
	"github.com/ethersphere/batch-export/pkg/verify"
)

// testLog builds a log shaped like the ones the exporter writes.
func testLog(blockNumber uint64, logIndex uint) types.Log {
	return types.Log{
		Address:     common.HexToAddress("0x45a1502382541cd610cc9068e88727426b696293"),
		Topics:      []common.Hash{common.HexToHash("0xae46785019700e30375a5d7b4f91e32f8060ef085111f896ebf889450aa2ab5a")},
		Data:        bytes.Repeat([]byte{0xab}, 32),
		BlockNumber: blockNumber,
		TxHash:      common.HexToHash("0xb08f07656eaafa8efc458e2aa90773648d95ec8119873d212b4377dea5190cc0"),
		Index:       logIndex,
	}
}

// ndjson renders logs the way the exporter's slim format does.
func ndjson(t *testing.T, logs ...types.Log) []byte {
	t.Helper()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range logs {
		if err := enc.Encode(filestore.NewSlimLog(l)); err != nil {
			t.Fatal(err)
		}
	}

	return buf.Bytes()
}

// gz compresses b as a single gzip member.
func gz(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// write puts content into a fresh temp file and returns its path.
func write(t *testing.T, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestVerifySuperset(t *testing.T) {
	t.Parallel()

	// The new snapshot is the old member plus an appended member, exactly
	// the shape a resumed gzip export produces.
	oldBlob := gz(t, ndjson(t, testLog(1, 0), testLog(2, 0)))
	newBlob := append(bytes.Clone(oldBlob), gz(t, ndjson(t, testLog(2, 1), testLog(3, 0)))...)

	res, err := verify.Verify(
		write(t, "old.ndjson.gzip", oldBlob),
		write(t, "new.ndjson.gzip", newBlob),
	)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.LastBlock != 3 {
		t.Errorf("LastBlock = %d, want 3", res.LastBlock)
	}
	if res.Appended != 2 {
		t.Errorf("Appended = %d, want 2", res.Appended)
	}
	if res.OldTruncated {
		t.Error("OldTruncated = true, want false")
	}
}

func TestVerifyIdentical(t *testing.T) {
	t.Parallel()

	blob := gz(t, ndjson(t, testLog(1, 0), testLog(2, 0)))

	res, err := verify.Verify(
		write(t, "old.ndjson.gzip", blob),
		write(t, "new.ndjson.gzip", blob),
	)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.LastBlock != 2 || res.Appended != 0 {
		t.Errorf("got LastBlock=%d Appended=%d, want 2 and 0", res.LastBlock, res.Appended)
	}
}
