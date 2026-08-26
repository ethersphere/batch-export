package resume_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/resume"
)

// testLog builds a log shaped like the ones the exporter writes. types.Log
// always marshals blockNumber and logIndex, even at zero.
func testLog(blockNumber uint64, logIndex uint) types.Log {
	return types.Log{
		Address:     common.HexToAddress("0x45a1502382541cd610cc9068e88727426b696293"),
		Topics:      []common.Hash{common.HexToHash("0xae46785019700e30375a5d7b4f91e32f8060ef085111f896ebf889450aa2ab5a")},
		Data:        bytes.Repeat([]byte{0xab}, 32),
		BlockNumber: blockNumber,
		TxHash:      common.HexToHash("0xb08f07656eaafa8efc458e2aa90773648d95ec8119873d212b4377dea5190cc0"),
		TxIndex:     9,
		BlockHash:   common.HexToHash("0x86dc5f9da5fcba5191f6b3d2ba995bd75532ef369a7baa3970b3fb292ae91324"),
		Index:       logIndex,
		Removed:     false,
	}
}

// ndjson renders logs as newline-delimited JSON, the exporter's output format.
func ndjson(t *testing.T, logs ...types.Log) []byte {
	t.Helper()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range logs {
		if err := enc.Encode(l); err != nil {
			t.Fatalf("encode log: %v", err)
		}
	}
	return buf.Bytes()
}

// gz compresses b into a single gzip member.
func gz(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// write puts content in a temp file and returns its path.
func write(t *testing.T, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestReadCursor(t *testing.T) {
	t.Parallel()

	threeLogs := ndjson(t, testLog(100, 0), testLog(101, 1), testLog(102, 7))

	// many spans several 64 KiB backward-read windows.
	manyLogs := make([]types.Log, 0, 2000)
	for i := range 2000 {
		manyLogs = append(manyLogs, testLog(uint64(1000+i), uint(i%16)))
	}
	many := ndjson(t, manyLogs...)

	// garbageLines are newline-delimited but unparseable, as if a different
	// file had been concatenated onto a good export.
	garbageLines := bytes.Repeat([]byte("not-json\n"), 12*1024)

	tests := []struct {
		name           string
		content        []byte
		wantBlock      uint64
		wantIndex      uint
		wantCompressed bool
	}{
		{
			name:      "plain ndjson",
			content:   threeLogs,
			wantBlock: 102,
			wantIndex: 7,
		},
		{
			name:      "single line",
			content:   ndjson(t, testLog(55, 3)),
			wantBlock: 55,
			wantIndex: 3,
		},
		{
			name:      "truncated trailing line is discarded",
			content:   append(append([]byte{}, threeLogs...), []byte(`{"address":"0x45a15","topics":["0xae`)...),
			wantBlock: 102,
			wantIndex: 7,
		},
		{
			name:      "trailing line without newline is still read",
			content:   bytes.TrimSuffix(threeLogs, []byte("\n")),
			wantBlock: 102,
			wantIndex: 7,
		},
		{
			name:      "line missing blockNumber is skipped",
			content:   append(append([]byte{}, threeLogs...), []byte("{\"address\":\"0x1\",\"topics\":[],\"data\":\"0x\"}\n")...),
			wantBlock: 102,
			wantIndex: 7,
		},
		{
			name:      "spans multiple backward read windows",
			content:   many,
			wantBlock: 2999,
			wantIndex: 15,
		},
		{
			name:      "walks back across windows of garbage lines",
			content:   append(append([]byte{}, threeLogs...), garbageLines...),
			wantBlock: 102,
			wantIndex: 7,
		},
		{
			name:           "gzip",
			content:        gz(t, threeLogs),
			wantBlock:      102,
			wantIndex:      7,
			wantCompressed: true,
		},
		{
			name:           "gzip spanning many logs",
			content:        gz(t, many),
			wantBlock:      2999,
			wantIndex:      15,
			wantCompressed: true,
		},
		{
			name:           "multi member gzip reads through to the last member",
			content:        append(gz(t, threeLogs), gz(t, ndjson(t, testLog(200, 2)))...),
			wantBlock:      200,
			wantIndex:      2,
			wantCompressed: true,
		},
		{
			// A resume interrupted before the second member was flushed: the
			// header is present but carries no decodable data.
			name:           "multi member gzip with unflushed final member",
			content:        append(gz(t, threeLogs), []byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 0xff}...),
			wantBlock:      102,
			wantIndex:      7,
			wantCompressed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resume.Read(write(t, "export.ndjson", tt.content))
			if err != nil {
				t.Fatalf("Read() error = %v, want nil", err)
			}
			if got.BlockNumber != tt.wantBlock {
				t.Errorf("BlockNumber = %d, want %d", got.BlockNumber, tt.wantBlock)
			}
			if got.LogIndex != tt.wantIndex {
				t.Errorf("LogIndex = %d, want %d", got.LogIndex, tt.wantIndex)
			}
			if got.Compressed != tt.wantCompressed {
				t.Errorf("Compressed = %t, want %t", got.Compressed, tt.wantCompressed)
			}
		})
	}
}

func TestReadCursorErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content []byte
	}{
		{name: "empty file", content: []byte{}},
		{name: "only a newline", content: []byte("\n")},
		{name: "only garbage lines", content: bytes.Repeat([]byte("not-json\n"), 10)},
		{name: "single unterminated line larger than the cap", content: bytes.Repeat([]byte("x"), 2<<20)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := resume.Read(write(t, "export.ndjson", tt.content))
			if !errors.Is(err, resume.ErrNoLogs) {
				t.Fatalf("Read() error = %v, want ErrNoLogs", err)
			}
		})
	}
}

func TestReadMissingFile(t *testing.T) {
	t.Parallel()

	_, err := resume.Read(filepath.Join(t.TempDir(), "does-not-exist.ndjson"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read() error = %v, want os.ErrNotExist", err)
	}
}

func TestCursorSkip(t *testing.T) {
	t.Parallel()

	cursor := &resume.Cursor{BlockNumber: 100, LogIndex: 5}

	tests := []struct {
		name string
		log  types.Log
		want bool
	}{
		{name: "earlier block", log: testLog(99, 0), want: true},
		{name: "same block earlier index", log: testLog(100, 4), want: true},
		{name: "same block same index", log: testLog(100, 5), want: true},
		{name: "same block later index", log: testLog(100, 6), want: false},
		{name: "later block index zero", log: testLog(101, 0), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := cursor.Skip(tt.log); got != tt.want {
				t.Errorf("Skip() = %t, want %t", got, tt.want)
			}
		})
	}
}
