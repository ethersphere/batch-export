# batch-export

batch-export is a tool to retrieve Ethereum event logs for specific contracts, particularly designed for Swarm's Postage Stamp contract on the Gnosis Chain. It fetches logs within a specified block range using the `export` command and saves them to a file.

## Features

- Retrieve event logs for a specified contract address and block range.
- Handles large block ranges by querying in smaller chunks.
- Supports rate limiting for RPC requests.
- Retries requests that fail with a transient network error (timeouts, dropped connections, rate limiting) using an exponential backoff.
- Saves retrieved logs to a specified output file (default: `export.ndjson`) in NDJSON format.
- Exports up to the latest **finalized** block by default (`--end=0`), so a snapshot never contains logs from blocks that can still be reorged.
- Graceful shutdown on interrupt signals (Ctrl+C).
- By default emits a slim log shape — only the `types.Log` fields Bee consumes (`address`, `topics`, `data`, `blockNumber`, `transactionHash`), plus `logIndex` so exports stay resumable. Pass `--slim=false` to emit the full geth `types.Log` JSON shape for other consumers. The slim shape is decoder-compatible with geth's `types.Log` (so Bee reads both interchangeably).

## Requirements

- Go 1.25 or later

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
      --end uint                   End block (optional, uses latest finalized block if 0)
  -e, --endpoint string            Ethereum RPC endpoint URL
  -h, --help                       help for export
  -m, --max-request int            Max RPC requests/sec (default 15)
  -o, --output string              Output file path (NDJSON) (default "export.ndjson")
      --retry-delay duration       Delay before the first retry, doubling per retry up to 30s (default 1s)
      --retry-max int              Max retries per RPC request on transient network errors (0 disables retrying) (default 5)
      --slim                       Emit only the types.Log fields Bee consumes, plus logIndex (default true)
      --start uint                 Start block (optional, uses contract start block if 0) (default 31306381)
  -v, --verbosity string           Log verbosity (silent, error, warn, info, debug) (default "info")
```

The produced NDJSON is consumed by [batch-archive](https://github.com/ethersphere/batch-archive), which embeds it for use inside Bee.

## Maintainers

- [Bee](https://github.com/orgs/ethersphere/teams/bee) team
