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

// AppendWriter opens an existing gzip file for appending and returns a writer
// that adds a new gzip member to it. Concatenated members form a valid gzip
// stream, so readers see one continuous file and the existing bytes are never
// rewritten. The caller must close the writer to flush the member.
func AppendWriter(filePath string) (io.WriteCloser, error) {
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip file '%s' for appending: %w", filePath, err)
	}

	return &memberWriter{file: file, gzip: gzip.NewWriter(file)}, nil
}

// memberWriter writes one gzip member and owns the file it was opened from.
type memberWriter struct {
	file *os.File
	gzip *gzip.Writer
}

func (w *memberWriter) Write(p []byte) (int, error) {
	return w.gzip.Write(p)
}

// Close finishes the gzip member and then closes the file. Both are attempted
// even if the first fails, so the descriptor is never leaked. Closing the
// member writes the data the file needs to be readable at all, so the error
// names the file it belongs to.
func (w *memberWriter) Close() error {
	if err := errors.Join(w.gzip.Close(), w.file.Close()); err != nil {
		return fmt.Errorf("failed to close gzip file '%s': %w", w.file.Name(), err)
	}

	return nil
}
