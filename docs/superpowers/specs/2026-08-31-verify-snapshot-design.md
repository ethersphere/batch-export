# `batch-export verify` — snapshot regression gate

- **Date:** 2026-08-31
- **Status:** proposed
- **Scope:** one subcommand + its package, and one workflow step wiring it in

## Problem

The Batch Sync workflow resumes the postage-batch snapshot from a tag of
[ethersphere/batch-archive](https://github.com/ethersphere/batch-archive) and
publishes the refreshed file back as a commit on `main` plus the next patch
tag. Nothing verifies that the refreshed snapshot is a strict extension of the
one it resumed from. A regression in batch-export — in the resume logic, the
slim NDJSON encoding, or the gzip append handling — could silently drop,
mutate, or reorder historical entries, and the workflow would tag and publish
the corrupted snapshot as the new latest version.

Separately, the workflow extracts the last block number for the commit title
with a `gunzip | tail | sed` pipeline whose regex re-encodes the slim JSON
shape by hand. Nothing ties that regex to the Go struct that defines the
format, so a format change passes every Go test and breaks only in CI.

## Goal

A `batch-export verify` subcommand that:

1. proves the new snapshot contains everything the old one did, byte for byte
   and in order, with only new entries appended;
2. validates the appended entries parse and are correctly ordered;
3. prints the new snapshot's last block number, replacing the sed pipeline.

Wired into the Batch Sync workflow between the Export and Publish steps, so a
failed verification blocks the commit and the tag.

## Non-goals

- No chain-side validation: `verify` never queries an RPC endpoint.
- No repair: a bad snapshot is reported, never fixed.
- No comparison against batch-archive `main`'s current file: the contract is
  old-input vs. new-output of one export run. (The workflow always resumes
  the file it just fetched, so that is the meaningful pair.)
- No change to export behavior, scheduling, or bootstrap flows.

## CLI contract

```
batch-export verify --old <file> --new <file> [--verbosity <level>]
```

- `--old` (required): the snapshot the export resumed from — in the workflow,
  the file fetched from the batch-archive tag.
- `--new` (required): the freshly exported snapshot.
- Formats: plain NDJSON or gzip, each file detected independently by content
  (the same detection `--resume` uses); any old/new combination is accepted.
- **stdout** on success: the new snapshot's last block number, decimal, one
  line, nothing else — safe for `$(...)` capture in the workflow.
- **stderr**: all logging and diagnostics.
- **Exit code**: 0 = verified; non-zero = do not publish (verification
  failure, I/O error, and parse error are deliberately not distinguished —
  the workflow reacts identically to all of them).

## Verification algorithm

1. **Cursor of the old file** via the existing `resume.Read`: yields the last
   complete entry's `(blockNumber, logIndex)`, the compressed flag, and the
   clean size. `resume.Read`'s refusals fail verification with the same
   meaning they have on resume: `ErrNotAnExport` (file was altered) and
   `ErrNoLogs` (nothing to extend).
2. **Prefix property.** The new file's decompressed content must begin, byte
   for byte, with the old file's *clean content* — the same bytes
   `resume.PrepareOutput` copies (for a gzip old file: the compressed bytes up
   to the cursor's clean size, decompressed; for plain NDJSON: the file up to
   clean size). Comparison is streamed with constant memory; files are never
   loaded whole. If the old file carries a truncated tail past its clean
   size, the tail is excluded from the comparison and a warning is logged —
   mirroring what resume itself discards.
3. **Appended tail.** Every line of the new file past the prefix must:
   - stay under the same line-length cap resume enforces;
   - parse as an export log entry (slim or full shape, as resume accepts);
   - be strictly increasing in `(blockNumber, logIndex)` order, with the
     first appended entry strictly greater than the old file's cursor —
     matching resume's skip semantics, which re-query the cursor block but
     never re-write entries at or before the cursor.
4. **Empty tail is valid**: a run that found no new logs verifies
   successfully and prints the old cursor's block number.
5. **Output**: the last appended entry's block number (or the old cursor's,
   when the tail is empty), printed in decimal on stdout.

Failure messages name the location: byte offset and line number of the first
divergence for a prefix mismatch; line number and reason for a tail failure.

## Code layout

```
cmd/verify.go              thin cobra command: flags, logger, calls pkg/verify,
                           prints the block number (mirrors cmd/export.go style)
pkg/verify/verify.go       Verify(oldPath, newPath) (Result, error);
                           Result{LastBlock uint64, Appended int}
pkg/verify/verify_test.go  unit tests with generated fixture files
```

`pkg/verify` reuses `pkg/resume` for cursor reading and format handling. If a
helper it needs (e.g. the decompressing opener) is unexported in `pkg/resume`,
the minimal piece is exported from there rather than duplicated.

## Workflow integration (same PR)

`.github/workflows/batch-sync.yml` gains one step between Export and Publish:

```yaml
      - name: Verify snapshot
        run: |
          set -euo pipefail
          last_block="$(./dist/batch-export verify --old resume.ndjson.gzip --new snapshot.ndjson.gzip)"
          echo "LAST_BLOCK=${last_block}" >> "${GITHUB_ENV}"
```

The Publish step then uses `${LAST_BLOCK}` in the commit title and drops the
`gunzip | tail | sed` block together with its empty-result guard. A verify
failure stops the job before any commit or tag is created.

## Testing

- **Unit (`pkg/verify`)**, fixtures generated per test: identical files;
  proper superset; corrupted byte inside the prefix; new file shorter than
  old; non-monotonic appended entry; duplicate of the cursor entry; malformed
  JSON tail line; old file with no complete entry; old file with a truncated
  tail; plain/gzip in all four combinations.
- **Command (`cmd`)**: exit code and stdout shape for one passing and one
  failing pair.
- **Workflow**: `actionlint` locally; end-to-end via a manual dispatch after
  merge.

Development is test-first (TDD), consistent with the repo's existing
`pkg/resume` test style.

## Delivery

Branch `feat/verify-snapshot` off `main`; conventional commits (`feat:` for
the subcommand, `ci:` for the workflow wiring); one PR to `main`.
