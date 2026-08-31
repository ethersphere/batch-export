package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// CreateWriter creates the file at filePath, replacing anything already there.
// It is separate from writing so a caller can fail before it starts producing
// logs it would have nowhere to put.
func CreateWriter(filePath string) (io.WriteCloser, error) {
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("error creating file: %w", err)
	}

	return file, nil
}

// AppendWriter opens an existing NDJSON file for appending.
func AppendWriter(filePath string) (io.WriteCloser, error) {
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("error opening file for appending: %w", err)
	}

	return file, nil
}

// AppendLogsAsync writes logs to w asynchronously, keeping whatever the
// destination already holds. Logs for which skip reports true are dropped; a
// nil skip writes every log. When slim is true, each log is encoded with only
// the fields Bee consumes; otherwise the full geth types.Log JSON shape is
// emitted. w is closed before returning, cancellation included, so a buffered
// destination is always flushed.
//
// The close error is joined rather than discarded: on a compressed destination
// Close writes the terminator and footer, so a failure there leaves a truncated
// member that must not be reported as a completed save. errors.Join keeps
// errors.Is working, so a cancelled context still reads as context.Canceled.
func AppendLogsAsync(ctx context.Context, logChan <-chan types.Log, w io.WriteCloser, skip func(types.Log) bool, slim bool) (err error) {
	defer func() { err = errors.Join(err, w.Close()) }()

	return writeLogs(ctx, logChan, w, skip, slim)
}

// writeLogs encodes logs from logChan to w as NDJSON until the channel is
// closed or the context is cancelled.
func writeLogs(ctx context.Context, logChan <-chan types.Log, w io.Writer, skip func(types.Log) bool, slim bool) error {
	encoder := json.NewEncoder(w)

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

			if skip != nil && skip(logObj) {
				continue
			}

			if err := encode(logObj); err != nil {
				return fmt.Errorf("error encoding log: %w", err)
			}
		}
	}
}
