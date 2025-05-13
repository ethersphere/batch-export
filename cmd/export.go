package cmd

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	ethclient "github.com/ethersphere/batch-export/pkg/ethclientwrapper"
	"github.com/ethersphere/batch-export/pkg/eventfetcher"
	"github.com/ethersphere/batch-export/pkg/filestore"
	"github.com/ethersphere/batch-export/pkg/gzipstore"
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
			ctx := cmd.Context()

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

			go func() {
				defer wg.Done()
				if err := filestore.SaveLogsAsync(ctx, logChan, outputFile); err != nil {
					if errors.Is(err, context.Canceled) {
						c.log.Error(err, "context canceled while saving logs")
						return
					}
					c.log.Error(err, "error saving logs")
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
						return fmt.Errorf("error retrieving logs: %w", err)
					}
				case <-ticker.C:
					c.log.Info("still retrieving logs...")
				case <-ctx.Done():
					c.log.Info("context canceled, waiting for logs to be saved...")
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
			if err := compressFunc(); err != nil {
				return fmt.Errorf("error compressing file: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().Uint64VarP(&startBlock, "start", "", 31306381, "Start block (optional, uses contract start block if 0)")
	cmd.Flags().Uint64VarP(&endBlock, "end", "", 0, "End block (optional, uses latest block if 0)")
	cmd.Flags().StringVarP(&rpcEndpoint, "endpoint", "e", "https://wandering-evocative-gas.xdai.quiknode.pro/0f2525676e3ba76259ab3b72243f7f60334b0000/", "Ethereum RPC endpoint URL")
	cmd.Flags().IntVarP(&maxRequest, "max-request", "m", 15, "Max RPC requests/sec")
	cmd.Flags().Uint32VarP(&blockRangeLimit, "block-range-limit", "b", 5, "Max blocks per log query")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "export.ndjson", "Output file path (NDJSON)")
	cmd.Flags().BoolVarP(&compress, "compress", "c", false, "Compress to GZIP")

	c.root.AddCommand(cmd)

	return nil
}
