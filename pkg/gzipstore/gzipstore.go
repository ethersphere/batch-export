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

	gzipWriter := gzip.NewWriter(outputFile)
	defer gzipWriter.Close()

	if _, err := io.Copy(gzipWriter, inputFile); err != nil {
		return fmt.Errorf("failed to write compressed data to '%s': %w", outputFilePath, err)
	}

	return nil
}

// AppendWriter opens an existing gzip file and returns a writer that adds a new
// gzip member to it. Concatenated members form a valid gzip stream, so readers
// see one continuous file and the existing bytes are never rewritten. The
// caller must close the writer to flush the member.
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

// Close finishes the gzip member, then closes the file. Both are attempted even
// if the first fails, so the descriptor is never leaked.
func (w *memberWriter) Close() error {
	if err := errors.Join(w.gzip.Close(), w.file.Close()); err != nil {
		return fmt.Errorf("failed to close gzip file '%s': %w", w.file.Name(), err)
	}

	return nil
}
