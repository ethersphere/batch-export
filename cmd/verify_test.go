package cmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/filestore"
)

// writeSnapshot writes a slim-format gzip snapshot with one entry per
// (blockNumber, logIndex) pair and returns its path.
func writeSnapshot(t *testing.T, name string, entries ...[2]uint64) string {
	t.Helper()

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(filestore.NewSlimLog(types.Log{BlockNumber: e[0], Index: uint(e[1])})); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestVerifyCmd(t *testing.T) {
	t.Parallel()

	oldPath := writeSnapshot(t, "old.ndjson.gzip", [2]uint64{1, 0}, [2]uint64{2, 0})
	newPath := writeSnapshot(t, "new.ndjson.gzip", [2]uint64{1, 0}, [2]uint64{2, 0}, [2]uint64{3, 0})

	c, err := newCommand()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c.root.SetOut(&out)
	c.root.SetArgs([]string{"verify", "--old", oldPath, "--new", newPath})

	if err := c.Execute(context.Background()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got, want := out.String(), "3\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestVerifyCmdRefusal(t *testing.T) {
	t.Parallel()

	oldPath := writeSnapshot(t, "old.ndjson.gzip", [2]uint64{1, 0}, [2]uint64{2, 0})
	newPath := writeSnapshot(t, "new.ndjson.gzip", [2]uint64{1, 0}, [2]uint64{3, 0})

	c, err := newCommand()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c.root.SetOut(&out)
	c.root.SetArgs([]string{"verify", "--old", oldPath, "--new", newPath})

	if err := c.Execute(context.Background()); err == nil {
		t.Fatal("want an error for a snapshot that drops an entry")
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}
