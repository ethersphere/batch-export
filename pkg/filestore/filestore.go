package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ethereum/go-ethereum/core/types"
)

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

// SaveLogsAsync writes logs to a file asynchronously, replacing any file
// already at filePath. The file is closed before returning.
func SaveLogsAsync(ctx context.Context, logChan <-chan types.Log, filePath string) error {
	w, err := CreateWriter(filePath)
	if err != nil {
		return err
	}

	return AppendLogsAsync(ctx, logChan, w, nil)
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
// nil skip writes every log. w is closed before returning, cancellation
// included, so a buffered destination is always flushed.
//
// The close error is joined rather than discarded: on a compressed destination
// Close writes the terminator and footer, so a failure there leaves a truncated
// member that must not be reported as a completed save. errors.Join keeps
// errors.Is working, so a cancelled context still reads as context.Canceled.
func AppendLogsAsync(ctx context.Context, logChan <-chan types.Log, w io.WriteCloser, skip func(types.Log) bool) (err error) {
	defer func() { err = errors.Join(err, w.Close()) }()

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
