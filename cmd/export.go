package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	ethclient "github.com/ethersphere/batch-export/pkg/ethclientwrapper"
	"github.com/ethersphere/batch-export/pkg/eventfetcher"
	"github.com/ethersphere/batch-export/pkg/filestore"
	"github.com/ethersphere/batch-export/pkg/gzipstore"
	"github.com/ethersphere/batch-export/pkg/resume"
	"github.com/ethersphere/bee/v2/pkg/config"
	"github.com/ethersphere/bee/v2/pkg/util/abiutil"
	"github.com/spf13/cobra"
)

func (c *command) initExportCmd() (err error) {
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

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export Swarm Postage Stamp contract event logs within a block range.",
		Long: `Exports event logs for the Swarm Postage Stamp contract from a specified Ethereum RPC endpoint
within a given block range (--start to --end). It handles large ranges by querying in chunks (--block-range-limit)
and respects RPC rate limits (--max-request).

The retrieved logs are saved to the specified output file (default: 'export.ndjson') in NDJSON format.
The process can be interrupted at any time (Ctrl+C), and it will attempt to save already retrieved logs before exiting.`,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

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

			ec, err := ethclient.NewClient(ctx, rpcEndpoint, ethclient.WithRateLimit(maxRequest), ethclient.WithLogger(c.log))
			if err != nil {
				return fmt.Errorf("failed to connect to the Ethereum client: %w", err)
			}
			defer ec.Close()

			chainID, err := ec.ChainID(ctx)
			if err != nil {
				return fmt.Errorf("failed to get chainID: %w", err)
			}

			chainCfg, found := config.GetByChainID(chainID.Int64())
			if !found {
				return fmt.Errorf("chain config not found for chain ID %d", chainID.Int64())
			}

			postageStampContractABI := abiutil.MustParseABI(chainCfg.PostageStampABI)

			client := eventfetcher.NewClient(ec, postageStampContractABI, blockRangeLimit, c.log)

			if startBlock == 0 {
				startBlock = chainCfg.PostageStampStartBlock
			}

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

			// Opened before the first log is fetched: from inside the saving
			// goroutine, a failure here would leave the fetcher pushing into a
			// channel nobody drains.
			w, err := openOutput(outputFile, cursor)
			if err != nil {
				return fmt.Errorf("failed to open output file: %w", err)
			}

			c.log.Info("Retrieving logs", "startBlock", startBlock, "endBlock", endBlock)

			logChan, errorChan := client.GetLogs(ctx, &eventfetcher.Request{
				Address:    chainCfg.PostageStampAddress,
				StartBlock: startBlock,
				EndBlock:   endBlock,
			})

			var wg sync.WaitGroup
			wg.Add(1)

			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()

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

			compressFunc := func() error {
				if compress {
					if err := gzipstore.CompressFile(outputFile, outputFile+".gzip"); err != nil {
						return fmt.Errorf("error compressing file: %w", err)
					}
					c.log.Info("File compressed", "outputFile", outputFile+".gzip")
				}
				return nil
			}

			for {
				select {
				case err, ok := <-errorChan:
					if !ok {
						errorChan = nil
					} else {
						wg.Wait()
						if saveErr != nil && errors.Is(err, context.Canceled) {
							return saveErr
						}
						return errors.Join(fmt.Errorf("error retrieving logs: %w", err), saveErr)
					}
				case <-ticker.C:
					c.log.Info("still retrieving logs...")
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
				}

				if errorChan == nil {
					break
				}
			}

			wg.Wait()
			if saveErr != nil {
				return saveErr
			}
			if err := compressFunc(); err != nil {
				return fmt.Errorf("error compressing file: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().Uint64VarP(&startBlock, "start", "", 31306381, "Start block (optional, uses contract start block if 0)")
	cmd.Flags().Uint64VarP(&endBlock, "end", "", 0, "End block (optional, uses latest block if 0)")
	cmd.Flags().StringVarP(&rpcEndpoint, "endpoint", "e", "https://rpc.gnosis.gateway.fm", "Ethereum based RPC endpoint URL")
	cmd.Flags().IntVarP(&maxRequest, "max-request", "m", 15, "Max RPC requests/sec")
	cmd.Flags().Uint32VarP(&blockRangeLimit, "block-range-limit", "b", 5, "Max blocks per log query")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "export.ndjson", "Output file path (NDJSON)")
	cmd.Flags().BoolVarP(&compress, "compress", "c", false, "Compress to GZIP")
	cmd.Flags().StringVarP(&resumeFile, "resume", "r", "", "Continue a previous export file (.ndjson, .gz or .gzip); combine with --output to write a new snapshot instead of appending in place")

	c.root.AddCommand(cmd)

	return nil
}

// openOutput opens the destination for a run's logs: a fresh file when cursor
// is nil, or a writer that appends to the file the cursor came from.
func openOutput(outputFile string, cursor *resume.Cursor) (io.WriteCloser, error) {
	switch {
	case cursor == nil:
		return filestore.CreateWriter(outputFile)
	case cursor.Compressed:
		return gzipstore.AppendWriter(outputFile)
	default:
		return filestore.AppendWriter(outputFile)
	}
}

// saveLogs writes logs to w, dropping any entry a resumed export already holds.
// A nil cursor means the destination starts empty, so every log is kept.
func saveLogs(ctx context.Context, logChan <-chan types.Log, w io.WriteCloser, cursor *resume.Cursor) error {
	var skip func(types.Log) bool
	if cursor != nil {
		skip = cursor.Skip
	}

	return filestore.AppendLogsAsync(ctx, logChan, w, skip)
}
