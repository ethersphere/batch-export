package gzipstore

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
)

// CompressFile compresses the specified input file into the specified output gzip file.
func CompressFile(inputFilePath string, outputFilePath string) error {
	// open the input file for reading
	inputFile, err := os.Open(inputFilePath)
	if err != nil {
		return fmt.Errorf("failed to open input file '%s': %w", inputFilePath, err)
	}
	defer inputFile.Close()

	// create the output file for writing
	outputFile, err := os.Create(outputFilePath)
	if err != nil {
		return fmt.Errorf("failed to create output file '%s': %w", outputFilePath, err)
	}
	defer outputFile.Close()

	return compress(outputFile, inputFile)
}

func compress(dst io.Writer, src io.Reader) (err error) {
	gzipWriter := gzip.NewWriter(dst)
	// Close flushes the remaining compressed bytes and the gzip footer; a
	// swallowed error here would report a truncated archive as success.
	defer func() {
		if cerr := gzipWriter.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("failed to finalize gzip stream: %w", cerr))
		}
	}()

	// copy the contents from the source to the gzip writer
	if _, err := io.Copy(gzipWriter, src); err != nil {
		return fmt.Errorf("failed to write compressed data: %w", err)
	}

	return nil
}
