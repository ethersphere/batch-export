# Resume as Incremental Snapshots

**Date:** 2026-08-27
**Status:** Draft, awaiting review
**Scope:** the complete behavior of the `--resume` feature on PR #11. This
document is the source of truth; the implementation is edited until it
conforms.

## 1. Goal

`batch-export` produces periodic snapshots of Postage Stamp contract events.
The operator runs an export today, archives the resulting `.gzip`, and later —
typically a month later — produces the next snapshot by *continuing from the
previous one* instead of re-exporting every block from the contract's start.

`--resume` therefore means: **continue a previous, normally finished export
made by this same tool.** A run that was interrupted mid-write is handled
(§5), but crash recovery is robustness, not the purpose, and does not shape
the interface or the documentation.

## 2. Non-Goals

- **Repairing foreign or manipulated files.** A resume input is trusted to be
  this tool's own output; anything else is refused, never "fixed" (§5).
- **Validating chain provenance.** An export from the same tool against a
  different chain parses identically; detecting the mismatch would need a file
  header, which changes the format `batch-archive` consumes. Documented
  limitation.
- **Validating the interior of plain files.** Plain-file validation covers the
  region the cursor reading must touch (the tail); silent corruption elsewhere
  in a 90 MB file is out of scope. (Gzip necessarily decodes the whole stream,
  so it validates every line as a side effect — §5.)
- **Retiring post-hoc `CompressFile` for fresh `--compress` runs.** Fresh
  exports keep today's behavior.

## 3. CLI Contract

No new flags. `--resume` names the **input** (the previous snapshot);
`--output` names the **destination**. They compose:

| Invocation | Behavior |
|---|---|
| `export` | Fresh export to `--output` (default `export.ndjson`). Unchanged. |
| `export --resume old.gzip` | **In-place**: append to `old.gzip` itself. An unset `--output` does not redirect the result to its default. |
| `export --resume old.gzip --output new.gzip` | **Copy mode**: `old.gzip` is never modified; `new.gzip` = clean content of `old.gzip` + newly fetched entries. |
| `export --resume f --output f` | Identical paths (`filepath.Clean` equality) mean in-place. Distinct spellings of one file (symlinks, hardlinks) are the operator's responsibility. |

Flag interactions when `--resume` is set:

- `--start` is ignored with a warning: the cursor decides the start block.
- `--end` works as usual (default 0 = latest block).
- `--compress` is **always ignored with a warning**. Post-hoc compression of a
  resumed plain file is the twin-file trap: `CompressFile`'s `os.Create` would
  overwrite an independently accumulated `.gzip`. A compressed result comes
  from resuming a compressed input; compressing a plain result is `gzip`'s job.
- The output's format always equals the input's format, detected from the
  input's leading magic bytes. Extensions are names, nothing more; `.ndjson`,
  `.gz` and `.gzip` all work.
- In copy mode an existing file at `--output` is overwritten (`os.Create`
  semantics, same as a fresh export).

Flag help: `Continue a previous export file (.ndjson, .gz or .gzip); combine
with --output to write a new snapshot instead of appending in place`.

## 4. Modes

**Copy mode** (input != output) — the recommended monthly workflow:

```sh
batch-export export --resume snapshots/2026-07.gzip --output snapshots/2026-08.gzip
```

The first `CleanSize` bytes (§5) of the input are copied to the output raw —
no decompression, no re-encoding — and new entries are appended to the output.
The input is opened read-only and is never modified, truncated, or deleted. If
the run fails, the output is incomplete but the input is intact: delete the
output and rerun.

**In-place** (input == output): the space-saving variant. If the input carries
an interrupted final write (§5), the file is first truncated to `CleanSize`
with a warning naming the discarded byte count; appending then proceeds.

## 5. Reading the Cursor: Strict Tail Validation

The *cursor* is the last complete log entry of the input: its `blockNumber`
and `logIndex`, plus `Compressed`, `CleanSize` (the byte offset at which the
input's trustworthy content ends) and `Truncated` (whether anything follows
`CleanSize`).

The contract is strict because the input is by definition this tool's own
output. The tool writes NDJSON via `json.Encoder` — each entry is one line,
value and terminating newline in a single write — either plainly or inside
gzip members, one member per run, each member holding only whole lines. A
member may be empty (a resumed run that fetched nothing new still closes its
member). Consequently the **only** irregularity the tool itself can produce is
an interrupted final write:

- plain: a single incomplete trailing line (a partial write never ends in a
  newline, and these JSON lines contain no raw newlines);
- gzip: a single trailing member without its terminating CRC/length trailer.

Exactly that irregularity is tolerated: the cursor is the last complete entry
before it, `CleanSize` excludes it, `Truncated` is true, and each mode handles
it per §4 (copy mode does not copy it; in-place truncates it). Nothing else is
tolerated. Any of the following mean the file is not untouched tool output,
and `Read` refuses with `ErrNotAnExport`, naming what was found and at which
offset:

- a complete line (newline-terminated) that does not parse as a log entry —
  including blank lines;
- a trailing newline-free fragment longer than `maxLineBytes` (1 MiB; real
  entries run a few hundred bytes);
- a cleanly terminated gzip member that ends mid-line or contains a complete
  non-parsing line;
- gzip content after a member that failed to decode (unreachable in practice —
  decoding stops there — but stated for completeness: bytes past `CleanSize`
  are ignored, never interpreted).

Refusal is deliberate: for an archival artifact, silently cutting away content
the tool never wrote is worse than stopping. The operator decides what a
manipulated file means.

**Errors.** Two sentinel errors replace the current three:

- `ErrNotAnExport` — foreign content, as above. Terminal; the message says
  what and where.
- `ErrNoLogs` — the file is consistent with tool output but holds no complete
  entry to resume from: empty, or only an interrupted first write. The remedy
  is a fresh export.

(`ErrNoCleanBoundary` is retired: its gzip case — a sole truncated member — is
`ErrNoLogs`; its mid-line-member case is `ErrNotAnExport`.)

**Mechanics.** Plain: one read of the last `maxLineBytes` bytes suffices —
find the last newline, check the fragment after it, parse the single line
before it. The current multi-window backward walk with carry exists only to
tolerate foreign junk and is deleted. Gzip: cannot seek, so the stream is
decoded member by member with a counting reader (which must implement
`io.ByteReader` so `gzip.Reader` consumes it directly and member boundaries
are observed exactly); every complete line must parse; `CleanSize` is the end
of the last cleanly terminated member.

## 6. Appending

- **Plain**: `O_APPEND` on the destination.
- **Gzip**: a new gzip member (`gzip.NewWriter` on a file opened `O_APPEND`).
  Concatenated members are one valid stream per RFC 1952; `gzcat`, `gunzip`,
  Go and Python read them transparently. Existing bytes are never rewritten.
  Measured on the real 90 MB export: 12 members cost +0.021% over a single
  member, 120 members +0.224%. Members can be consolidated any time with
  `gzcat old.gzip | gzip > fresh.gzip`.
- **Skip filter**: the resumed query starts at the cursor's block
  *inclusively* — an interrupted run may have written only part of it, and for
  a finished run the re-query is a no-op — and entries at or before the cursor
  (`blockNumber < cursor's`, or equal block with `logIndex <=` cursor's) are
  dropped before writing. No gaps, no duplicates.
- The destination writer is opened before the first log is fetched, so an open
  failure aborts the run instead of leaving the fetcher pushing into a channel
  nobody drains.

## 7. Error Handling and Exit Codes

The saver goroutine's error must reach `RunE`'s return value:

- `RunE` derives a cancellable context. The saver records its error and
  **cancels the derived context on failure**, which unblocks the fetcher,
  which closes `errorChan`, which lets the select loop exit. No hang wherever
  the save fails (open, encode, close).
- **Every** exit path out of the select loop runs `wg.Wait()` before
  returning — including the `errorChan` error branch, which today returns
  immediately and can lose the buffered gzip member.
- After `wg.Wait()`, the saver's error is joined into the returned error: a
  failed save always exits non-zero. On a gzip destination the writer's
  `Close` finalizes the member, so its error is part of the save result, not
  noise. A save error caused only by cancellation is not double-reported.
- The goroutine still logs the error when it happens, so the operator sees it
  immediately.

## 8. Documentation (README)

1. Section title **"Continuing a previous snapshot"**; lead example is copy
   mode with dated files (§4). In-place follows as the variant.
2. The multi-member note with the measured overhead and the consolidation
   one-liner.
3. A short "if the previous run was interrupted" subsection: copy mode —
   rerun; in-place — the tool truncates the interrupted write and re-fetches,
   warning shown. A fusnote, not the headline.
4. The trust rule, stated plainly: resume only files this tool produced; a
   file with any other content is refused, and resuming does not detect a
   wrong chain — that is the operator's contract.
5. One canonical snapshot file: keep either the plain file or the gzip, not
   both; `--compress` is ignored on resume.
6. Feature bullet: "Continue a previous export from where it stopped
   (incremental snapshots)."

## 9. Code Placement

- `pkg/resume`: gains `PrepareOutput(cursor, inputPath, outputPath)` — the
  in-place truncation or clean-prefix copy of §4. It lives beside `Read`
  because the package that computes `CleanSize` should also enforce it; a
  caller can then not append without the invariant holding, and the mechanics
  are testable without an RPC harness.
- `cmd/export.go`: resolves the mode (§3), calls `PrepareOutput`, and picks
  create vs append in `openOutput`; the saver-error plumbing of §7 lives in
  `RunE`.
- `pkg/resume`: strict validation per §5; `lastCursorPlain` shrinks to the
  single-tail read; `lastCursorGzip` keeps the member walk, drops cross-member
  line carry (whole-line members are part of the contract), refuses on any
  non-parsing complete line; error taxonomy per §5.
- `pkg/filestore`: `SaveLogsAsync` — exported, no production caller — is
  deleted with its tests; fresh exports route through `CreateWriter` +
  `AppendLogsAsync`. The `AppendLogsAsync` close-error join stays.
- `pkg/gzipstore`: unchanged.

## 10. Testing

All tests remain package-level (`./pkg/...`) plus the cross-package round
trips in `resume_test.go`; `cmd` still has no RPC harness, so §7 is verified
at whatever level extraction permits, with manual evidence recorded in the PR
for the rest.

- **Copy-mode round trip** (both formats): input byte-identical before and
  after; output = input's content + exactly the new entries in order; output
  itself resumable; output ends clean.
- **Copy mode from an interrupted input** (both formats): input untouched
  *including its partial tail*; output holds the clean content plus re-fetched
  entries; nothing lost, nothing duplicated.
- **In-place round trip and interrupted-write recovery**: as today.
- **Strict refusal** (`ErrNotAnExport`): plain with a trailing complete
  non-log line; plain with a blank line; plain with a >1 MiB newline-free
  tail (refused fast, no scan-back); gzip with a junk member; gzip with a
  clean member ending mid-line.
- **Tolerated irregularities**: plain partial trailing line; gzip truncated
  final member; empty gzip member (valid, contributes nothing).
- **`ErrNoLogs`**: empty file; file holding only a partial first write.
- **Same-path detection**: `--resume f --output f` behaves as in-place.
- Deleted along with the code they exercised: the multi-window walk tests
  (garbage windows, boundary straddle).

## 11. Compatibility

PR #11 is unmerged; nothing released changes. Within the PR: `--output`
composes with `--resume` instead of being overridden; `--compress` is ignored
on every resume, not only on compressed input; lenient junk tolerance is
replaced by strict refusal. All three are called out in the PR description.
The final merge squashes, so branch history need not tell this story twice.
