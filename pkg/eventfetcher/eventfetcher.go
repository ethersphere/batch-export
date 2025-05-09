package eventfetcher

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/go-playground/validator/v10"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/ethclientwrapper"
	"github.com/ethersphere/batch-export/pkg/logcache"
)

type Client struct {
	validate        *validator.Validate
	client          *ethclientwrapper.Client
	logger          log.Logger
	blockRangeLimit uint32
	logCache        *logcache.Cache

	batchCreatedTopic       common.Hash
	batchTopUpTopic         common.Hash
	batchDepthIncreaseTopic common.Hash
	priceUpdateTopic        common.Hash
	pausedTopic             common.Hash
}

func NewClient(client *ethclientwrapper.Client, postageStampContractABI abi.ABI, blockRangeLimit uint32, logger log.Logger) *Client {
	return &Client{
		validate:                validator.New(),
		logCache:                logcache.New(),
		client:                  client,
		logger:                  logger,
		blockRangeLimit:         blockRangeLimit,
		batchCreatedTopic:       postageStampContractABI.Events["BatchCreated"].ID,
		batchTopUpTopic:         postageStampContractABI.Events["BatchTopUp"].ID,
		batchDepthIncreaseTopic: postageStampContractABI.Events["BatchDepthIncrease"].ID,
		priceUpdateTopic:        postageStampContractABI.Events["PriceUpdate"].ID,
		// pausedTopic:             postageStampContractABI.Events["Paused"].ID,
	}
}

type Request struct {
	Address    common.Address `validate:"required"`
	StartBlock uint64
	EndBlock   uint64
}

// GetLogs fetches logs and sends them to a channel
func (c *Client) GetLogs(ctx context.Context, tr *Request) (<-chan types.Log, <-chan error) {
	logChan := make(chan types.Log, 100)
	errorChan := make(chan error, 1)

	go func() {
		defer close(logChan)
		defer close(errorChan)
		// send the last cached value to the channel
		// defer func() {
		// 	priceUpdateLog := c.logCache.Get()
		// 	if priceUpdateLog != nil {
		// 		c.logger.Info("sending last cached value", "transactionHash", priceUpdateLog.TxHash)
		// 		logChan <- *priceUpdateLog
		// 	}
		// }()

		if err := c.validate.Struct(tr); err != nil {
			errorChan <- fmt.Errorf("error validating request: %w", err)
			return
		}

		var fromBlock, toBlock *big.Int

		// Determine toBlock
		if tr.EndBlock == 0 {
			latestBlock, err := c.client.BlockNumber(ctx)
			if err != nil {
				errorChan <- fmt.Errorf("failed to get latest block number: %w", err)
				return
			}
			toBlock = new(big.Int).SetUint64(latestBlock)
		} else {
			toBlock = big.NewInt(int64(tr.EndBlock))
		}

		if tr.StartBlock == 0 {
			fromBlock = big.NewInt(0)
		} else {
			fromBlock = big.NewInt(int64(tr.StartBlock))
		}

		if fromBlock.Cmp(toBlock) > 0 {
			errorChan <- fmt.Errorf("start block (%s) cannot be greater than end block (%s)", fromBlock.String(), toBlock.String())
			return
		}
		query := c.filterQuery(tr.Address, fromBlock, toBlock)
		c.fetchLogs(ctx, query, logChan, errorChan)
	}()

	return logChan, errorChan
}

// fetchLogs iterates through block ranges and fetches logs.
// It sends errors to errorChan and stops processing if an error occurs or context is cancelled.
// It is responsible for closing logsChan.
func (c *Client) fetchLogs(ctx context.Context, query ethereum.FilterQuery, logsChan chan<- types.Log, errorChan chan<- error) {
	maxBlocks := uint64(c.blockRangeLimit)
	startBlock := query.FromBlock.Uint64()
	endBlock := query.ToBlock.Uint64()

	for start := startBlock; start <= endBlock; start += maxBlocks {
		currentEnd := start + maxBlocks - 1
		if currentEnd < start {
			currentEnd = endBlock
		} else {
			currentEnd = min(currentEnd, endBlock)
		}

		chunkQuery := ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(start),
			ToBlock:   new(big.Int).SetUint64(currentEnd),
			Addresses: query.Addresses,
			Topics:    query.Topics,
		}

		c.logger.Debug("querying logs", "fromBlock", chunkQuery.FromBlock.Uint64(), "toBlock", chunkQuery.ToBlock.Uint64())

		logs, err := c.client.FilterLogs(ctx, chunkQuery)
		if err != nil {
			errorChan <- fmt.Errorf("failed to retrieve logs for range %d-%d: %w", start, currentEnd, err)
			return
		}

		for _, log := range logs {
			// cache the price update log and skip sending it to the channel
			// if log.Topics[0] == c.priceUpdateTopic {
			// 	c.logCache.Set(&log)
			// 	continue
			// }
			select {
			case logsChan <- log:
			case <-ctx.Done():
				errorChan <- ctx.Err()
				return // stop processing if context is cancelled
			}
		}

		// check cancellation between chunks
		select {
		case <-ctx.Done():
			errorChan <- ctx.Err()
			return
		default:
			// continue to next chunk
		}
	}
}

func (c *Client) filterQuery(postageStampContractAddress common.Address, from, to *big.Int) ethereum.FilterQuery {
	return ethereum.FilterQuery{
		FromBlock: from,
		ToBlock:   to,
		Addresses: []common.Address{
			postageStampContractAddress,
		},
		Topics: [][]common.Hash{
			{
				c.batchCreatedTopic,
				c.batchTopUpTopic,
				c.batchDepthIncreaseTopic,
				c.priceUpdateTopic,
				c.pausedTopic,
			},
		},
	}
}
