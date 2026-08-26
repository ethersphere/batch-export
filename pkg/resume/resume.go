// Package resume locates the point at which a previous export stopped so that
// a new run can continue from there.
package resume

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	// windowSize is how much of a plain NDJSON file is read at a time when
	// walking backwards from the end.
	windowSize = 64 * 1024
	// maxLineBytes caps how long a single line may be. Exported log lines run
	// to a few hundred bytes, so anything beyond this is corruption, and the
	// cap keeps a file without newlines from being read into memory whole.
	maxLineBytes = 1 << 20
)

// ErrNoLogs indicates that a file holds no complete log entry to resume from.
var ErrNoLogs = errors.New("no complete log entry found")

// Cursor marks the last log entry saved by a previous export.
type Cursor struct {
	// BlockNumber is the block of the last saved log. A resumed export
	// re-queries this block, because an interrupted run may have saved only
	// some of its logs.
	BlockNumber uint64
	// LogIndex is the index of the last saved log within BlockNumber.
	LogIndex uint
	// Compressed reports whether the file holds gzip data rather than plain
	// NDJSON.
	Compressed bool
}

// Skip reports whether l was already written to the file the cursor came from.
func (c *Cursor) Skip(l types.Log) bool {
	if l.BlockNumber != c.BlockNumber {
		return l.BlockNumber < c.BlockNumber
	}
	return l.Index <= c.LogIndex
}

// cursorLine is the part of an exported log line a cursor is built from. Both
// fields are pointers so that a line missing either one can be rejected.
type cursorLine struct {
	BlockNumber *hexutil.Uint64 `json:"blockNumber"`
	LogIndex    *hexutil.Uint   `json:"logIndex"`
}

// Read returns a cursor for the last complete log entry in the file at path.
// The file may be plain NDJSON or gzip; the format is detected from its
// leading bytes rather than its extension. Lines that do not parse are
// skipped, which discards a partial line left behind by an interrupted write.
func Read(path string) (*Cursor, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening resume file: %w", err)
	}
	defer file.Close()

	compressed, err := isGzip(file)
	if err != nil {
		return nil, fmt.Errorf("error reading resume file: %w", err)
	}

	var cursor *Cursor
	if compressed {
		cursor, err = lastCursorGzip(file)
	} else {
		cursor, err = lastCursorPlain(file)
	}
	if err != nil {
		return nil, err
	}

	cursor.Compressed = compressed

	return cursor, nil
}

// isGzip reports whether file starts with the gzip magic bytes.
func isGzip(file *os.File) (bool, error) {
	var magic [2]byte

	n, err := file.ReadAt(magic[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if n < len(magic) {
		return false, nil
	}

	return magic[0] == 0x1f && magic[1] == 0x8b, nil
}

// lastCursorPlain walks a plain NDJSON file backwards a window at a time and
// returns a cursor for the last line that parses.
func lastCursorPlain(file *os.File) (*Cursor, error) {
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("error seeking resume file: %w", err)
	}

	// carry holds bytes from the window just read that precede its first
	// newline. They belong to a line whose start lies in the next window back.
	var carry []byte

	for offset > 0 {
		size := int64(windowSize)
		if offset < size {
			size = offset
		}
		offset -= size

		window := make([]byte, size)
		if _, err := file.ReadAt(window, offset); err != nil {
			return nil, fmt.Errorf("error reading resume file: %w", err)
		}
		window = append(window, carry...)

		for {
			i := bytes.LastIndexByte(window, '\n')
			if i < 0 {
				break
			}
			if cursor, err := parseCursor(window[i+1:]); err == nil {
				return cursor, nil
			}
			window = window[:i]
		}

		carry = window
		if len(carry) > maxLineBytes {
			return nil, ErrNoLogs
		}
	}

	return parseCursor(carry)
}

// lastCursorGzip returns a cursor for the last line that parses in a gzip
// file. Gzip cannot be seeked, so the whole stream is decompressed. A stream
// truncated by an interrupted run still yields the last line it did decode.
func lastCursorGzip(file *os.File) (*Cursor, error) {
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("error opening gzip resume file: %w", err)
	}
	defer reader.Close()

	// Multistream is on by default, so concatenated members read as one stream.
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLineBytes)

	var last *Cursor
	for scanner.Scan() {
		if cursor, err := parseCursor(scanner.Bytes()); err == nil {
			last = cursor
		}
	}

	if last != nil {
		return last, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading gzip resume file: %w", err)
	}

	return nil, ErrNoLogs
}

// parseCursor builds a cursor from a single NDJSON line. It rejects blank and
// truncated lines, and logs that carry no block number.
func parseCursor(line []byte) (*Cursor, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, ErrNoLogs
	}

	var parsed cursorLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return nil, ErrNoLogs
	}
	if parsed.BlockNumber == nil || parsed.LogIndex == nil {
		return nil, ErrNoLogs
	}

	return &Cursor{
		BlockNumber: uint64(*parsed.BlockNumber),
		LogIndex:    uint(*parsed.LogIndex),
	}, nil
}
