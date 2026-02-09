package compressor

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// CompressFile compresses the specified input file into the specified output files using the given algorithm.
func CompressFile(inputFilePath string, outputFilePath string, algo string) error {
	inputFile, err := os.Open(inputFilePath)
	if err != nil {
		return fmt.Errorf("failed to open input file '%s': %w", inputFilePath, err)
	}
	defer inputFile.Close()

	outputFile, err := os.Create(outputFilePath)
	if err != nil {
		return fmt.Errorf("failed to create output file '%s': %w", outputFilePath, err)
	}
	defer outputFile.Close()

	var writer io.WriteCloser

	switch algo {
	case "gzip":
		writer = gzip.NewWriter(outputFile)
	case "zstd":
		writer, err = zstd.NewWriter(outputFile)
		if err != nil {
			return fmt.Errorf("failed to create zstd writer: %w", err)
		}
	case "xz":
		writer, err = xz.NewWriter(outputFile)
		if err != nil {
			return fmt.Errorf("failed to create xz writer: %w", err)
		}
	default:
		return fmt.Errorf("unsupported compression algorithm: %s", algo)
	}
	defer writer.Close()

	_, err = io.Copy(writer, inputFile)
	if err != nil {
		return fmt.Errorf("failed to compress data: %w", err)
	}

	return nil
}
