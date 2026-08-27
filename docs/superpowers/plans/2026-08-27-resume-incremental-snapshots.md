# Resume as Incremental Snapshots Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the existing `feat/resume-flag` branch into conformance with the incremental-snapshots spec: strict tail validation, `--resume`/`--output` composition with copy mode, saver errors reaching the exit code, and docs reframed around continuation.

**Architecture:** This plan **edits the existing branch**, it does not start over. `pkg/resume` keeps its public shape (`Cursor`, `Read`, `Skip`) but its internals simplify: the lenient multi-window backward walk becomes a single tail read that refuses foreign content, and the gzip member walk refuses non-log lines instead of tolerating them. A new `resume.PrepareOutput` enforces the `CleanSize` invariant (in-place truncate or clean-prefix copy) next to where it is computed. `cmd/export.go` gains mode resolution and a saver-error path to `RunE`'s return value, keeping its existing select-loop shape.

**Tech Stack:** Go 1.25, stdlib (`compress/gzip`, `compress/flate`, `bufio`, `path/filepath`), `github.com/ethereum/go-ethereum` v1.15.11, `github.com/ethersphere/bee/v2` v2.7.0, cobra.

**Spec:** `docs/superpowers/specs/2026-08-27-resume-input-output-design.md` — the plan argues from it; read both. Section references (§N) below point into it.

**Baseline:** branch `feat/resume-flag`, code at commit `b53a9c3` (plus the local spec commit). All file:line references are against that state.

## Global Constraints

- Go `1.25` per `go.mod`; do not change it. **No new dependencies**; do not run `go get`.
- Lint: `.golangci.yml` enables `copyloopvar`, `errname`, `errorlint`, `goconst`, `misspell`, `nilerr`, `unconvert` + `gofmt`/`gofumpt`. Wrap with `%w`; compare with `errors.Is`/`errors.As`, never `==`.
- Doc comment on every exported identifier, starting with its name. Error strings lowercase, unpunctuated.
- **Review-friendliness is a requirement, not a preference:** keep the diff against `main` as small as the spec allows; extend existing structures instead of restructuring them; keep `RunE`'s select-loop shape; never reformat code you are not changing. Where minimal-diff and clean idiomatic Go conflict, prefer clean Go — but say so in the commit message.
- Dependency direction stays one-way: `cmd` → {`resume`, `filestore`, `gzipstore`}; none of the three import each other.
- Conventional Commits. **Do not push** — local commits only; the branch is squash-merged at the end (§11).
- `dist/` holds the user's real export archives. Never point `--resume` (or any truncating/copying code) at a `dist/` path; test against copies in a temp dir only.
- Tests: `make test` (= `go test -v ./pkg/...`); also run `go test -race ./pkg/...` before each commit that touches concurrency.

## File Structure

| File | Change | Responsibility after this plan |
|---|---|---|
| `pkg/resume/resume.go` | Rewrite internals | Strict tail validation (§5): `Read` + two sentinel errors; single-read plain path; member-walk gzip path; `PrepareOutput` (§4/§9). |
| `pkg/resume/resume_test.go` | Overhaul | Validation, refusal, tolerance, and cross-package round-trip tests for both modes. |
| `pkg/filestore/filestore.go` | Shrink | `CreateWriter`, `AppendWriter`, `AppendLogsAsync` only; `SaveLogsAsync` deleted (§9). |
| `pkg/filestore/filestore_test.go` | Retool | Seeding via `CreateWriter`+`AppendLogsAsync`. |
| `cmd/export.go` | Edit | Mode resolution (§3), `PrepareOutput` wiring, saver-error plumbing (§7), flag help. |
| `README.md` | Rewrite section | §8. |
| `pkg/gzipstore/*` | Untouched | — |

---

### Task 1: Strict tail validation in `pkg/resume`

**Files:**
- Modify: `pkg/resume/resume.go` (all of `lastCursorPlain`, `lastCursorGzip`, `scanLines`, the const/var blocks, `parseCursor`; `Read`, `Cursor`, `Skip`, `isGzip`, `countingReader` keep their shape)
- Test: `pkg/resume/resume_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (Task 2 and 4 rely on these):
  - `var ErrNotAnExport error`, `var ErrNoLogs error` (sentinel; `ErrNoCleanBoundary` and `errLineTooLong` are deleted)
  - `Read(path string) (*Cursor, error)` — unchanged signature; new contract per §5.
  - `Cursor` fields unchanged: `BlockNumber uint64; LogIndex uint; Compressed bool; CleanSize int64; Truncated bool`.

- [ ] **Step 1: Update the tests to the strict contract**

In `pkg/resume/resume_test.go`:

**(a) Delete** the now-obsolete lenient machinery in `TestReadCursor`'s setup: the `garbageLines` variable, the whole `straddle*` block (from the `// straddle places a valid log line…` comment through the `t.Fatalf("test setup: boundary %d…")` guard), and the `windowSize` test constant with its comment near the top of the file. Keep `threeLogs`, `manyLogs`/`many`, `twoLogsEnd`, `threeLogsEnd`, and all helpers (`testLog`, `ndjson`, `gz`, `truncateLast`, `write`).

**(b) Delete** these `TestReadCursor` cases (their content is foreign under §5 and moves to the refusal table): `"line missing blockNumber is skipped"`, `"walks back across windows of garbage lines"`, `"valid line straddles a window boundary"`.

**(c) Rename** the case `"spans multiple backward read windows"` to `"many logs"` (the window concept no longer exists; the case stays as a large-file regression).

**(d) Add** one `TestReadCursor` case — an empty trailing member is the tool's own output (a resume that fetched nothing) and must stay valid:

```go
		{
			name:           "empty trailing gzip member is valid",
			content:        append(gz(t, threeLogs), gz(t, nil)...),
			wantBlock:      102,
			wantIndex:      7,
			wantCompressed: true,
		},
```

(`gz(t, nil)` compresses zero bytes into a complete member; `bytes.Buffer.Write(nil)` is a no-op, so the existing helper handles it.)

All remaining `TestReadCursor` cases keep their current expectations — including `"truncated trailing line is excluded from the clean boundary"`, `"trailing line without a newline is not a clean end"` (cursor falls back to `{101,1}`), both truncated-member gzip cases, and the unflushed-header case. They are the §5 tolerated irregularities.

**(e) Replace** `TestReadCursorErrors` and `TestReadRefusesWithoutACleanBoundary` with one consolidated table (delete both, add this):

```go
// TestReadRefusals covers §5's strict contract: the only irregularity
// tolerated is the tool's own interrupted final write. Content the tool never
// writes is ErrNotAnExport; a file consistent with tool output but holding no
// complete entry is ErrNoLogs.
func TestReadRefusals(t *testing.T) {
	t.Parallel()

	logs := ndjson(t, testLog(100, 0), testLog(101, 0))

	tests := []struct {
		name    string
		content []byte
		wantErr error
	}{
		{
			name:    "empty file",
			content: []byte{},
			wantErr: resume.ErrNoLogs,
		},
		{
			name:    "plain file holding one unterminated line",
			content: bytes.TrimSuffix(ndjson(t, testLog(100, 0)), []byte("\n")),
			wantErr: resume.ErrNoLogs,
		},
		{
			// The tool never writes a blank line.
			name:    "only a newline",
			content: []byte("\n"),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "trailing blank line after valid logs",
			content: append(append([]byte{}, logs...), '\n'),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// A complete line that is not a log entry means the file was
			// altered after export; refusing beats guessing what to cut.
			name:    "trailing non-log line after valid logs",
			content: append(append([]byte{}, logs...), []byte("not-json\n")...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "trailing log line missing blockNumber",
			content: append(append([]byte{}, logs...), []byte("{\"address\":\"0x1\",\"topics\":[],\"data\":\"0x\"}\n")...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "only garbage lines",
			content: bytes.Repeat([]byte("not-json\n"), 10),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// Finding #4's shape: refused from a single tail read, no
			// backward scan through the junk.
			name:    "newline-free tail longer than a line can be",
			content: append(append([]byte{}, logs...), bytes.Repeat([]byte("x"), 1<<20+1)...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "single unterminated line larger than the cap",
			content: bytes.Repeat([]byte("x"), 2<<20),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// A cleanly terminated member whose content stops mid-line
			// cannot come from this tool: members hold whole lines, and an
			// interrupted write cannot produce a valid trailer.
			name:    "gzip member ending mid line",
			content: gz(t, bytes.TrimSuffix(logs, []byte("\n"))),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// Finding #6's shape: foreign data concatenated as its own valid
			// member. Refused rather than treated as a removable tail.
			name:    "gzip junk member after valid member",
			content: append(gz(t, logs), gz(t, []byte("not-json\nalso-not\n"))...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "gzip member holding a non-log line between logs",
			content: gz(t, append(append([]byte{}, logs...), []byte("not-json\n")...)),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// A sole member without its trailer is an interrupted first
			// write: nothing to resume from, so a fresh export is the remedy.
			name:    "gzip with a single truncated member",
			content: truncateLast(gz(t, logs), 6),
			wantErr: resume.ErrNoLogs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := resume.Read(write(t, plainFile, tt.content)); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Read() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
```

**(f) Keep unchanged:** `TestReadMissingFile`, `TestCursorSkip`, `TestReadReportsGzipReadErrors`, `TestGzipCleanSizeIsAMemberBoundary`, `TestAppendResumeRoundTrip`, `TestResumeAfterInterruptedWrite` (Task 2 touches the last one).

- [ ] **Step 2: Run the tests to verify the new ones fail**

Run: `go test ./pkg/resume/ -run 'TestReadRefusals|TestReadCursor' -v 2>&1 | tail -30`
Expected: compile error first (`resume.ErrNotAnExport` undefined, `resume.ErrNoCleanBoundary` still referenced only if a stray use remains); after the file compiles, `TestReadRefusals` cases like `"trailing non-log line after valid logs"` FAIL against the lenient implementation (it returns a cursor instead of refusing).

- [ ] **Step 3: Rewrite the validation internals**

In `pkg/resume/resume.go`:

**(a) Constants** — delete `windowSize`; keep `maxLineBytes` and `bufferSize`:

```go
const (
	// maxLineBytes caps a single line. Exported log lines run to a few
	// hundred bytes, so anything longer is not this tool's output.
	maxLineBytes = 1 << 20
	// bufferSize is how much of a gzip file is buffered per read.
	bufferSize = 64 * 1024
)
```

**(b) Errors** — replace the var block (deletes `ErrNoCleanBoundary` and `errLineTooLong`):

```go
var (
	// ErrNotAnExport indicates content this tool never writes. The file was
	// altered after export, so resuming it is refused rather than repaired.
	ErrNotAnExport = errors.New("not an untouched batch-export file")
	// ErrNoLogs indicates a file consistent with tool output that holds no
	// complete entry to resume from: it is empty, or holds only an
	// interrupted first write. The remedy is a fresh export.
	ErrNoLogs = errors.New("no complete log entry found")
)
```

**(c) `lastCursorPlain`** — replace entirely:

```go
// lastCursorPlain validates the tail of a plain NDJSON file and returns a
// cursor for its last complete line. The tool writes one entry per line in a
// single call, so only the tail needs examining: the last newline-terminated
// line must parse as a log entry, and the only bytes allowed after it are a
// single interrupted write — a trailing fragment with no newline.
func lastCursorPlain(file *os.File, size int64) (*Cursor, error) {
	window := min(size, 2*maxLineBytes)
	offset := size - window

	buf := make([]byte, window)
	if _, err := file.ReadAt(buf, offset); err != nil {
		return nil, fmt.Errorf("error reading resume file: %w", err)
	}

	nl := bytes.LastIndexByte(buf, '\n')
	if nl < 0 {
		// No newline at all: an interrupted first write, unless the file is
		// longer than any single line the tool writes.
		if size > maxLineBytes {
			return nil, fmt.Errorf("%w: %d bytes without a newline", ErrNotAnExport, size)
		}
		return nil, ErrNoLogs
	}
	if tail := window - int64(nl) - 1; tail > maxLineBytes {
		return nil, fmt.Errorf("%w: %d bytes without a newline after offset %d", ErrNotAnExport, tail, offset+int64(nl)+1)
	}

	start := bytes.LastIndexByte(buf[:nl], '\n') + 1
	if start == 0 && offset > 0 {
		return nil, fmt.Errorf("%w: final line is over %d bytes long", ErrNotAnExport, nl)
	}
	cursor, err := parseCursor(buf[start:nl])
	if err != nil {
		return nil, fmt.Errorf("%w: final complete line at offset %d is not a log entry: %v", ErrNotAnExport, offset+int64(start), err)
	}
	cursor.CleanSize = offset + int64(nl) + 1

	return cursor, nil
}
```

**(d) `lastCursorGzip`** — replace entirely (drops cross-member line carry; members hold whole lines by contract):

```go
// lastCursorGzip walks a gzip file one member at a time and returns a cursor
// for the last entry inside the cleanly terminated prefix. Gzip cannot be
// seeked, so the whole stream is decompressed; every complete line is
// validated on the way. A member cut short by an interrupted write ends the
// walk: CleanSize stays at the last clean member boundary, and the truncated
// member's content — about to be discarded and re-fetched — never advances
// the cursor.
func lastCursorGzip(file *os.File) (*Cursor, error) {
	counter := &countingReader{reader: bufio.NewReaderSize(file, bufferSize)}

	reader, err := gzip.NewReader(counter)
	if err != nil {
		return nil, fmt.Errorf("error opening gzip resume file: %w", err)
	}
	defer reader.Close()

	var (
		cursor    *Cursor
		cleanSize int64
		sawClean  bool
	)
	for {
		// Reset turns multistream back on, so it must be switched off per
		// member, not just for the first.
		reader.Multistream(false)

		last, err := scanMember(reader)
		switch {
		case errors.Is(err, ErrNotAnExport):
			return nil, err
		case err != nil && !truncationShaped(err):
			return nil, fmt.Errorf("error reading gzip resume file: %w", err)
		case err != nil:
			// The member never got its trailer: an interrupted final write.
			return gzipResult(cursor, cleanSize, sawClean)
		}

		if last != nil {
			cursor = last
		}
		cleanSize, sawClean = counter.read, true

		// A clean end of file makes Reset report io.EOF; a partly written
		// next member fails to parse as a header and also ends the walk.
		if err := reader.Reset(counter); err != nil {
			if errors.Is(err, io.EOF) || truncationShaped(err) {
				return gzipResult(cursor, cleanSize, sawClean)
			}
			return nil, fmt.Errorf("error reading gzip resume file: %w", err)
		}
	}
}

// gzipResult finalizes the walk: a file with no clean member, or none holding
// an entry, has nothing to resume from.
func gzipResult(cursor *Cursor, cleanSize int64, sawClean bool) (*Cursor, error) {
	if !sawClean || cursor == nil {
		return nil, ErrNoLogs
	}
	cursor.CleanSize = cleanSize

	return cursor, nil
}

// truncationShaped reports whether err is what an interrupted write produces,
// as opposed to a real read failure that must not be mistaken for one.
func truncationShaped(err error) bool {
	var corrupt flate.CorruptInputError

	return errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, gzip.ErrHeader) ||
		errors.Is(err, gzip.ErrChecksum) ||
		errors.As(err, &corrupt)
}
```

Add `"compress/flate"` to the import block.

**(e) `scanLines`** — replace with `scanMember` (no carry parameter):

```go
// scanMember reads one gzip member's NDJSON and returns a cursor for its last
// entry, nil when the member is empty. A nil error means the member decoded
// to a clean end of stream on a line boundary. The tool writes members
// holding whole log lines only, so a complete line that does not parse, or a
// clean member ending mid-line, is foreign content (ErrNotAnExport); any
// other read error is returned as-is for the caller to classify.
func scanMember(r io.Reader) (*Cursor, error) {
	buffered := bufio.NewReaderSize(r, bufferSize)

	var (
		last *Cursor
		line []byte
	)
	for {
		chunk, err := buffered.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return nil, fmt.Errorf("%w: line exceeds %d bytes", ErrNotAnExport, maxLineBytes)
		}

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				return nil, fmt.Errorf("%w: gzip member ends mid-line", ErrNotAnExport)
			}
			return last, nil
		case err != nil:
			return last, err
		}

		cursor, err := parseCursor(line)
		if err != nil {
			return nil, fmt.Errorf("%w: line is not a log entry: %v", ErrNotAnExport, err)
		}
		last = cursor
		line = line[:0]
	}
}
```

**(f) `parseCursor`** — return descriptive plain errors (callers wrap with the sentinel):

```go
// parseCursor builds a cursor from a single NDJSON line. Callers wrap the
// returned error with the sentinel that fits their context.
func parseCursor(line []byte) (*Cursor, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, errors.New("blank line")
	}

	var parsed cursorLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return nil, err
	}
	if parsed.BlockNumber == nil || parsed.LogIndex == nil {
		return nil, errors.New("missing blockNumber or logIndex")
	}

	return &Cursor{
		BlockNumber: uint64(*parsed.BlockNumber),
		LogIndex:    uint(*parsed.LogIndex),
	}, nil
}
```

`Read`, `Cursor`, `Skip`, `isGzip`, `countingReader` stay as they are (`Read` already computes `Truncated = CleanSize < size` centrally).

- [ ] **Step 4: Run the package tests**

Run: `go test ./pkg/resume/... && go vet ./pkg/resume/... && gofumpt -l pkg/resume`
Expected: PASS, no vet output, no files listed. If `TestAppendResumeRoundTrip` or the interrupted-write test fails, the strict path broke a tolerated case — fix the implementation, not the test.

- [ ] **Step 5: Commit**

```bash
git add pkg/resume/resume.go pkg/resume/resume_test.go
git commit -m "feat(resume): validate strictly, refuse files this tool did not write"
```

---

### Task 2: `resume.PrepareOutput` — enforce the clean boundary

**Files:**
- Modify: `pkg/resume/resume.go` (append after `Skip`)
- Test: `pkg/resume/resume_test.go`

**Interfaces:**
- Consumes: `Cursor` from Task 1.
- Produces (Task 4 relies on this): `func PrepareOutput(c *Cursor, inputPath, outputPath string) (discarded int64, err error)`.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/resume/resume_test.go` (imports of `filestore`/`gzipstore` and the helpers `appendWriter`, `feed`, `logsIn`, `ids` already exist):

```go
// TestCopyResumeRoundTrip is the spec's recommended workflow (§4): resume an
// archived snapshot into a NEW file. The input must come out byte-identical;
// the output must hold the input's content plus exactly the new entries and
// be itself resumable.
func TestCopyResumeRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		compressed bool
	}{
		{name: "plain ndjson"},
		{name: "gzip", compressed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			original := []types.Log{testLog(100, 0), testLog(101, 0), testLog(102, 0), testLog(102, 1)}
			boundaryHigher := testLog(102, 2)
			newer := []types.Log{testLog(103, 0), testLog(104, 0)}

			inputBytes := ndjson(t, original...)
			if tt.compressed {
				inputBytes = gz(t, inputBytes)
			}
			dir := t.TempDir()
			input := filepath.Join(dir, "prev.snapshot")
			output := filepath.Join(dir, "next.snapshot")
			if err := os.WriteFile(input, inputBytes, 0o644); err != nil {
				t.Fatalf("write input: %v", err)
			}

			cursor, err := resume.Read(input)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}

			discarded, err := resume.PrepareOutput(cursor, input, output)
			if err != nil {
				t.Fatalf("PrepareOutput() error = %v", err)
			}
			if discarded != 0 {
				t.Errorf("discarded = %d, want 0 for a clean input", discarded)
			}

			w := appendWriter(t, output, cursor)
			replay := []types.Log{testLog(102, 0), testLog(102, 1), boundaryHigher}
			replay = append(replay, newer...)
			if err := filestore.AppendLogsAsync(t.Context(), feed(replay...), w, cursor.Skip); err != nil {
				t.Fatalf("AppendLogsAsync() error = %v", err)
			}

			// The input is untouched, byte for byte.
			gotInput, err := os.ReadFile(input)
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			if !bytes.Equal(gotInput, inputBytes) {
				t.Fatal("input file was modified by a copy-mode resume")
			}

			// The output begins with the input's exact bytes (raw prefix
			// copy, no recompression) and holds the full sequence.
			gotOutput, err := os.ReadFile(output)
			if err != nil {
				t.Fatalf("read output: %v", err)
			}
			if len(gotOutput) < len(inputBytes) || !bytes.Equal(gotOutput[:len(inputBytes)], inputBytes) {
				t.Fatal("input bytes are not an unchanged prefix of the output")
			}
			all := append(append(append([]types.Log{}, original...), boundaryHigher), newer...)
			if got, want := ids(logsIn(t, output)), ids(all); !slices.Equal(got, want) {
				t.Fatalf("output logs = %v, want %v", got, want)
			}

			cursor2, err := resume.Read(output)
			if err != nil {
				t.Fatalf("Read(output) error = %v", err)
			}
			if cursor2.BlockNumber != 104 || cursor2.LogIndex != 0 || cursor2.Truncated {
				t.Errorf("output cursor = {%d,%d,truncated=%t}, want {104,0,false}", cursor2.BlockNumber, cursor2.LogIndex, cursor2.Truncated)
			}
		})
	}
}

// TestCopyResumeFromInterruptedInput: copy mode never repairs the input — the
// interrupted tail stays in it — while the output gets only clean content
// plus the re-fetched entries.
func TestCopyResumeFromInterruptedInput(t *testing.T) {
	t.Parallel()

	saved := ndjson(t, testLog(100, 0), testLog(101, 0))

	tests := []struct {
		name       string
		content    []byte
		compressed bool
	}{
		{
			name:    "plain with a truncated last line",
			content: append(append([]byte{}, saved...), []byte(`{"address":"0x45a15`)...),
		},
		{
			name:       "gzip with a truncated final member",
			content:    append(gz(t, saved), truncateLast(gz(t, ndjson(t, testLog(102, 0))), 6)...),
			compressed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			input := filepath.Join(dir, "prev.snapshot")
			output := filepath.Join(dir, "next.snapshot")
			if err := os.WriteFile(input, tt.content, 0o644); err != nil {
				t.Fatalf("write input: %v", err)
			}

			cursor, err := resume.Read(input)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if !cursor.Truncated {
				t.Fatal("test setup: input should be truncated")
			}

			discarded, err := resume.PrepareOutput(cursor, input, output)
			if err != nil {
				t.Fatalf("PrepareOutput() error = %v", err)
			}
			if want := int64(len(tt.content)) - cursor.CleanSize; discarded != want {
				t.Errorf("discarded = %d, want %d", discarded, want)
			}

			w := appendWriter(t, output, cursor)
			replay := []types.Log{testLog(101, 0), testLog(102, 0), testLog(103, 0)}
			if err := filestore.AppendLogsAsync(t.Context(), feed(replay...), w, cursor.Skip); err != nil {
				t.Fatalf("AppendLogsAsync() error = %v", err)
			}

			gotInput, err := os.ReadFile(input)
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			if !bytes.Equal(gotInput, tt.content) {
				t.Fatal("input was modified, interrupted tail included it must stay")
			}

			want := []types.Log{testLog(100, 0), testLog(101, 0), testLog(102, 0), testLog(103, 0)}
			if got := ids(logsIn(t, output)); !slices.Equal(got, ids(want)) {
				t.Fatalf("output logs = %v, want %v", got, ids(want))
			}
		})
	}
}
```

Add `"path/filepath"` and `"slices"` to the test file's imports if not present.

- [ ] **Step 2: Update `TestResumeAfterInterruptedWrite` to use `PrepareOutput`**

In that test's execution section, replace the direct `os.Truncate(path, cursor.CleanSize)` call (and any surrounding size check) with:

```go
			if _, err := resume.PrepareOutput(cursor, path, path); err != nil {
				t.Fatalf("PrepareOutput() error = %v", err)
			}
```

so the in-place recovery path exercises the production mechanics.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./pkg/resume/ -run 'TestCopyResume|TestResumeAfterInterruptedWrite' -v 2>&1 | tail -15`
Expected: FAIL — `undefined: resume.PrepareOutput`.

- [ ] **Step 4: Implement `PrepareOutput`**

Append to `pkg/resume/resume.go` after `Skip` (add `"io"` — already imported — and `"path/filepath"` to imports):

```go
// PrepareOutput readies outputPath for appending the continuation of the
// export at inputPath. With equal paths the file is prepared in place: an
// interrupted final write, if any, is truncated away. With distinct paths the
// input is never modified: its clean content is copied raw into outputPath,
// replacing whatever was there, and the interrupted tail is simply not
// copied. Either way it returns how many trailing bytes were left out —
// every entry they held falls at or after the cursor, so the resumed query
// fetches it again.
func PrepareOutput(c *Cursor, inputPath, outputPath string) (int64, error) {
	if filepath.Clean(inputPath) == filepath.Clean(outputPath) {
		return prepareInPlace(c, inputPath)
	}

	in, err := os.Open(inputPath)
	if err != nil {
		return 0, fmt.Errorf("error opening resume file: %w", err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return 0, fmt.Errorf("error inspecting resume file: %w", err)
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("error creating output file: %w", err)
	}

	_, err = io.CopyN(out, in, c.CleanSize)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("error copying clean content to output file: %w", err)
	}

	return info.Size() - c.CleanSize, nil
}

// prepareInPlace drops the interrupted tail so appending continues from the
// clean boundary. It never truncates past the offset the reader identified.
func prepareInPlace(c *Cursor, path string) (int64, error) {
	if !c.Truncated {
		return 0, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("error inspecting resume file: %w", err)
	}
	if info.Size() <= c.CleanSize {
		return 0, nil
	}

	if err := os.Truncate(path, c.CleanSize); err != nil {
		return 0, fmt.Errorf("error truncating resume file: %w", err)
	}

	return info.Size() - c.CleanSize, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/resume/... && go vet ./pkg/resume/... && gofumpt -l pkg/resume`
Expected: PASS, clean, nothing listed.

- [ ] **Step 6: Commit**

```bash
git add pkg/resume/resume.go pkg/resume/resume_test.go
git commit -m "feat(resume): add PrepareOutput for in-place and copy-mode continuation"
```

---

### Task 3: Delete `filestore.SaveLogsAsync`

**Files:**
- Modify: `pkg/filestore/filestore.go`
- Test: `pkg/filestore/filestore_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `filestore` exports exactly `CreateWriter`, `AppendWriter`, `AppendLogsAsync`. Task 4's `cmd` already uses only these.

- [ ] **Step 1: Retool the tests**

In `pkg/filestore/filestore_test.go`, add a seeding helper and rewrite the four tests to use it:

```go
// seed writes logs for the given blocks to a fresh file at path.
func seed(t *testing.T, path string, blocks ...uint64) {
	t.Helper()

	w, err := filestore.CreateWriter(path)
	if err != nil {
		t.Fatalf("CreateWriter() error = %v", err)
	}
	if err := filestore.AppendLogsAsync(t.Context(), feed(blocks...), w, nil); err != nil {
		t.Fatalf("AppendLogsAsync() error = %v", err)
	}
}
```

- `TestSaveLogsAsyncReplacesExistingFile` → rename to `TestCreateWriterReplacesExistingFile`; keep the stale-content pre-write, then `seed(t, path, 1, 2)` and the same `blocksIn` assertion (`[]uint64{1, 2}`).
- In `TestAppendLogsAsyncKeepsExistingContent`, `TestAppendLogsAsyncSkipsFilteredLogs`, `TestAppendLogsAsyncClosesWriterOnCancel`: replace each `filestore.SaveLogsAsync(t.Context(), feed(...), path)` seeding call (and its error check) with the matching `seed(t, path, ...)` call. Assertions unchanged.

- [ ] **Step 2: Delete the function**

Remove `SaveLogsAsync` (currently `pkg/filestore/filestore.go:26-35`) and nothing else. `CreateWriter`'s doc comment already explains the split.

- [ ] **Step 3: Verify**

Run: `go build ./... && go test ./pkg/filestore/... && grep -rn "SaveLogsAsync" --include='*.go' .`
Expected: build OK (proves `cmd` never used it), tests PASS, grep prints nothing.

- [ ] **Step 4: Commit**

```bash
git add pkg/filestore/filestore.go pkg/filestore/filestore_test.go
git commit -m "refactor(filestore): drop SaveLogsAsync, callers compose CreateWriter and AppendLogsAsync"
```

---

### Task 4: Mode resolution in `cmd/export.go`

**Files:**
- Modify: `cmd/export.go`

**Interfaces:**
- Consumes: `resume.Read`, `resume.PrepareOutput` (Tasks 1–2); `filestore.CreateWriter`/`AppendWriter`, `gzipstore.AppendWriter` (existing).
- Produces: the §3 CLI contract. Task 5 edits the same file afterwards.

- [ ] **Step 1: Rework the resume block in `RunE`**

Replace the current resume block (`cmd/export.go:47-74`) with:

```go
			var cursor *resume.Cursor
			if resumeFile != "" {
				cursor, err = resume.Read(resumeFile)
				if err != nil {
					return fmt.Errorf("failed to read resume file %q: %w", resumeFile, err)
				}

				if cmd.Flags().Changed("start") {
					c.log.Warning("--start is ignored when --resume is set", "resumeFile", resumeFile)
				}
				if compress {
					c.log.Warning("--compress is ignored when resuming; resume a compressed file to get a compressed result", "resumeFile", resumeFile)
					compress = false
				}
				// An unset --output means in-place; so does naming the input.
				if !cmd.Flags().Changed("output") || filepath.Clean(outputFile) == filepath.Clean(resumeFile) {
					outputFile = resumeFile
				}

				startBlock = cursor.BlockNumber

				c.log.Info("Resuming export",
					"resumeFile", resumeFile,
					"outputFile", outputFile,
					"startBlock", startBlock,
					"lastLogIndex", cursor.LogIndex,
					"compressed", cursor.Compressed,
				)
			}
```

This removes the old `--output is ignored` warning and the `cursor.Compressed && compress` condition (the warning now fires for every resume with `--compress`). Add `"path/filepath"` to the imports.

- [ ] **Step 2: Replace `discardPartialWrite` with `PrepareOutput`**

Replace the call site (`cmd/export.go:100-102`) with:

```go
			if cursor != nil {
				discarded, err := resume.PrepareOutput(cursor, resumeFile, outputFile)
				if err != nil {
					return err
				}
				if discarded > 0 {
					c.log.Warning("previous export ends with an interrupted write, leaving it out",
						"resumeFile", resumeFile,
						"offset", cursor.CleanSize,
						"discardedBytes", discarded,
					)
				}
			}
```

Delete the whole `discardPartialWrite` method (`cmd/export.go:197-229`). If `"os"` is now unused in the file, remove it from the imports. `openOutput` and `saveLogs` stay exactly as they are — in copy mode they receive the already-prepared `outputFile`, and `openOutput`'s `cursor.Compressed` branch matches because the output's format equals the input's.

- [ ] **Step 3: Update the flag help**

Replace the `--resume` registration line (`cmd/export.go:190`) with:

```go
	cmd.Flags().StringVarP(&resumeFile, "resume", "r", "", "Continue a previous export file (.ndjson, .gz or .gzip); combine with --output to write a new snapshot instead of appending in place")
```

- [ ] **Step 4: Build and verify by hand against copies**

```bash
make binary && go vet ./... && gofumpt -l cmd pkg
mkdir -p /tmp/resume-verify && cp dist/export.ndjson.gzip /tmp/resume-verify/prev.gzip
# Copy mode: prev untouched, next = prev + nothing new (end pinned at the cursor block).
./dist/batch-export export --resume /tmp/resume-verify/prev.gzip --output /tmp/resume-verify/next.gzip --end 47908504
cmp /tmp/resume-verify/prev.gzip dist/export.ndjson.gzip && echo "input untouched"
gzcat /tmp/resume-verify/next.gzip | wc -l   # equals gzcat prev | wc -l
# In-place still works, and --compress warns:
./dist/batch-export export --resume /tmp/resume-verify/prev.gzip --compress --end 47908504 2>&1 | grep -i "ignored"
```

Expected: build clean; copy run logs `Resuming export` with `outputFile=/tmp/resume-verify/next.gzip`; `cmp` silent; line counts equal; the last run warns about `--compress` and appends in place to the copy. **Never point `--resume` or `--output` at `dist/` paths.**

- [ ] **Step 5: Commit**

```bash
git add cmd/export.go
git commit -m "feat(export): compose --resume with --output for copy-mode continuation"
```

---

### Task 5: Saver errors reach the exit code

**Files:**
- Modify: `cmd/export.go`

**Interfaces:**
- Consumes: everything already in the file. No new symbols.
- Produces: §7's behavior — a failed save is a non-zero exit, never a hang, never a lost gzip member.

- [ ] **Step 1: Derive a cancellable context**

Replace `ctx := cmd.Context()` (top of `RunE`) with:

```go
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
```

- [ ] **Step 2: Capture the saver's error and cancel on failure**

Replace the saver goroutine with:

```go
			var saveErr error
			go func() {
				defer wg.Done()

				if err := saveLogs(ctx, logChan, w, cursor); err != nil {
					if errors.Is(err, context.Canceled) {
						c.log.Error(err, "context canceled while saving logs")
						return
					}
					c.log.Error(err, "error saving logs")
					// Stop the fetcher too: with the saver gone, logChan
					// would fill and block it forever.
					saveErr = err
					cancel()
					return
				}
				c.log.Info("all logs have been saved", "outputFile", outputFile)
			}()
```

(`saveErr` is written before `wg.Done` and read after `wg.Wait`, so the WaitGroup orders the accesses; `go test -race` confirms.)

- [ ] **Step 3: Wait and join on every exit path**

The select loop's `errorChan` branch (`cmd/export.go:152-157`) becomes:

```go
				case err, ok := <-errorChan:
					if !ok {
						errorChan = nil
					} else {
						wg.Wait()
						return errors.Join(fmt.Errorf("error retrieving logs: %w", err), saveErr)
					}
```

(no deadlock: the fetcher closes `logChan` on its way out, so the saver finishes and `wg.Wait` returns). The `ctx.Done` branch becomes:

```go
				case <-ctx.Done():
					c.log.Info("context canceled, waiting for logs to be saved...")
					wg.Wait()
					if saveErr != nil {
						return saveErr
					}
					if err := compressFunc(); err != nil {
						return errors.Join(fmt.Errorf("error compressing file: %w", err), ctx.Err())
					}
					return ctx.Err()
```

And the normal exit (`cmd/export.go:174-179`):

```go
				wg.Wait()
				if saveErr != nil {
					return saveErr
				}
				if err := compressFunc(); err != nil {
					return fmt.Errorf("error compressing file: %w", err)
				}

				return nil
```

- [ ] **Step 4: Verify the failure modes by hand**

```bash
make binary && go vet ./... && go test -race ./pkg/...
cp dist/export.ndjson.gzip /tmp/resume-verify/failing.gzip
# Read-only destination: the run must exit non-zero promptly, not hang.
chmod a-w /tmp/resume-verify/failing.gzip
./dist/batch-export export --resume /tmp/resume-verify/failing.gzip --end 47908504; echo "exit=$?"
chmod u+w /tmp/resume-verify/failing.gzip
# Ctrl+C mid-run on a copy: file must still be readable afterwards.
./dist/batch-export export --resume /tmp/resume-verify/failing.gzip &  sleep 3; kill -INT %1; wait
gzcat /tmp/resume-verify/failing.gzip > /dev/null && echo "stream intact"
```

Expected: the read-only run prints the open error and `exit=1` immediately (the open happens before fetching); the interrupted run exits, and `gzcat` reads the file end to end (the member was flushed under `wg.Wait`).

- [ ] **Step 5: Commit**

```bash
git add cmd/export.go
git commit -m "fix(export): surface save failures in the exit code and never skip the final flush"
```

---

### Task 6: README, PR description, final sweep

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: the finished behavior of Tasks 1–5.
- Produces: §8's documentation. Nothing depends on it.

- [ ] **Step 1: Update the feature bullet and flag table**

In `## Features`, replace the resume bullet with:

```markdown
- Continue a previous export from where it stopped (incremental snapshots).
```

In the `## Flags` block, replace the `--resume` line with:

```sh
  -r, --resume string              Continue a previous export file (.ndjson, .gz or .gzip); combine with --output to write a new snapshot instead of appending in place
```

- [ ] **Step 2: Replace the resume section**

Replace everything from `### Resuming an interrupted export` up to (not including) the `The produced NDJSON is consumed by ...` line with:

````markdown
### Continuing a previous snapshot

Instead of re-exporting every block, point `--resume` at the previous
snapshot and name the new one with `--output`. The previous file is read,
never modified; the new file holds everything the previous one did plus the
blocks exported since:

```sh
./dist/batch-export export --resume snapshots/2026-07.gzip --output snapshots/2026-08.gzip
```

Formats are detected by content, not extension: `.ndjson`, `.gz` and `.gzip`
all work, and the output's format always matches the input's. Omitting
`--output` (or naming the input) appends to the previous file in place — the
space-saving variant:

```sh
./dist/batch-export export --resume export.ndjson.gzip
```

The last exported block is re-queried and entries already present are
skipped, so continuing neither duplicates nor drops a log. Each continuation
of a compressed snapshot adds a gzip member — standard tools (`gzcat`,
`gunzip`, Go, Python) read multi-member files as one stream, a year of
monthly continuations costs about 0.02% in size, and
`gzcat old.gzip | gzip > fresh.gzip` consolidates the members any time.

Keep one canonical snapshot file. `--compress` is ignored when resuming:
regenerating a `.gzip` from a plain twin is how an independently continued
archive gets overwritten. Resume a compressed file to get a compressed
result.

Resume only files this tool produced. The file's tail is validated before
anything is written: content the tool never writes — a non-log line, foreign
data, an alien gzip member — is refused rather than repaired. The one
exception is the tool's own interrupted final write (a run killed
mid-export): in copy mode it is simply not copied, in place it is truncated
away with a warning, and its entries are re-fetched. Note that resuming does
not detect a file from a different chain; pairing the snapshot with the
right `--endpoint` is the operator's contract.
````

- [ ] **Step 3: Full verification sweep**

```bash
make test && go test -race ./pkg/... && make vet && make lint && make binary
```

Expected: everything green, `0 issues.` from lint.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: reframe resume around incremental snapshots"
```

- [ ] **Step 5: Update the PR description (no push without approval)**

Rewrite the PR #11 body to lead with incremental snapshots and copy mode, and list the three semantic changes from §11 (output composes; `--compress` always ignored on resume; strict refusal replaces lenient tolerance). Hold the `git push` and `gh pr edit` until the human approves pushing this phase.

---

## Self-Review

**Spec coverage:** §3 CLI table → Task 4 Step 1 (mode resolution) + Step 3 (help text); §4 modes → Task 2 (`PrepareOutput`) + Task 4 Step 2; §5 strict validation, both formats, error taxonomy → Task 1; §6 appending/skip → unchanged code, re-verified by Task 2's round trips; §7 exit codes → Task 5; §8 README → Task 6; §9 placement → Tasks 1–4 as mapped; §10 testing → Tasks 1–3 test steps; §11 compatibility → Task 6 Step 5. No spec requirement without a task.

**Type consistency:** `PrepareOutput(c *Cursor, inputPath, outputPath string) (int64, error)` is defined in Task 2 and consumed with that exact shape in Task 4 Step 2 and the Task 2 tests. `ErrNotAnExport`/`ErrNoLogs` defined in Task 1, consumed in Task 1's refusal table. `seed(t, path, blocks...)` defined and used only in Task 3. `appendWriter`, `feed`, `logsIn`, `ids`, `truncateLast`, `gz`, `ndjson`, `write` all pre-exist in `resume_test.go`.

**Known deliberate residue:** `cmd`'s §7 plumbing has no unit test (no RPC harness — §10 sanctions manual evidence, Task 5 Step 4 collects it); `compressFunc` stays for fresh runs only; the gzip walk treats unparseable bytes after a clean member as an interrupted tail (indistinguishable from a partial member header — §5 documents this).
