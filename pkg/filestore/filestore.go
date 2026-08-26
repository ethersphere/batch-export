package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ethereum/go-ethereum/core/types"
)

// SaveLogsAsync writes logs to a file asynchronously, replacing any file
// already at filePath.
func SaveLogsAsync(ctx context.Context, logChan <-chan types.Log, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer file.Close()

	return writeLogs(ctx, logChan, file, nil)
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
// nil skip writes every log. The writer is closed before returning, including
// when the context is cancelled, so a buffered destination is always flushed.
func AppendLogsAsync(ctx context.Context, logChan <-chan types.Log, w io.WriteCloser, skip func(types.Log) bool) error {
	defer w.Close()

	return writeLogs(ctx, logChan, w, skip)
}

// writeLogs encodes logs from logChan to w as NDJSON until the channel is
// closed or the context is cancelled.
func writeLogs(ctx context.Context, logChan <-chan types.Log, w io.Writer, skip func(types.Log) bool) error {
	encoder := json.NewEncoder(w)

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

			if err := encoder.Encode(logObj); err != nil {
				return fmt.Errorf("error encoding log: %w", err)
			}
		}
	}
}
