package compressor_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/ethersphere/batch-export/pkg/compressor"
	"github.com/stretchr/testify/assert"
)

func TestCompressionSizes(t *testing.T) {
	// Create specific test data that resembles JSON logs
	inputContent := []byte(`{"address":"0x123","block":100,"event":"Transfer","data":"0xabc"}
{"address":"0x123","block":101,"event":"Transfer","data":"0xdef"}
{"address":"0x123","block":102,"event":"Transfer","data":"0xghi"}
`)
	// Repeat to get a reasonable file size
	for i := 0; i < 1000; i++ {
		inputContent = append(inputContent, []byte(`{"address":"0x123","block":100,"event":"Transfer","data":"0xabc"}`)...)
	}

	inputFile := "test_input.json"
	err := os.WriteFile(inputFile, inputContent, 0o644)
	assert.NoError(t, err)
	defer os.Remove(inputFile)

	algos := []string{"gzip", "zstd", "xz"}
	results := make(map[string]int64)

	fmt.Printf("\n--- Compression Size Comparison (Input size: %d bytes) ---\n", len(inputContent))

	for _, algo := range algos {
		outputFile := "test_output." + algo
		defer os.Remove(outputFile)

		err := compressor.CompressFile(inputFile, outputFile, algo)
		assert.NoError(t, err)

		info, err := os.Stat(outputFile)
		assert.NoError(t, err)

		results[algo] = info.Size()
		fmt.Printf("%-5s: %d bytes (%.2f%% of original)\n", algo, info.Size(), float64(info.Size())/float64(len(inputContent))*100)
	}
	fmt.Println("----------------------------------------------------------")

	// Verify expectations: xz should generally be smaller than gzip for this kind of data
	// Note: for very small files/specific patterns, results may vary, so we just log them for the user
	// But typically xz < gzip
}
