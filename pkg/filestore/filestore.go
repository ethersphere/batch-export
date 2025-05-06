package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/core/types"
)

// SaveLogsAsync writes logs to a file asynchronously.
func SaveLogsAsync(ctx context.Context, logChan <-chan types.Log, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)

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
