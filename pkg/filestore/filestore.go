package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// SlimLog mirrors the subset of geth's types.Log that Bee's snapshot consumer
// reads, plus logIndex, which the --resume cursor needs to locate the last
// exported log within its block. Field order, JSON tags, and hex value shapes
// match geth's generated MarshalJSON, so slim and full snapshots are
// interchangeable on the Bee side.
type SlimLog struct {
	Address     common.Address `json:"address"`
	Topics      []common.Hash  `json:"topics"`
	Data        hexutil.Bytes  `json:"data"`
	BlockNumber hexutil.Uint64 `json:"blockNumber"`
	TxHash      common.Hash    `json:"transactionHash"`
	Index       hexutil.Uint   `json:"logIndex"`
}

func NewSlimLog(l types.Log) SlimLog {
	return SlimLog{
		Address:     l.Address,
		Topics:      l.Topics,
		Data:        l.Data,
		BlockNumber: hexutil.Uint64(l.BlockNumber),
		TxHash:      l.TxHash,
		Index:       hexutil.Uint(l.Index),
	}
}

// SaveLogsAsync writes logs to a file asynchronously.
// When slim is true, each log is encoded with only the fields Bee consumes;
// otherwise the full geth types.Log JSON shape is emitted.
func SaveLogsAsync(ctx context.Context, logChan <-chan types.Log, filePath string, slim bool) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)

	encode := func(l types.Log) error { return encoder.Encode(l) }
	if slim {
		encode = func(l types.Log) error { return encoder.Encode(NewSlimLog(l)) }
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case logObj, ok := <-logChan:
			if !ok {
				return nil
			}

			if err := encode(logObj); err != nil {
				return fmt.Errorf("error encoding log: %w", err)
			}
		}
	}
}
