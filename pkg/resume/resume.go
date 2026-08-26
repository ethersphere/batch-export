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
	// windowSize is how much of a plain NDJSON file is read per backward step.
	windowSize = 64 * 1024
	// maxLineBytes caps a single line, so a file without newlines is not read
	// into memory whole. Exported log lines run to a few hundred bytes.
	maxLineBytes = 1 << 20
	// bufferSize is how much of a gzip file is buffered per read.
	bufferSize = 64 * 1024
)

var (
	// ErrNoLogs indicates that a file holds no complete log entry.
	ErrNoLogs = errors.New("no complete log entry found")
	// ErrNoCleanBoundary indicates that a file holds log entries but no offset
	// at which appending is safe.
	ErrNoCleanBoundary = errors.New("file was left partially written and has no clean boundary to append at")
	// errLineTooLong indicates a line ran past maxLineBytes, so the data is not
	// the NDJSON an export writes.
	errLineTooLong = errors.New("log line exceeds the maximum length")
)

// Cursor marks the last log entry saved by a previous export, together with
// the point up to which that export's file is known to be complete.
type Cursor struct {
	// BlockNumber is the block of the last saved log. A resumed export
	// re-queries it, because an interrupted run may have saved only part of it.
	BlockNumber uint64
	// LogIndex is the index of the last saved log within BlockNumber.
	LogIndex uint
	// Compressed reports whether the file holds gzip rather than plain NDJSON.
	Compressed bool
	// CleanSize is the offset just past the entry the cursor points at: past
	// that line's newline, or past the gzip member it ends in. Bytes beyond it
	// are a partial write and must be discarded before anything is appended.
	CleanSize int64
	// Truncated reports whether any bytes follow CleanSize.
	Truncated bool
}

// Skip reports whether l was already written to the file the cursor came from.
func (c *Cursor) Skip(l types.Log) bool {
	if l.BlockNumber != c.BlockNumber {
		return l.BlockNumber < c.BlockNumber
	}
	return l.Index <= c.LogIndex
}

// cursorLine is the part of an exported log line a cursor is built from. Both
// fields are pointers so a line missing either one can be rejected.
type cursorLine struct {
	BlockNumber *hexutil.Uint64 `json:"blockNumber"`
	LogIndex    *hexutil.Uint   `json:"logIndex"`
}

// Read returns a cursor for the last complete log entry in the file at path,
// detecting plain NDJSON or gzip from the leading bytes rather than the
// extension.
//
// Only a properly terminated entry counts: a plain line must end in a newline,
// a compressed one must sit in a member that decoded to a clean end of stream.
// A caller that appends must first discard everything past CleanSize.
//
// It returns ErrNoLogs when there is no complete entry, and ErrNoCleanBoundary
// when there are entries but no offset at which appending is safe.
func Read(path string) (*Cursor, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening resume file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("error inspecting resume file: %w", err)
	}

	compressed, err := isGzip(file)
	if err != nil {
		return nil, fmt.Errorf("error reading resume file: %w", err)
	}

	var cursor *Cursor
	if compressed {
		cursor, err = lastCursorGzip(file)
	} else {
		cursor, err = lastCursorPlain(file, info.Size())
	}
	if err != nil {
		return nil, err
	}

	cursor.Compressed = compressed
	cursor.Truncated = cursor.CleanSize < info.Size()

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
// returns a cursor for the last line that parses and ends in a newline.
// json.Encoder writes a value and its newline in one call, so a trailing line
// without one was cut short by an interrupted run and is passed over.
func lastCursorPlain(file *os.File, size int64) (*Cursor, error) {
	var (
		offset = size
		// end is one past the last byte held in window; terminated reports
		// whether the file byte at end is the newline closing that region's
		// last line. Only a terminated region can yield a cursor.
		end        = size
		terminated bool
		// carry holds the bytes before the window's first newline, belonging to
		// a line that starts in the next window back.
		carry []byte
	)

	for offset > 0 {
		n := int64(windowSize)
		if offset < n {
			n = offset
		}
		offset -= n

		window := make([]byte, n)
		if _, err := file.ReadAt(window, offset); err != nil {
			return nil, fmt.Errorf("error reading resume file: %w", err)
		}
		window = append(window, carry...)

		for {
			i := bytes.LastIndexByte(window, '\n')
			if i < 0 {
				break
			}
			if terminated {
				if cursor, err := parseCursor(window[i+1:]); err == nil {
					cursor.CleanSize = end + 1
					return cursor, nil
				}
			}
			window = window[:i]
			end, terminated = offset+int64(i), true
		}

		carry = window
		if len(carry) > maxLineBytes {
			return nil, ErrNoLogs
		}
	}

	if terminated {
		if cursor, err := parseCursor(carry); err == nil {
			cursor.CleanSize = end + 1
			return cursor, nil
		}
	}

	return nil, ErrNoLogs
}

// lastCursorGzip walks a gzip file one member at a time and returns a cursor
// for the last log line inside a cleanly terminated member. Gzip cannot be
// seeked, so the whole stream is decompressed.
//
// A half-written member has no CRC and length trailer, so it reports an error
// rather than io.EOF; the walk stops there and CleanSize is the end of the last
// member that did terminate cleanly, which is a valid place to concatenate the
// next one. Lines are carried across members, so one split between two members
// is still recognised and a member ending mid-line is not a boundary.
func lastCursorGzip(file *os.File) (*Cursor, error) {
	counter := &countingReader{reader: bufio.NewReaderSize(file, bufferSize)}

	reader, err := gzip.NewReader(counter)
	if err != nil {
		return nil, fmt.Errorf("error opening gzip resume file: %w", err)
	}
	defer reader.Close()

	var (
		// clean is the cursor as of cleanSize; pending also covers the member
		// being read and is promoted only once that member is known to have
		// ended cleanly and on a line boundary.
		clean, pending *Cursor
		cleanSize      int64
		carry          []byte
	)

	for {
		// Reset turns multistream back on, so it must be switched off per
		// member, not just for the first.
		reader.Multistream(false)

		cursor, tail, err := scanLines(reader, carry)
		if cursor != nil {
			pending = cursor
		}
		if err != nil {
			// Member did not reach a clean end of stream, so everything it
			// holds belongs to an interrupted write.
			break
		}
		if len(tail) == 0 {
			clean, cleanSize = pending, counter.read
		}
		carry = tail

		// A clean end of file makes Reset report io.EOF; anything else is a
		// partly written member header.
		if err := reader.Reset(counter); err != nil {
			break
		}
	}

	if clean == nil {
		if pending == nil {
			return nil, ErrNoLogs
		}
		return nil, ErrNoCleanBoundary
	}

	clean.CleanSize = cleanSize

	return clean, nil
}

// scanLines reads NDJSON from r and returns a cursor for the last line that
// parses and ends in a newline. carry is prepended to the first line so a line
// split across two gzip members is reassembled, and the unterminated trailing
// bytes are returned for the next member to complete.
//
// The error is nil only when r reached a clean io.EOF. Truncation, a checksum
// mismatch, an over-long line and an I/O failure are all reported, because each
// means the bytes after the last good line cannot be trusted.
func scanLines(r io.Reader, carry []byte) (*Cursor, []byte, error) {
	buffered := bufio.NewReaderSize(r, bufferSize)

	var last *Cursor

	line := carry
	for {
		chunk, err := buffered.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return last, nil, errLineTooLong
		}

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return last, line, nil
		case err != nil:
			return last, nil, fmt.Errorf("error reading gzip resume file: %w", err)
		}

		if cursor, err := parseCursor(line); err == nil {
			last = cursor
		}
		line = line[:0]
	}
}

// countingReader counts the bytes consumed from the reader it wraps.
//
// It must keep implementing io.ByteReader: that is what makes gzip.Reader read
// from it directly instead of wrapping it in a bufio.Reader of its own. Without
// it the count runs ahead of the real position and member boundaries can no
// longer be observed exactly.
type countingReader struct {
	reader *bufio.Reader
	// read is the number of bytes handed out so far.
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read += int64(n)

	return n, err
}

func (c *countingReader) ReadByte() (byte, error) {
	b, err := c.reader.ReadByte()
	if err == nil {
		c.read++
	}

	return b, err
}

// parseCursor builds a cursor from a single NDJSON line, rejecting blank and
// truncated lines and any log missing a block number or log index.
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
