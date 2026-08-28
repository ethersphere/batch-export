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

// SaveLogsAsync writes logs to a file asynchronously.
func SaveLogsAsync(ctx context.Context, logChan <-chan types.Log, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	return saveLogs(ctx, logChan, file)
}

func saveLogs(ctx context.Context, logChan <-chan types.Log, w io.WriteCloser) (err error) {
	// OS-buffered writes can surface a failure only when the file is flushed
	// at close time, so a swallowed Close error would report an incomplete
	// export as success.
	defer func() {
		if cerr := w.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("error closing file: %w", cerr))
		}
	}()

	encoder := json.NewEncoder(w)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case logObj, ok := <-logChan:
			if !ok {
				return nil
			}

			if err := encoder.Encode(logObj); err != nil {
				return fmt.Errorf("error encoding log: %w", err)
			}
		}
	}
}
