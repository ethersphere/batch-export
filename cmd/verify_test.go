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

	tests := []struct {
		name    string
		old     [][2]uint64
		new     [][2]uint64
		wantErr bool
		wantOut string
	}{
		{
			name:    "extension prints the last block",
			old:     [][2]uint64{{1, 0}, {2, 0}},
			new:     [][2]uint64{{1, 0}, {2, 0}, {3, 0}},
			wantOut: "3\n",
		},
		{
			name:    "a dropped entry is refused with empty stdout",
			old:     [][2]uint64{{1, 0}, {2, 0}},
			new:     [][2]uint64{{1, 0}, {3, 0}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, err := newCommand()
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			c.root.SetOut(&out)
			c.root.SetArgs([]string{
				"verify",
				"--old", writeSnapshot(t, "old.ndjson.gzip", tc.old...),
				"--new", writeSnapshot(t, "new.ndjson.gzip", tc.new...),
			})

			err = c.Execute(context.Background())
			if tc.wantErr && err == nil {
				t.Fatal("verify: want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("verify: %v", err)
			}
			if got := out.String(); got != tc.wantOut {
				t.Errorf("stdout = %q, want %q", got, tc.wantOut)
			}
		})
	}
}
