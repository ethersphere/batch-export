package gzipstore_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethersphere/batch-export/pkg/gzipstore"
)

func TestCompressFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "in.ndjson")
	outputPath := filepath.Join(dir, "out.gzip")

	content := []byte("{\"blockNumber\":\"0x1\"}\n{\"blockNumber\":\"0x2\"}\n")
	if err := os.WriteFile(inputPath, content, 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	if err := gzipstore.CompressFile(inputPath, outputPath); err != nil {
		t.Fatalf("CompressFile: %v", err)
	}

	file, err := os.Open(outputPath)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gzipReader.Close()

	got, err := io.ReadAll(gzipReader)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("round trip mismatch: got %q want %q", got, content)
	}
}
