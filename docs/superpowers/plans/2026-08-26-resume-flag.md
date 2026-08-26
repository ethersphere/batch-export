# Resume Flag Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `export --resume <file>` so an interrupted export continues from the end of an existing `.ndjson`, `.gz`, or `.gzip` file instead of restarting from the contract's start block.

**Architecture:** A new `pkg/resume` package reads the tail of a previous export and returns a `Cursor` (last block number + log index + whether the file is gzip). `cmd/export.go` uses that cursor as the start block and appends new logs to the same file — plain files via `O_APPEND`, gzip files by appending a second gzip member (concatenated members are a valid gzip stream, verified against both `gzcat` and Go's `gzip.Reader`). Logs already present in the boundary block are filtered out during the write.

**Tech Stack:** Go 1.25, cobra, `github.com/ethereum/go-ethereum` v1.15.11 (`core/types.Log`, `common/hexutil`), `github.com/ethersphere/bee/v2` v2.7.0 (`pkg/log`), stdlib `compress/gzip`.

**Spec:** No separate spec file — this was a bounded change designed and approved in-session. The approved design is reproduced in full under "Design Summary" below; executors should treat that section as the spec.

## Design Summary

1. **Flag:** `--resume` / `-r`, a path to a previous export. When set it overrides `--start` and `--output`.
2. **Format detection:** by magic bytes (`0x1f 0x8b`), never by file extension. The repo's own archives use `.gzip` while the request also mentions `.gz`; magic bytes make the extension irrelevant.
3. **Cursor:** the last line of the file that parses as JSON *and* carries both `blockNumber` and `logIndex`. Lines that fail either check are skipped, which discards a truncated trailing line left by a hard kill.
4. **Resume point:** `startBlock = cursor.BlockNumber` **inclusive**, because that block may have been only partially written. While writing, any log with `blockNumber < cursor.BlockNumber`, or `blockNumber == cursor.BlockNumber && logIndex <= cursor.LogIndex`, is skipped. No gaps, no duplicates.
5. **Writing:** the resume file is opened `O_APPEND`. Gzip files get a fresh `gzip.NewWriter` (a new member); plain files get the JSON encoder directly.
6. **`--compress` interaction:** a no-op when resuming an already-gzipped file; unchanged behavior when resuming a plain `.ndjson`.

## Global Constraints

- Go version: `go 1.25` (per `go.mod`). Do not raise or lower it.
- Do not add dependencies. Everything needed is already in `go.mod`: stdlib, `go-ethereum`, `bee/v2`, `cobra`.
- Lint config (`.golangci.yml`) enables `copyloopvar`, `errname`, `errorlint`, `goconst`, `misspell`, `nilerr`, `unconvert`, plus `gofmt` and `gofumpt` formatters. `errorlint` means: always wrap with `%w`, always compare with `errors.Is`/`errors.As`.
- Every exported identifier gets a doc comment starting with its own name.
- Error strings are lowercase and unpunctuated, matching the existing `pkg/` style (`"error creating file: %w"`).
- Commit messages use Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`, `refactor:`), matching this repo's history.
- Tests run via `make test`, which is `go test -v ./pkg/...`. The repo currently has **zero** test files; these will be the first.
- Do not reformat or restructure code unrelated to this feature.

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `pkg/resume/resume.go` | **Create** | Detect gzip vs plain, find the last complete log line, expose `Cursor` + `Cursor.Skip`. All tail-reading edge cases live here and nowhere else. |
| `pkg/resume/resume_test.go` | **Create** | Table tests for every tail-reading edge case, plus an append→re-read round trip. |
| `pkg/gzipstore/gzipstore.go` | **Modify** | Add `AppendWriter` returning an `io.WriteCloser` that appends a new gzip member. Existing `CompressFile` untouched. |
| `pkg/gzipstore/gzipstore_test.go` | **Create** | Verify an appended member reads back as one continuous stream. |
| `pkg/filestore/filestore.go` | **Modify** | Extract the write loop into an unexported `writeLogs`; add `AppendWriter` and `AppendLogsAsync(ctx, logChan, w, skip)`. `SaveLogsAsync` keeps its current signature. |
| `pkg/filestore/filestore_test.go` | **Create** | Verify truncate-vs-append semantics and that `skip` filters correctly. |
| `cmd/export.go` | **Modify** | Register `--resume`, read the cursor before dialing RPC, pick the writer, warn on overridden flags. Also fix the missing `wg.Wait()` on the cancellation path. |
| `README.md` | **Modify** | Document the flag in the Features list and the flag table. |

**Why `pkg/resume` is its own package:** backward chunked reads, truncated-line recovery, and multi-member gzip handling are the only genuinely tricky logic in this feature. Isolating them behind `Read(path) (*Cursor, error)` keeps `cmd/export.go` readable and makes the edge cases testable without an RPC endpoint.

---

### Task 1: `pkg/resume` — read the cursor from a previous export

**Files:**
- Create: `pkg/resume/resume.go`
- Test: `pkg/resume/resume_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Cursor struct { BlockNumber uint64; LogIndex uint; Compressed bool }`
  - `func Read(path string) (*Cursor, error)`
  - `func (c *Cursor) Skip(l types.Log) bool`
  - `var ErrNoLogs error`

- [ ] **Step 1: Write the failing tests**

Create `pkg/resume/resume_test.go`. Note the package is `resume_test` (external) — later tasks add a round-trip test here that imports `filestore` and `gzipstore`, and an external test package keeps that free of import cycles.

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./pkg/resume/...
```

Expected: FAIL — `no required module provides package github.com/ethersphere/batch-export/pkg/resume` (the package does not exist yet).

- [ ] **Step 3: Write the implementation**

Create `pkg/resume/resume.go`:

```go
// Package resume locates the point at which a previous export stopped so that
// a new run can continue from there.
package resume

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	// windowSize is how much of a plain NDJSON file is read at a time when
	// walking backwards from the end.
	windowSize = 64 * 1024
	// maxLineBytes caps how long a single line may be. Exported log lines run
	// to a few hundred bytes, so anything beyond this is corruption, and the
	// cap keeps a file without newlines from being read into memory whole.
	maxLineBytes = 1 << 20
)

// ErrNoLogs indicates that a file holds no complete log entry to resume from.
var ErrNoLogs = errors.New("no complete log entry found")

// Cursor marks the last log entry saved by a previous export.
type Cursor struct {
	// BlockNumber is the block of the last saved log. A resumed export
	// re-queries this block, because an interrupted run may have saved only
	// some of its logs.
	BlockNumber uint64
	// LogIndex is the index of the last saved log within BlockNumber.
	LogIndex uint
	// Compressed reports whether the file holds gzip data rather than plain
	// NDJSON.
	Compressed bool
}

// Skip reports whether l was already written to the file the cursor came from.
func (c *Cursor) Skip(l types.Log) bool {
	if l.BlockNumber != c.BlockNumber {
		return l.BlockNumber < c.BlockNumber
	}
	return l.Index <= c.LogIndex
}

// cursorLine is the part of an exported log line a cursor is built from. Both
// fields are pointers so that a line missing either one can be rejected.
type cursorLine struct {
	BlockNumber *hexutil.Uint64 `json:"blockNumber"`
	LogIndex    *hexutil.Uint   `json:"logIndex"`
}

// Read returns a cursor for the last complete log entry in the file at path.
// The file may be plain NDJSON or gzip; the format is detected from its
// leading bytes rather than its extension. Lines that do not parse are
// skipped, which discards a partial line left behind by an interrupted write.
func Read(path string) (*Cursor, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening resume file: %w", err)
	}
	defer file.Close()

	compressed, err := isGzip(file)
	if err != nil {
		return nil, fmt.Errorf("error reading resume file: %w", err)
	}

	var cursor *Cursor
	if compressed {
		cursor, err = lastCursorGzip(file)
	} else {
		cursor, err = lastCursorPlain(file)
	}
	if err != nil {
		return nil, err
	}

	cursor.Compressed = compressed

	return cursor, nil
}

// isGzip reports whether file starts with the gzip magic bytes.
func isGzip(file *os.File) (bool, error) {
	var magic [2]byte

	n, err := file.ReadAt(magic[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if n < len(magic) {
		return false, nil
	}

	return magic[0] == 0x1f && magic[1] == 0x8b, nil
}

// lastCursorPlain walks a plain NDJSON file backwards a window at a time and
// returns a cursor for the last line that parses.
func lastCursorPlain(file *os.File) (*Cursor, error) {
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("error seeking resume file: %w", err)
	}

	// carry holds bytes from the window just read that precede its first
	// newline. They belong to a line whose start lies in the next window back.
	var carry []byte

	for offset > 0 {
		size := int64(windowSize)
		if offset < size {
			size = offset
		}
		offset -= size

		window := make([]byte, size)
		if _, err := file.ReadAt(window, offset); err != nil {
			return nil, fmt.Errorf("error reading resume file: %w", err)
		}
		window = append(window, carry...)

		for {
			i := bytes.LastIndexByte(window, '\n')
			if i < 0 {
				break
			}
			if cursor, err := parseCursor(window[i+1:]); err == nil {
				return cursor, nil
			}
			window = window[:i]
		}

		carry = window
		if len(carry) > maxLineBytes {
			return nil, ErrNoLogs
		}
	}

	return parseCursor(carry)
}

// lastCursorGzip returns a cursor for the last line that parses in a gzip
// file. Gzip cannot be seeked, so the whole stream is decompressed. A stream
// truncated by an interrupted run still yields the last line it did decode.
func lastCursorGzip(file *os.File) (*Cursor, error) {
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("error opening gzip resume file: %w", err)
	}
	defer reader.Close()

	// Multistream is on by default, so concatenated members read as one stream.
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLineBytes)

	var last *Cursor
	for scanner.Scan() {
		if cursor, err := parseCursor(scanner.Bytes()); err == nil {
			last = cursor
		}
	}

	if last != nil {
		return last, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading gzip resume file: %w", err)
	}

	return nil, ErrNoLogs
}

// parseCursor builds a cursor from a single NDJSON line. It rejects blank and
// truncated lines, and logs that carry no block number.
func parseCursor(line []byte) (*Cursor, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, ErrNoLogs
	}

	var parsed cursorLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return nil, ErrNoLogs
	}
	if parsed.BlockNumber == nil || parsed.LogIndex == nil {
		return nil, ErrNoLogs
	}

	return &Cursor{
		BlockNumber: uint64(*parsed.BlockNumber),
		LogIndex:    uint(*parsed.LogIndex),
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./pkg/resume/... && gofumpt -l pkg/resume && go vet ./pkg/resume/...
```

Expected: `ok github.com/ethersphere/batch-export/pkg/resume`, no files listed by `gofumpt`, no vet output.

If `gofumpt` is not installed, run `go run mvdan.cc/gofumpt@latest -l pkg/resume` instead.

- [ ] **Step 5: Commit**

```bash
git add pkg/resume/resume.go pkg/resume/resume_test.go
git commit -m "feat(resume): read the last saved log from a previous export"
```

---

### Task 2: `pkg/gzipstore` — append a gzip member

**Files:**
- Modify: `pkg/gzipstore/gzipstore.go` (add to the existing file; leave `CompressFile` as-is)
- Test: `pkg/gzipstore/gzipstore_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func AppendWriter(filePath string) (io.WriteCloser, error)`

- [ ] **Step 1: Write the failing test**

Create `pkg/gzipstore/gzipstore_test.go`:

```go
package gzipstore_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethersphere/batch-export/pkg/gzipstore"
)

// writeGzip creates a gzip file holding content and returns its path.
func writeGzip(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "export.ndjson.gzip")

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

// readGzip decompresses the whole file, following every member.
func readGzip(t *testing.T, path string) string {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}

	return string(got)
}

func TestAppendWriterAddsReadableMember(t *testing.T) {
	t.Parallel()

	path := writeGzip(t, "first\nsecond\n")

	w, err := gzipstore.AppendWriter(path)
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}
	if _, err := io.WriteString(w, "third\nfourth\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	const want = "first\nsecond\nthird\nfourth\n"
	if got := readGzip(t, path); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestAppendWriterRepeatedAppends(t *testing.T) {
	t.Parallel()

	path := writeGzip(t, "a\n")

	for _, line := range []string{"b\n", "c\n"} {
		w, err := gzipstore.AppendWriter(path)
		if err != nil {
			t.Fatalf("AppendWriter() error = %v", err)
		}
		if _, err := io.WriteString(w, line); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}

	const want = "a\nb\nc\n"
	if got := readGzip(t, path); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestAppendWriterMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := gzipstore.AppendWriter(filepath.Join(t.TempDir(), "nope.gzip")); err == nil {
		t.Fatal("AppendWriter() error = nil, want an error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./pkg/gzipstore/...
```

Expected: FAIL — `undefined: gzipstore.AppendWriter`.

- [ ] **Step 3: Write the implementation**

Add to `pkg/gzipstore/gzipstore.go`. Add `"errors"` to the import block; `io` and `os` are already imported.

```go
// AppendWriter opens an existing gzip file for appending and returns a writer
// that adds a new gzip member to it. Concatenated members form a valid gzip
// stream, so readers see one continuous file and the existing bytes are never
// rewritten. The caller must close the writer to flush the member.
func AppendWriter(filePath string) (io.WriteCloser, error) {
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip file '%s' for appending: %w", filePath, err)
	}

	return &memberWriter{file: file, gzip: gzip.NewWriter(file)}, nil
}

// memberWriter writes one gzip member and owns the file it was opened from.
type memberWriter struct {
	file *os.File
	gzip *gzip.Writer
}

func (w *memberWriter) Write(p []byte) (int, error) {
	return w.gzip.Write(p)
}

// Close finishes the gzip member and then closes the file. Both are attempted
// even if the first fails, so the descriptor is never leaked.
func (w *memberWriter) Close() error {
	return errors.Join(w.gzip.Close(), w.file.Close())
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./pkg/gzipstore/... && go vet ./pkg/gzipstore/...
```

Expected: `ok github.com/ethersphere/batch-export/pkg/gzipstore`, no vet output.

- [ ] **Step 5: Commit**

```bash
git add pkg/gzipstore/gzipstore.go pkg/gzipstore/gzipstore_test.go
git commit -m "feat(gzipstore): add AppendWriter for appending a gzip member"
```

---

### Task 3: `pkg/filestore` — append logs with a skip filter

**Files:**
- Modify: `pkg/filestore/filestore.go` (whole file; see the full replacement below)
- Test: `pkg/filestore/filestore_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks. `Cursor.Skip` from Task 1 satisfies the `skip` parameter, but this package must not import `pkg/resume` — it takes a plain function so the dependency runs one way only.
- Produces:
  - `func SaveLogsAsync(ctx context.Context, logChan <-chan types.Log, filePath string) error` (unchanged signature)
  - `func AppendWriter(filePath string) (io.WriteCloser, error)`
  - `func AppendLogsAsync(ctx context.Context, logChan <-chan types.Log, w io.WriteCloser, skip func(types.Log) bool) error`

- [ ] **Step 1: Write the failing test**

Create `pkg/filestore/filestore_test.go`:

```go
package filestore_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// feed returns a closed channel already holding logs for the given blocks.
func feed(blocks ...uint64) <-chan types.Log {
	ch := make(chan types.Log, len(blocks))
	for _, b := range blocks {
		ch <- types.Log{BlockNumber: b}
	}
	close(ch)

	return ch
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestSaveLogsAsyncReplacesExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}

	if err := filestore.SaveLogsAsync(t.Context(), feed(1, 2), path); err != nil {
		t.Fatalf("SaveLogsAsync() error = %v", err)
	}

	want := []uint64{1, 2}
	if got := blocksIn(t, path); !equal(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
}

func TestAppendLogsAsyncKeepsExistingContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	if err := filestore.SaveLogsAsync(t.Context(), feed(1, 2), path); err != nil {
		t.Fatalf("SaveLogsAsync() error = %v", err)
	}

	w, err := filestore.AppendWriter(path)
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}
	if err := filestore.AppendLogsAsync(t.Context(), feed(3, 4), w, nil); err != nil {
		t.Fatalf("AppendLogsAsync() error = %v", err)
	}

	want := []uint64{1, 2, 3, 4}
	if got := blocksIn(t, path); !equal(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
}

func TestAppendLogsAsyncSkipsFilteredLogs(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	if err := filestore.SaveLogsAsync(t.Context(), feed(1, 2), path); err != nil {
		t.Fatalf("SaveLogsAsync() error = %v", err)
	}

	w, err := filestore.AppendWriter(path)
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}
	skip := func(l types.Log) bool { return l.BlockNumber <= 2 }
	if err := filestore.AppendLogsAsync(t.Context(), feed(1, 2, 3, 4), w, skip); err != nil {
		t.Fatalf("AppendLogsAsync() error = %v", err)
	}

	want := []uint64{1, 2, 3, 4}
	if got := blocksIn(t, path); !equal(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
}

func TestAppendLogsAsyncClosesWriterOnCancel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "export.ndjson")
	if err := filestore.SaveLogsAsync(t.Context(), feed(1), path); err != nil {
		t.Fatalf("SaveLogsAsync() error = %v", err)
	}

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
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./pkg/filestore/...
```

Expected: FAIL — `undefined: filestore.AppendWriter` and `undefined: filestore.AppendLogsAsync`.

- [ ] **Step 3: Write the implementation**

Replace the whole of `pkg/filestore/filestore.go` with:

```go
package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ethereum/go-ethereum/core/types"
)

// SaveLogsAsync writes logs to a file asynchronously, replacing any file
// already at filePath.
func SaveLogsAsync(ctx context.Context, logChan <-chan types.Log, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer file.Close()

	return writeLogs(ctx, logChan, file, nil)
}

// AppendWriter opens an existing NDJSON file for appending.
func AppendWriter(filePath string) (io.WriteCloser, error) {
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("error opening file for appending: %w", err)
	}

	return file, nil
}

// AppendLogsAsync writes logs to w asynchronously, keeping whatever the
// destination already holds. Logs for which skip reports true are dropped; a
// nil skip writes every log. The writer is closed before returning, including
// when the context is cancelled, so a buffered destination is always flushed.
func AppendLogsAsync(ctx context.Context, logChan <-chan types.Log, w io.WriteCloser, skip func(types.Log) bool) error {
	defer w.Close()

	return writeLogs(ctx, logChan, w, skip)
}

// writeLogs encodes logs from logChan to w as NDJSON until the channel is
// closed or the context is cancelled.
func writeLogs(ctx context.Context, logChan <-chan types.Log, w io.Writer, skip func(types.Log) bool) error {
	encoder := json.NewEncoder(w)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case logObj, ok := <-logChan:
			if !ok {
				return nil
			}

			if skip != nil && skip(logObj) {
				continue
			}

			if err := encoder.Encode(logObj); err != nil {
				return fmt.Errorf("error encoding log: %w", err)
			}
		}
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./pkg/... && go vet ./pkg/...
```

Expected: `ok` for `pkg/filestore`, `pkg/gzipstore`, and `pkg/resume`; no vet output.

- [ ] **Step 5: Commit**

```bash
git add pkg/filestore/filestore.go pkg/filestore/filestore_test.go
git commit -m "feat(filestore): add AppendLogsAsync with a skip filter"
```

---

### Task 4: Wire `--resume` into the export command

**Files:**
- Modify: `cmd/export.go`

**Interfaces:**
- Consumes: `resume.Read`, `resume.Cursor`, `Cursor.Skip` (Task 1); `gzipstore.AppendWriter` (Task 2); `filestore.AppendWriter`, `filestore.AppendLogsAsync` (Task 3).
- Produces: the `--resume` / `-r` CLI flag. Nothing consumes this task.

This task has no unit test: `RunE` needs a live RPC endpoint, and the repo has no HTTP fixture harness to build on. The logic worth testing was pushed into `pkg/` by Tasks 1–3 and is covered there. Step 5 verifies this task end to end against the real files in `dist/`.

- [ ] **Step 1: Add the flag variable and registration**

In `initExportCmd`, add `resumeFile` to the `var` block at the top:

```go
	var (
		startBlock      uint64
		endBlock        uint64
		rpcEndpoint     string
		maxRequest      int
		blockRangeLimit uint32
		outputFile      string
		compress        bool
		resumeFile      string
	)
```

And register the flag alongside the others, after the `--compress` line:

```go
	cmd.Flags().StringVarP(&resumeFile, "resume", "r", "", "Resume a previous export file (.ndjson, .gz or .gzip); overrides --start and --output")
```

- [ ] **Step 2: Read the cursor at the top of RunE**

Insert this as the first statement inside `RunE`, immediately after `ctx := cmd.Context()` and **before** `ethclient.NewClient`. Reading the cursor first means a bad path fails instantly instead of after dialing the RPC endpoint.

```go
			var cursor *resume.Cursor
			if resumeFile != "" {
				cursor, err = resume.Read(resumeFile)
				if err != nil {
					return fmt.Errorf("failed to read resume file: %w", err)
				}

				if cmd.Flags().Changed("start") {
					c.log.Warning("--start is ignored when --resume is set", "resumeFile", resumeFile)
				}
				if cmd.Flags().Changed("output") {
					c.log.Warning("--output is ignored when --resume is set, logs are appended to the resume file", "resumeFile", resumeFile)
				}
				if cursor.Compressed && compress {
					c.log.Warning("--compress is ignored when resuming an already compressed file", "resumeFile", resumeFile)
					compress = false
				}

				outputFile = resumeFile
				startBlock = cursor.BlockNumber

				c.log.Info("Resuming export",
					"resumeFile", resumeFile,
					"startBlock", startBlock,
					"lastLogIndex", cursor.LogIndex,
					"compressed", cursor.Compressed,
				)
			}
```

Then add the imports. The `import` block becomes:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	ethclient "github.com/ethersphere/batch-export/pkg/ethclientwrapper"
	"github.com/ethersphere/batch-export/pkg/eventfetcher"
	"github.com/ethersphere/batch-export/pkg/filestore"
	"github.com/ethersphere/batch-export/pkg/gzipstore"
	"github.com/ethersphere/batch-export/pkg/resume"
	"github.com/ethersphere/bee/v2/pkg/config"
	"github.com/ethersphere/bee/v2/pkg/util/abiutil"
	"github.com/spf13/cobra"
)
```

- [ ] **Step 3: Branch the writer goroutine**

Replace the existing saver goroutine — the block from `go func() {` through the closing `}()` that currently calls `filestore.SaveLogsAsync` — with:

```go
			go func() {
				defer wg.Done()

				if err := saveLogs(ctx, logChan, outputFile, cursor); err != nil {
					if errors.Is(err, context.Canceled) {
						c.log.Error(err, "context canceled while saving logs")
						return
					}
					c.log.Error(err, "error saving logs")
					return
				}
				c.log.Info("all logs have been saved", "outputFile", outputFile)
			}()
```

Then add this helper at the end of `cmd/export.go`, after `initExportCmd`:

```go
// saveLogs writes logs to outputFile. With a nil cursor it replaces the file;
// otherwise it appends to the file the cursor came from, dropping any log that
// was already written to it.
func saveLogs(ctx context.Context, logChan <-chan types.Log, outputFile string, cursor *resume.Cursor) error {
	if cursor == nil {
		return filestore.SaveLogsAsync(ctx, logChan, outputFile)
	}

	var (
		w   io.WriteCloser
		err error
	)
	if cursor.Compressed {
		w, err = gzipstore.AppendWriter(outputFile)
	} else {
		w, err = filestore.AppendWriter(outputFile)
	}
	if err != nil {
		return fmt.Errorf("error opening output file for appending: %w", err)
	}

	return filestore.AppendLogsAsync(ctx, logChan, w, cursor.Skip)
}
```

`saveLogs` takes `types.Log`, so add one more import to the block from Step 2, in the third-party group above the `batch-export` imports:

```go
	"github.com/ethereum/go-ethereum/core/types"
```

- [ ] **Step 4: Wait for the saver before returning on cancellation**

The existing `<-ctx.Done()` branch logs `"context canceled, waiting for logs to be saved..."` but returns without ever waiting, so the saver goroutine can be killed mid-write. That was survivable when every write was a lone `Encode` call; it is not survivable now, because an unflushed gzip member leaves a trailing fragment that the next `--resume` has to discard. Fix the branch to actually wait:

```go
				case <-ctx.Done():
					c.log.Info("context canceled, waiting for logs to be saved...")
					wg.Wait()
					if err := compressFunc(); err != nil {
						return errors.Join(fmt.Errorf("error compressing file: %w", err), ctx.Err())
					}
					return ctx.Err()
```

This cannot deadlock: on cancellation `writeLogs` returns from its `ctx.Done()` case immediately, and `AppendLogsAsync`'s deferred `Close` flushes the gzip member on the way out.

- [ ] **Step 5: Build and verify against the real export files**

```bash
make binary && go vet ./... && gofumpt -l cmd pkg
```

Expected: the binary builds, no vet output, no files listed by `gofumpt`.

Then confirm the flag is registered and that both file formats are read correctly. `dist/export.ndjson` ends at block `0x2db0697` (47908503), log index `0x18` (24):

```bash
./dist/batch-export export --help | grep -A1 resume

# Plain NDJSON: cursor must be block 47908503.
./dist/batch-export export -v debug --resume dist/export.ndjson --end 47908504 2>&1 | head -20

# Gzip: same cursor, read through the compressed stream.
./dist/batch-export export -v debug --resume dist/export.ndjson.gzip --end 47908504 2>&1 | head -20
```

Expected: both runs log `"Resuming export"` with `startBlock=47908503` and `lastLogIndex=24`, the second also with `compressed=true`. Both then fetch the one-block range and exit cleanly.

Verify the appended gzip is still one readable stream, and that no duplicate was written:

```bash
cp dist/export.ndjson.gzip /tmp/resume-check.gzip
BEFORE=$(gzcat /tmp/resume-check.gzip | wc -l)
./dist/batch-export export --resume /tmp/resume-check.gzip --end 47908504
gzcat /tmp/resume-check.gzip | wc -l          # >= BEFORE, and must not error
gzcat /tmp/resume-check.gzip | tail -n 3
gzcat /tmp/resume-check.gzip | sort | uniq -d | head   # must print nothing
echo "before=$BEFORE"
```

Expected: `gzcat` exits 0, the line count is at least `BEFORE`, and the duplicate check prints nothing.

Finally, confirm the flag-override warnings fire:

```bash
./dist/batch-export export --resume dist/export.ndjson --start 100 --output other.ndjson --compress --end 47908504 2>&1 | grep -i "ignored"
```

Expected: warnings for `--start` and `--output`. `--compress` is not warned about here because `dist/export.ndjson` is not compressed; re-run with `--resume dist/export.ndjson.gzip` to see that third warning.

- [ ] **Step 6: Commit**

```bash
git add cmd/export.go
git commit -m "feat(export): add --resume to continue a previous export"
```

---

### Task 5: Document the flag

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: the `--resume` flag from Task 4.
- Produces: nothing.

- [ ] **Step 1: Add a feature bullet**

In the `## Features` list, after the `Graceful shutdown on interrupt signals (Ctrl+C).` bullet, add:

```markdown
- Resume an interrupted export from an existing `.ndjson`, `.gz`, or `.gzip` file.
```

- [ ] **Step 2: Add the flag to the flag table**

In the `## Flags` block, insert the `--resume` line between `--output` and `--start` so the list stays alphabetical:

```sh
  -r, --resume string              Resume a previous export file (.ndjson, .gz or .gzip); overrides --start and --output
```

- [ ] **Step 3: Document the behavior**

After the `## Flags` code block and before the `The produced NDJSON is consumed by ...` line, add:

````markdown
### Resuming an interrupted export

Point `--resume` at a file a previous run produced. The tool reads its last
complete entry, restarts from that block, and appends to the same file:

```sh
./dist/batch-export export --resume dist/export.ndjson
```

Compressed exports work the same way and are detected by content, not by
extension, so `.gz` and `.gzip` both work:

```sh
./dist/batch-export export --resume dist/export.ndjson.gzip
```

The resumed block is re-queried, because an interrupted run may have saved only
part of it; entries already in the file are skipped, so resuming never
duplicates or drops a log. Appending to a compressed export adds a second gzip
member — standard tools such as `gzcat`, `gunzip`, and Go's `compress/gzip`
read the result as one continuous stream.

When `--resume` is set it overrides `--start` and `--output`.
````

- [ ] **Step 4: Verify the whole suite still passes**

```bash
make test && make vet && make lint
```

Expected: all `pkg/` tests pass, no vet output, no lint findings.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs: document the --resume flag"
```

---

## Self-Review

**Spec coverage** — every point of the Design Summary maps to a task:

| Design point | Task |
|---|---|
| 1. `--resume` / `-r` flag, overrides `--start` / `--output` | Task 4 Steps 1–2 |
| 2. Magic-byte format detection | Task 1 (`isGzip`) |
| 3. Last parseable line; truncated tail discarded | Task 1 (`parseCursor`, `lastCursorPlain`, `lastCursorGzip`) |
| 4. Inclusive resume block + skip already-written logs | Task 1 (`Cursor.Skip`), Task 3 (`skip` param), Task 4 Step 2 |
| 5. `O_APPEND`; gzip gets a new member | Task 2, Task 3 (`AppendWriter`), Task 4 Step 3 |
| 6. `--compress` no-op on already-gzipped input | Task 4 Step 2 |

**Type consistency** — `Cursor.Skip(types.Log) bool` (Task 1) matches the `skip func(types.Log) bool` parameter of `AppendLogsAsync` (Task 3) and the `cursor.Skip` value passed in Task 4. Both `AppendWriter` functions return `(io.WriteCloser, error)`, which is what `saveLogs` assigns into its `io.WriteCloser` variable. `SaveLogsAsync` keeps its existing three-argument signature, so its call site in Task 4 needs no change beyond moving into `saveLogs`.

**Dependency direction** — `pkg/filestore` takes a bare `func(types.Log) bool` rather than importing `pkg/resume`, so `cmd` depends on all three packages while none of them depend on each other.

**Known gaps, deliberate:**
- `cmd/export.go` has no unit test (no RPC fixture harness exists in this repo); Task 4 Step 5 covers it manually against the real `dist/` files instead.
- Resume assumes the file was produced by this tool against the same contract and chain. A file from a different chain would resume from a meaningless block. Guarding that would mean writing a header, which changes the output format that `batch-archive` consumes — out of scope here.
- A multi-member gzip is legal and read transparently by standard tooling, but is a format change in spirit. If anything in [batch-archive](https://github.com/ethersphere/batch-archive) parses gzip by hand instead of through a standard library, that is the one place to check.
