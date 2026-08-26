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

// writeGzip creates a gzip file holding content and returns its path.
func writeGzip(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "export.ndjson.gzip")

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

// readGzip decompresses the whole file, following every member.
func readGzip(t *testing.T, path string) string {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}

	return string(got)
}

func TestAppendWriterAddsReadableMember(t *testing.T) {
	t.Parallel()

	path := writeGzip(t, "first\nsecond\n")

	w, err := gzipstore.AppendWriter(path)
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}
	if _, err := io.WriteString(w, "third\nfourth\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	const want = "first\nsecond\nthird\nfourth\n"
	if got := readGzip(t, path); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestAppendWriterRepeatedAppends(t *testing.T) {
	t.Parallel()

	path := writeGzip(t, "a\n")

	for _, line := range []string{"b\n", "c\n"} {
		w, err := gzipstore.AppendWriter(path)
		if err != nil {
			t.Fatalf("AppendWriter() error = %v", err)
		}
		if _, err := io.WriteString(w, line); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}

	const want = "a\nb\nc\n"
	if got := readGzip(t, path); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestAppendWriterMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := gzipstore.AppendWriter(filepath.Join(t.TempDir(), "nope.gzip")); err == nil {
		t.Fatal("AppendWriter() error = nil, want an error")
	}
}
