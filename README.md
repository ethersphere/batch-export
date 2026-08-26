# batch-export

batch-export is a tool to retrieve Ethereum event logs for specific contracts, particularly designed for Swarm's Postage Stamp contract on the Gnosis Chain. It fetches logs within a specified block range using the `export` command and saves them to a file.

## Features

- Retrieve event logs for a specified contract address and block range.
- Handles large block ranges by querying in smaller chunks.
- Supports rate limiting for RPC requests.
- Saves retrieved logs to a specified output file (default: `export.ndjson`) in NDJSON format.
- Graceful shutdown on interrupt signals (Ctrl+C).
- Resume an interrupted export from an existing `.ndjson`, `.gz`, or `.gzip` file.

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
  -r, --resume string              Resume a previous export file (.ndjson, .gz or .gzip); overrides --start and --output
      --start uint                 Start block (optional, uses contract start block if 0) (default 31306381)
  -v, --verbosity string           Log verbosity (silent, error, warn, info, debug) (default "info")
```

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
part of it, and the entries already in the file are skipped. So long as the
file ends cleanly, resuming neither duplicates nor drops a log. Appending to a
compressed export adds a second gzip member — standard tools such as `gzcat`,
`gunzip`, and Go's `compress/gzip` read the result as one continuous stream.

#### When the previous run was killed mid-write

A run stopped by `SIGKILL`, a crash, or a full disk can leave a partial entry
at the end of the file: half a line, a line that never got its newline, or a
gzip member that never got its trailer. Appending onto any of those would
corrupt the file, so only an entry that is complete and properly terminated
counts as the resume point.

The tool finds the last offset at which the file is known to be complete — the
end of the last newline-terminated line that parses as a log entry, or of the last whole gzip member —
and discards whatever follows it, logging how many bytes it dropped and from
where:

```
"level"="warning" "msg"="resume file ends with a partial write, discarding it" "offset"=89649991 "discardedBytes"=317
```

For export content, nothing is lost by that: everything discarded sits at or after the resume
point, so the resumed query fetches it again. Anything discarded that was never a log entry is simply removed and not re-fetched. Because a line that parses but
has no newline does not count, the resume point in that case is the line
before it, and that entry is re-fetched too.

If no such offset can be identified — a gzip file whose only member is
truncated, for instance, since there is no member boundary to append at — the
tool refuses to touch the file and exits with an error rather than guess. Fall
back to a fresh export in that case.

When `--resume` is set it overrides `--start` and `--output`.

The produced NDJSON is consumed by [batch-archive](https://github.com/ethersphere/batch-archive), which embeds it for use inside Bee.

## Maintainers

- [Bee](https://github.com/orgs/ethersphere/teams/bee) team
