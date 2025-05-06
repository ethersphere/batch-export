package gzipstore

import (
	"compress/gzip"
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

	// create a new gzip writer that writes to the output file
	gzipWriter := gzip.NewWriter(outputFile)
	defer gzipWriter.Close()

	// copy the contents from the input file to the gzip writer
	_, err = io.Copy(gzipWriter, inputFile)
	if err != nil {
		return fmt.Errorf("failed to write compressed data to '%s': %w", outputFilePath, err)
	}

	return nil
}
