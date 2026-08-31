# `batch-export verify` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `verify` subcommand that proves a refreshed snapshot strictly extends the snapshot it was resumed from, printing the new last block number — wired into the Batch Sync workflow as a gate before publishing.

**Architecture:** `pkg/verify` compares the two files using `pkg/resume`'s existing cursor/format machinery (three small helpers get exported from `resume` first). `cmd/verify.go` is a thin cobra wrapper. The workflow gains a `Verify snapshot` step whose stdout replaces the current `gunzip | tail | sed` block-number extraction.

**Tech Stack:** Go (stdlib only — no new dependencies), cobra (already used), `github.com/ethersphere/bee/v2/pkg/log` (already used; its default sink is os.Stderr, so stdout stays clean).

**Spec:** `docs/superpowers/specs/2026-08-31-verify-snapshot-design.md`

## Global Constraints

- Module path is `github.com/ethersphere/batch-export`; work happens on branch `feat/verify-snapshot`.
- Conventional commits (`feat:`, `test:`, `ci:`, `docs:`).
- stdout of `verify` carries ONLY the decimal last block number and a newline; everything else goes to stderr via the logger. Non-zero exit = do not publish.
- Streaming with constant memory: never load a snapshot whole (they are tens of MB).
- No behavior change to `export`, `resume`, or the workflow's publish semantics beyond swapping the block-number source.
- Run tests with `go test ./...` and `go vet ./...`; both must pass before every commit.
- Comment style: sparse, constraint-stating, matching `pkg/resume` (read it first).

---

### Task 1: Export `MaxLineBytes`, `ParseEntry`, and `OpenClean` from `pkg/resume`

`pkg/verify` needs three things resume already knows: the line-length cap, how to parse one NDJSON line into `(blockNumber, logIndex)`, and how to read a file's decompressed clean content. Export them here so verify never re-encodes the on-disk format.

**Files:**
- Modify: `pkg/resume/resume.go`
- Test: `pkg/resume/resume_test.go` (append new test functions; its existing helpers `testLog`, `ndjson`, `gz`, `truncateLast`, `write`, and constants `plainFile`, `gzipFile` are reused)

**Interfaces:**
- Consumes: existing `resume.Read`, internal `parseCursor`, internal `maxLineBytes`.
- Produces (later tasks rely on these exact signatures):
  - `const MaxLineBytes = 1 << 20` (renamed from `maxLineBytes`)
  - `func ParseEntry(line []byte) (*Cursor, error)`
  - `func OpenClean(path string, c *Cursor) (io.ReadCloser, error)`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/resume/resume_test.go`:

```go
func TestParseEntry(t *testing.T) {
	t.Parallel()

	cursor, err := resume.ParseEntry(ndjson(t, testLog(7, 3)))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if cursor.BlockNumber != 7 || cursor.LogIndex != 3 {
		t.Fatalf("got (%d, %d), want (7, 3)", cursor.BlockNumber, cursor.LogIndex)
	}

	if _, err := resume.ParseEntry([]byte(`{"foo":1}`)); err == nil {
		t.Fatal("want an error for a line missing blockNumber and logIndex")
	}
}

func TestOpenClean(t *testing.T) {
	t.Parallel()

	content := ndjson(t, testLog(1, 0), testLog(2, 0))

	tests := []struct {
		name string
		file string
		blob []byte
	}{
		{nameFormatPlain, plainFile, content},
		{nameFormatGzip, gzipFile, gz(t, content)},
		{"plain with interrupted tail", plainFile, append(slices.Clone(content), `{"blockNu`...)},
		{"gzip with interrupted tail", gzipFile, truncateLast(append(gz(t, content), gz(t, ndjson(t, testLog(3, 0)))...), 4)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := write(t, tc.file, tc.blob)
			cursor, err := resume.Read(path)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}

			r, err := resume.OpenClean(path, cursor)
			if err != nil {
				t.Fatalf("OpenClean: %v", err)
			}
			defer r.Close()

			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("reading clean content: %v", err)
			}
			if !bytes.Equal(got, content) {
				t.Fatalf("clean content mismatch:\ngot  %q\nwant %q", got, content)
			}
		})
	}
}
```

Notes for the implementer: `ndjson` returns newline-terminated lines and `ParseEntry` trims whitespace, so passing a full line works. The "gzip with interrupted tail" case appends a second gzip member and cuts 4 bytes off its trailer — `Read` then reports `CleanSize` at the first member's boundary, and `OpenClean` must return only that member's content. Check the file's existing imports; `slices` and `io` are already imported.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/resume/ -run 'TestParseEntry|TestOpenClean' -v`
Expected: compile error — `undefined: resume.ParseEntry` and `undefined: resume.OpenClean`.

- [ ] **Step 3: Implement in `pkg/resume/resume.go`**

Rename the constant (find every use: `grep -n maxLineBytes pkg/resume/resume.go` — it appears only in this file) and update its comment:

```go
const (
	// MaxLineBytes caps a single line. Exported log lines run to a few
	// hundred bytes, so anything longer is not this tool's output.
	MaxLineBytes = 1 << 20
	// bufferSize is how much of a gzip file is buffered per read.
	bufferSize = 64 * 1024
)
```

Add at the end of the file:

```go
// ParseEntry builds a cursor from a single exported NDJSON line, accepting
// both the slim and the full log shape. It is the only place outside the
// writer where the on-disk field names are known, so consumers never
// re-encode them.
func ParseEntry(line []byte) (*Cursor, error) {
	return parseCursor(line)
}

// OpenClean returns the decompressed clean content of the export at path —
// the same bytes PrepareOutput would carry over. c must come from Read on
// the same, unmodified file.
func OpenClean(path string, c *Cursor) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening export file: %w", err)
	}

	limited := io.LimitReader(file, c.CleanSize)
	if !c.Compressed {
		return &cleanReader{Reader: limited, closer: file}, nil
	}

	gz, err := gzip.NewReader(bufio.NewReaderSize(limited, bufferSize))
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("error reading gzip export file: %w", err)
	}

	return &cleanReader{Reader: gz, closer: file}, nil
}

// cleanReader pairs the decompressed stream with the file it draws from.
type cleanReader struct {
	io.Reader
	closer io.Closer
}

func (r *cleanReader) Close() error { return r.closer.Close() }
```

The gzip path relies on `gzip.Reader`'s default multistream mode to walk every member inside the `CleanSize` limit; `CleanSize` always ends on a member boundary, so the limited stream ends cleanly.

- [ ] **Step 4: Run the full package tests**

Run: `go test ./pkg/resume/ -v` and `go vet ./...`
Expected: PASS (the rename must not have missed a reference; `go vet` is the catch-all).

- [ ] **Step 5: Commit**

```bash
git add pkg/resume/resume.go pkg/resume/resume_test.go
git commit -m "feat: export clean-content helpers from pkg/resume"
```

---

### Task 2: `pkg/verify` — core check, happy paths

**Files:**
- Create: `pkg/verify/verify.go`
- Create: `pkg/verify/verify_test.go`

**Interfaces:**
- Consumes: `resume.Read`, `resume.OpenClean`, `resume.ParseEntry`, `resume.MaxLineBytes`, `resume.Cursor` (Task 1).
- Produces (Tasks 3–5 rely on these exact names):
  - `type Result struct { LastBlock uint64; Appended int; OldTruncated bool }`
  - `func Verify(oldPath, newPath string) (Result, error)`
  - `var ErrMismatch, ErrOrder, ErrTruncatedNew error`

- [ ] **Step 1: Write the failing tests**

Create `pkg/verify/verify_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/verify/ -v`
Expected: compile error — package `verify` does not exist.

- [ ] **Step 3: Implement `pkg/verify/verify.go`**

```go
// Package verify checks that a refreshed snapshot is a strict extension of
// the snapshot it was resumed from: everything the old file held, byte for
// byte and in order, followed only by newer entries.
package verify

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/ethersphere/batch-export/pkg/resume"
)

var (
	// ErrMismatch indicates the new snapshot does not begin with the old
	// snapshot's content: an entry was dropped, mutated, or reordered.
	ErrMismatch = errors.New("new snapshot does not extend the old snapshot")
	// ErrOrder indicates an appended entry out of (blockNumber, logIndex)
	// order relative to what precedes it.
	ErrOrder = errors.New("appended entries out of order")
	// ErrTruncatedNew indicates the new snapshot ends in an interrupted
	// write; a completed export never does.
	ErrTruncatedNew = errors.New("new snapshot ends in an interrupted write")
)

// chunkSize is how many bytes of both snapshots are compared per read.
const chunkSize = 64 * 1024

// Result reports what a successful verification established.
type Result struct {
	// LastBlock is the block number of the new snapshot's final entry.
	LastBlock uint64
	// Appended is how many entries the new snapshot adds.
	Appended int
	// OldTruncated reports whether the old snapshot carried an interrupted
	// tail, excluded from the comparison the way resuming excludes it from
	// the copy.
	OldTruncated bool
}

// Verify checks that the snapshot at newPath extends the one at oldPath.
func Verify(oldPath, newPath string) (Result, error) {
	oldCursor, err := resume.Read(oldPath)
	if err != nil {
		return Result{}, fmt.Errorf("old snapshot: %w", err)
	}
	newCursor, err := resume.Read(newPath)
	if err != nil {
		return Result{}, fmt.Errorf("new snapshot: %w", err)
	}
	if newCursor.Truncated {
		return Result{}, ErrTruncatedNew
	}

	oldClean, err := resume.OpenClean(oldPath, oldCursor)
	if err != nil {
		return Result{}, fmt.Errorf("old snapshot: %w", err)
	}
	defer oldClean.Close()

	newClean, err := resume.OpenClean(newPath, newCursor)
	if err != nil {
		return Result{}, fmt.Errorf("new snapshot: %w", err)
	}
	defer newClean.Close()

	newBuffered := bufio.NewReaderSize(newClean, chunkSize)
	if err := comparePrefix(oldClean, newBuffered); err != nil {
		return Result{}, err
	}

	appended, err := checkTail(newBuffered, oldCursor)
	if err != nil {
		return Result{}, err
	}

	return Result{
		LastBlock:    newCursor.BlockNumber,
		Appended:     appended,
		OldTruncated: oldCursor.Truncated,
	}, nil
}

// comparePrefix requires every byte of oldR to appear at the start of newR.
func comparePrefix(oldR io.Reader, newR *bufio.Reader) error {
	oldBuf := make([]byte, chunkSize)
	newBuf := make([]byte, chunkSize)

	var offset int64
	for {
		n, err := oldR.Read(oldBuf)
		if n > 0 {
			if _, rerr := io.ReadFull(newR, newBuf[:n]); rerr != nil {
				return fmt.Errorf("%w: new snapshot ends at byte %d of the old content", ErrMismatch, offset)
			}
			if !bytes.Equal(oldBuf[:n], newBuf[:n]) {
				return fmt.Errorf("%w: content diverges within bytes %d..%d of the old content", ErrMismatch, offset, offset+int64(n))
			}
			offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("error reading old snapshot: %w", err)
		}
	}
}

// checkTail walks the entries the new snapshot appends after the old
// content, requiring each to advance the (blockNumber, logIndex) order.
func checkTail(r *bufio.Reader, prev *resume.Cursor) (int, error) {
	last := *prev

	var (
		appended int
		lineNum  int
		line     []byte
	)
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > resume.MaxLineBytes {
			return 0, fmt.Errorf("appended line %d exceeds %d bytes", lineNum+1, resume.MaxLineBytes)
		}

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				return 0, fmt.Errorf("appended line %d ends without a newline", lineNum+1)
			}
			return appended, nil
		case err != nil:
			return 0, fmt.Errorf("error reading new snapshot: %w", err)
		}

		lineNum++
		entry, err := resume.ParseEntry(line)
		if err != nil {
			return 0, fmt.Errorf("appended line %d is not a log entry: %w", lineNum, err)
		}
		if !after(entry, &last) {
			return 0, fmt.Errorf("%w: appended line %d holds (block %d, log %d) after (block %d, log %d)",
				ErrOrder, lineNum, entry.BlockNumber, entry.LogIndex, last.BlockNumber, last.LogIndex)
		}
		last = *entry
		appended++
		line = line[:0]
	}
}

// after reports whether entry strictly follows prev in log order.
func after(entry, prev *resume.Cursor) bool {
	if entry.BlockNumber != prev.BlockNumber {
		return entry.BlockNumber > prev.BlockNumber
	}
	return entry.LogIndex > prev.LogIndex
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/verify/ -v` and `go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/verify/
git commit -m "feat: add pkg/verify snapshot extension check"
```

---

### Task 3: `pkg/verify` — failure modes and truncation handling

The implementation from Task 2 should already handle these; this task pins each failure mode with a test and fixes anything that surfaces.

**Files:**
- Modify: `pkg/verify/verify_test.go` (append)
- Modify: `pkg/verify/verify.go` (only if a test exposes a gap)

**Interfaces:**
- Consumes: everything Task 2 produced, plus `resume.ErrNoLogs`.

- [ ] **Step 1: Write the tests**

Append to `pkg/verify/verify_test.go` (add `"slices"`, `"errors"`, and `"github.com/ethersphere/batch-export/pkg/resume"` to the imports):

```go
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
```

- [ ] **Step 2: Run the tests**

Run: `go test ./pkg/verify/ -v`
Expected: PASS if Task 2's implementation is complete. Any FAIL marks a real gap — fix `verify.go` minimally until green, and keep the fix inside the semantics the spec defines.

- [ ] **Step 3: Commit**

```bash
git add pkg/verify/
git commit -m "test: cover verify failure modes and truncation handling"
```

---

### Task 4: `pkg/verify` — format combination matrix

**Files:**
- Modify: `pkg/verify/verify_test.go` (append)

**Interfaces:**
- Consumes: everything from Tasks 2–3.

- [ ] **Step 1: Write the test**

```go
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
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/verify/ -v`
Expected: PASS (the format handling all lives in `resume`; a FAIL here means `OpenClean` or the buffering broke a combination — fix there, not with special cases in verify).

- [ ] **Step 3: Commit**

```bash
git add pkg/verify/verify_test.go
git commit -m "test: cover verify across plain and gzip snapshot combinations"
```

---

### Task 5: `verify` subcommand and README

**Files:**
- Create: `cmd/verify.go`
- Create: `cmd/verify_test.go`
- Modify: `cmd/cmd.go` (register the command in `newCommand`)
- Modify: `README.md` (new subsection after "Continuing a previous snapshot")

**Interfaces:**
- Consumes: `verify.Verify`, `verify.Result` (Task 2); the `command` struct and `c.log` from `cmd/cmd.go`.
- Produces: `batch-export verify --old <file> --new <file>` printing the last block in decimal on stdout; `func (c *command) initVerifyCmd() error`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/verify_test.go` (internal test — `package cmd`, like `export_test.go`):

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/ -run TestVerifyCmd -v`
Expected: FAIL — cobra reports `unknown command "verify"` (surfaced as an `Execute` error in the first test).

- [ ] **Step 3: Implement `cmd/verify.go` and register it**

Create `cmd/verify.go`:

```go
package cmd

import (
	"fmt"

	"github.com/ethersphere/batch-export/pkg/verify"
	"github.com/spf13/cobra"
)

func (c *command) initVerifyCmd() error {
	var (
		oldFile string
		newFile string
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify that a refreshed snapshot extends the one it was resumed from",
		Long: `Verifies that the snapshot at --new holds everything the snapshot at --old does,
byte for byte and in order, followed only by newer entries. On success the new
snapshot's last block number is printed to stdout in decimal; any failure exits
non-zero with nothing on stdout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := verify.Verify(oldFile, newFile)
			if err != nil {
				return err
			}

			if result.OldTruncated {
				c.log.Warning("old snapshot ends in an interrupted write; the truncated tail was excluded from the comparison", "oldFile", oldFile)
			}
			c.log.Info("snapshot verified", "oldFile", oldFile, "newFile", newFile, "appended", result.Appended, "lastBlock", result.LastBlock)

			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.LastBlock)
			return err
		},
	}

	cmd.Flags().StringVar(&oldFile, "old", "", "Snapshot the export resumed from (.ndjson, .gz or .gzip)")
	cmd.Flags().StringVar(&newFile, "new", "", "Freshly exported snapshot to check (.ndjson, .gz or .gzip)")
	for _, name := range []string{"old", "new"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			return err
		}
	}

	c.root.AddCommand(cmd)

	return nil
}
```

In `cmd/cmd.go`, register it right after the export command:

```go
	if err := c.initExportCmd(); err != nil {
		return nil, err
	}

	if err := c.initVerifyCmd(); err != nil {
		return nil, err
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/ -v` and `go test ./...` and `go vet ./...`
Expected: PASS.

- [ ] **Step 5: Add the README section**

In `README.md`, directly after the "Continuing a previous snapshot" section's content (before the next `##` heading), add:

```markdown
### Verifying a snapshot

`verify` proves a refreshed snapshot still holds everything the original did —
byte for byte, in order — followed only by newer entries, and prints the new
snapshot's last block number in decimal:

```sh
batch-export verify --old export.ndjson.gzip --new snapshot.ndjson.gzip
```

A non-zero exit means the new snapshot must not replace the old one.
```

- [ ] **Step 6: Commit**

```bash
git add cmd/verify.go cmd/verify_test.go cmd/cmd.go README.md
git commit -m "feat: add verify command gating snapshot publication"
```

---

### Task 6: Workflow wiring and PR

**Files:**
- Modify: `.github/workflows/batch-sync.yml`

**Interfaces:**
- Consumes: the `verify` subcommand's stdout contract (Task 5); the workflow's existing `resume.ndjson.gzip` / `snapshot.ndjson.gzip` step outputs.
- Produces: `LAST_BLOCK` in `GITHUB_ENV`, consumed by the Publish step.

- [ ] **Step 1: Add the Verify step**

In `.github/workflows/batch-sync.yml`, insert between the `Export` and `Publish to batch-archive` steps:

```yaml
      # A failed verification stops the job before any commit or tag exists.
      - name: Verify snapshot
        run: |
          set -euo pipefail
          last_block="$(./dist/batch-export verify --old resume.ndjson.gzip --new snapshot.ndjson.gzip)"
          echo "LAST_BLOCK=${last_block}" >> "${GITHUB_ENV}"
```

- [ ] **Step 2: Replace the sed extraction in the Publish step**

Delete this block from `Publish to batch-archive`:

```yaml
          # blockNumber is hex ("0x...") in the slim NDJSON; the commit title
          # uses decimal, matching the archive's history.
          last_block_hex="$(gunzip -c archive/export.ndjson.gzip | tail -n 1 \
            | sed -nE 's/.*"blockNumber":"(0x[0-9a-fA-F]+)".*/\1/p')"
          if [ -z "${last_block_hex}" ]; then
            echo "::error::could not read blockNumber from the last snapshot entry"
            exit 1
          fi
          last_block="$(printf '%d' "${last_block_hex}")"
```

and change both remaining uses of `${last_block}` to `${LAST_BLOCK}` — one in `git commit -m "chore: update snapshot to block number ${last_block}"`, one in the final `::notice::published snapshot at block ${last_block} ...` line.

- [ ] **Step 3: Validate**

Run:
```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/batch-sync.yml')); print('YAML OK')"
actionlint .github/workflows/batch-sync.yml || true   # only the known custom runner-label warning is acceptable
grep -n "last_block" .github/workflows/batch-sync.yml # expect: only the Verify step's local variable, no stale lowercase uses in Publish
```
Expected: YAML OK; no actionlint findings beyond the `bee` runner label; grep shows no `${last_block}` left in the Publish step.

- [ ] **Step 4: Full check and commit**

Run: `go test ./...` and `go vet ./...`
Expected: PASS.

```bash
git add .github/workflows/batch-sync.yml
git commit -m "ci: gate publishing on snapshot verification"
```

- [ ] **Step 5: Push and open the PR**

```bash
git push -u origin feat/verify-snapshot
gh pr create --title "feat: verify refreshed snapshots before publishing" --body "$(cat <<'EOF'
Adds a `verify` subcommand and wires it into the Batch Sync workflow as a regression gate before any commit or tag reaches batch-archive.

## What it does

- `batch-export verify --old <file> --new <file>` proves the new snapshot strictly extends the old one: the old content must appear byte for byte at the start of the new file, and every appended entry must parse and advance the (blockNumber, logIndex) order. On success it prints the new last block number in decimal on stdout; any failure exits non-zero.
- `pkg/verify` builds entirely on `pkg/resume`'s existing cursor and format handling; `resume` newly exports `MaxLineBytes`, `ParseEntry`, and `OpenClean` so no consumer re-encodes the on-disk format.
- The workflow runs `verify` between Export and Publish. Its stdout replaces the previous `gunzip | tail | sed` block-number extraction, so the commit title's block number now comes from the same Go code that defines the snapshot format.

Spec: `docs/superpowers/specs/2026-08-31-verify-snapshot-design.md`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```
