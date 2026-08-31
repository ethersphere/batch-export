package verify_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/filestore"
	"github.com/ethersphere/batch-export/pkg/resume"
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

func TestVerifyRefusals(t *testing.T) {
	t.Parallel()

	oldContent := ndjson(t, testLog(1, 0), testLog(2, 0))

	// mutated flips one byte inside the first entry's blockNumber.
	mutated := bytes.Replace(slices.Clone(oldContent), []byte(`"blockNumber":"0x1"`), []byte(`"blockNumber":"0x9"`), 1)

	tests := []struct {
		name    string
		old     []byte
		new     []byte
		wantErr error
	}{
		{
			name:    "mutated entry inside the old content",
			old:     gz(t, oldContent),
			new:     gz(t, append(mutated, ndjson(t, testLog(10, 0))...)),
			wantErr: verify.ErrMismatch,
		},
		{
			name:    "dropped entry",
			old:     gz(t, oldContent),
			new:     gz(t, ndjson(t, testLog(1, 0), testLog(3, 0))),
			wantErr: verify.ErrMismatch,
		},
		{
			name:    "new shorter than old",
			old:     gz(t, oldContent),
			new:     gz(t, ndjson(t, testLog(1, 0))),
			wantErr: verify.ErrMismatch,
		},
		{
			name:    "appended entry repeats the cursor",
			old:     gz(t, oldContent),
			new:     append(gz(t, oldContent), gz(t, ndjson(t, testLog(2, 0)))...),
			wantErr: verify.ErrOrder,
		},
		{
			name:    "appended entries out of order",
			old:     gz(t, oldContent),
			new:     append(gz(t, oldContent), gz(t, ndjson(t, testLog(5, 0), testLog(4, 0)))...),
			wantErr: verify.ErrOrder,
		},
		{
			name:    "new ends in an interrupted write",
			old:     oldContent,
			new:     append(slices.Clone(oldContent), `{"blockNumber":"0x3"`...),
			wantErr: verify.ErrTruncatedNew,
		},
		{
			name:    "old holds no complete entry",
			old:     nil,
			new:     oldContent,
			wantErr: resume.ErrNoLogs,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := verify.Verify(
				write(t, "old", tc.old),
				write(t, "new", tc.new),
			)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Verify error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyMalformedAppendedLine(t *testing.T) {
	t.Parallel()

	oldContent := ndjson(t, testLog(1, 0), testLog(2, 0))
	// The middle appended line is valid JSON but not a log entry; resume.Read
	// on a plain file only inspects the tail, so only checkTail can catch it.
	newContent := append(slices.Clone(oldContent), "{\"foo\":1}\n"...)
	newContent = append(newContent, ndjson(t, testLog(3, 0))...)

	_, err := verify.Verify(
		write(t, "old", oldContent),
		write(t, "new", newContent),
	)
	if err == nil {
		t.Fatal("want an error for a malformed appended line")
	}
}

func TestVerifyOldTruncated(t *testing.T) {
	t.Parallel()

	oldContent := ndjson(t, testLog(1, 0), testLog(2, 0))
	oldBlob := append(slices.Clone(oldContent), `{"block`...)
	newBlob := append(slices.Clone(oldContent), ndjson(t, testLog(3, 0))...)

	res, err := verify.Verify(
		write(t, "old", oldBlob),
		write(t, "new", newBlob),
	)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.OldTruncated {
		t.Error("OldTruncated = false, want true")
	}
	if res.LastBlock != 3 || res.Appended != 1 {
		t.Errorf("got LastBlock=%d Appended=%d, want 3 and 1", res.LastBlock, res.Appended)
	}
}

func TestVerifyFormatCombinations(t *testing.T) {
	t.Parallel()

	oldContent := ndjson(t, testLog(1, 0), testLog(2, 0))
	newContent := append(slices.Clone(oldContent), ndjson(t, testLog(3, 0))...)

	tests := []struct {
		name string
		old  []byte
		new  []byte
	}{
		{"plain old, plain new", oldContent, newContent},
		{"plain old, gzip new", oldContent, gz(t, newContent)},
		{"gzip old, plain new", gz(t, oldContent), newContent},
		{"gzip old, gzip new", gz(t, oldContent), gz(t, newContent)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := verify.Verify(
				write(t, "old", tc.old),
				write(t, "new", tc.new),
			)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if res.LastBlock != 3 || res.Appended != 1 {
				t.Errorf("got LastBlock=%d Appended=%d, want 3 and 1", res.LastBlock, res.Appended)
			}
		})
	}
}
