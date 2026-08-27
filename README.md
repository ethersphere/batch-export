# batch-export

batch-export is a tool to retrieve Ethereum event logs for specific contracts, particularly designed for Swarm's Postage Stamp contract on the Gnosis Chain. It fetches logs within a specified block range using the `export` command and saves them to a file.

## Features

- Retrieve event logs for a specified contract address and block range.
- Handles large block ranges by querying in smaller chunks.
- Supports rate limiting for RPC requests.
- Saves retrieved logs to a specified output file (default: `export.ndjson`) in NDJSON format.
- Graceful shutdown on interrupt signals (Ctrl+C).
- Continue a previous export from where it stopped (incremental snapshots).

## Requirements

- Go 1.24 or later

## Installation

```sh
git clone https://github.com/ethersphere/batch-export.git
cd batch-export
make binary
# The binary will be located in the dist/ folder
```

## Usage

The primary command is export.

```sh
./dist/batch-export export --help
```

```sh
./dist/batch-export export \
    --block-range-limit=10000 \
    --compress=true \
    --end=0 \
    --endpoint <YOUR_GNOSIS_RPC_ENDPOINT>
```

## Flags

```sh
  -b, --block-range-limit uint32   Max blocks per log query (default 5)
  -c, --compress                   Compress to GZIP
      --end uint                   End block (optional, uses latest block if 0) (default 39810670)
  -e, --endpoint string            Ethereum RPC endpoint URL
  -h, --help                       help for export
  -m, --max-request int            Max RPC requests/sec (default 15)
  -o, --output string              Output file path (NDJSON) (default "export.ndjson")
  -r, --resume string              Continue a previous export file (.ndjson, .gz or .gzip); combine with --output to write a new snapshot instead of appending in place
      --start uint                 Start block (optional, uses contract start block if 0) (default 31306381)
  -v, --verbosity string           Log verbosity (silent, error, warn, info, debug) (default "info")
```

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

The produced NDJSON is consumed by [batch-archive](https://github.com/ethersphere/batch-archive), which embeds it for use inside Bee.

## Maintainers

- [Bee](https://github.com/orgs/ethersphere/teams/bee) team
