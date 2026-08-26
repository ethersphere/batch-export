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
	// bufferSize is how much of a gzip file is buffered per read.
	bufferSize = 64 * 1024
)

var (
	// ErrNoLogs indicates that a file holds no complete log entry to resume
	// from.
	ErrNoLogs = errors.New("no complete log entry found")
	// ErrNoCleanBoundary indicates that a file holds log entries but no point
	// at which its content is known to be complete, so nothing can be appended
	// to it without corrupting what is already there.
	ErrNoCleanBoundary = errors.New("file was left partially written and has no clean boundary to append at")
	// errLineTooLong indicates that a line ran past maxLineBytes, which means
	// the data is not the NDJSON an export writes.
	errLineTooLong = errors.New("log line exceeds the maximum length")
)

// Cursor marks the last log entry saved by a previous export, together with
// the point up to which that export's file is known to be complete.
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
	// CleanSize is the byte offset at which the file's recoverable content
	// ends. It always falls just past the entry the cursor points at: for
	// plain NDJSON just past that line's newline, for gzip just past the
	// member the line ends in. Bytes beyond it are a partial write left by an
	// interrupted run and must be discarded before anything is appended.
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
// fields are pointers so that a line missing either one can be rejected.
type cursorLine struct {
	BlockNumber *hexutil.Uint64 `json:"blockNumber"`
	LogIndex    *hexutil.Uint   `json:"logIndex"`
}

// Read returns a cursor for the last complete log entry in the file at path.
// The file may be plain NDJSON or gzip; the format is detected from its
// leading bytes rather than its extension.
//
// Only an entry that is complete and properly terminated counts: a plain line
// must end in a newline, and a compressed line must sit inside a gzip member
// that decoded to a clean end of stream. The cursor therefore also reports
// where the file's recoverable content ends (CleanSize) and whether a partial
// write follows it (Truncated). A caller that appends must discard everything
// past CleanSize first, which is lossless: every entry discarded that way is
// re-fetched by the resumed query.
//
// It returns ErrNoLogs when the file holds no complete entry at all, and
// ErrNoCleanBoundary when it holds entries but no point at which appending is
// safe.
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

// lastCursorPlain walks a plain NDJSON file of the given size backwards a
// window at a time and returns a cursor for the last line that both parses and
// is terminated by a newline. json.Encoder writes a value and its newline in a
// single call, so a trailing line without one was cut short by an interrupted
// run: it is passed over, and CleanSize points just past the newline of the
// last line that was written whole.
func lastCursorPlain(file *os.File, size int64) (*Cursor, error) {
	var (
		offset = size
		// end is the offset one past the last byte of the region currently
		// held in window, and terminated reports whether the file byte at end
		// is the newline closing that region's last line. Only a terminated
		// region can yield a cursor.
		end        = size
		terminated bool
		// carry holds bytes from the window just read that precede its first
		// newline. They belong to a line whose start lies in the next window
		// back.
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
// for the last log line that lies wholly inside a cleanly terminated member.
// Gzip cannot be seeked, so the whole stream is decompressed.
//
// A member left half-written by an interrupted run cannot decode cleanly: its
// CRC and length trailer are missing, so the reader reports an error instead
// of io.EOF. The walk stops there and CleanSize is the offset just past the
// last member that did end cleanly, which is a member boundary and so a valid
// place to concatenate the next one. Lines are carried across member
// boundaries, so a line split between two members is still recognised, and a
// member that ends mid-line is not treated as a boundary at all.
func lastCursorGzip(file *os.File) (*Cursor, error) {
	counter := &countingReader{reader: bufio.NewReaderSize(file, bufferSize)}

	reader, err := gzip.NewReader(counter)
	if err != nil {
		return nil, fmt.Errorf("error opening gzip resume file: %w", err)
	}
	defer reader.Close()

	var (
		// clean is the cursor as of cleanSize, while pending also covers the
		// member being read; pending is promoted only once that member is
		// known to have ended cleanly and on a line boundary.
		clean, pending *Cursor
		cleanSize      int64
		carry          []byte
	)

	for {
		// Reset turns multistream back on, so it has to be switched off for
		// every member rather than only for the first.
		reader.Multistream(false)

		cursor, tail, err := scanLines(reader, carry)
		if cursor != nil {
			pending = cursor
		}
		if err != nil {
			// The member did not decode to a clean end of stream, so
			// everything it holds is part of an interrupted write.
			break
		}
		if len(tail) == 0 {
			clean, cleanSize = pending, counter.read
		}
		carry = tail

		// A clean end of file makes Reset report io.EOF; anything else is a
		// member header that was only partly written.
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
// both parses and is terminated by a newline. carry is prepended to the first
// line, so a line split across two gzip members is reassembled, and the
// trailing bytes not yet terminated by a newline are returned so the next
// member can complete them. The returned error is nil only when r ended at a
// clean io.EOF; a truncated member, a checksum mismatch, an over-long line and
// a genuine I/O failure are all reported rather than passed over, because each
// of them means the bytes after the last good line cannot be trusted.
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

// countingReader counts the bytes consumed from the reader it wraps. It
// implements io.ByteReader as well as io.Reader so that gzip.Reader reads from
// it directly rather than wrapping it in a bufio.Reader of its own; without
// that the count would run ahead of the reader's real position and no member
// boundary could be observed exactly.
type countingReader struct {
	reader *bufio.Reader
	// read is the number of bytes handed out so far.
	read int64
}

// Read implements io.Reader.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read += int64(n)

	return n, err
}

// ReadByte implements io.ByteReader.
func (c *countingReader) ReadByte() (byte, error) {
	b, err := c.reader.ReadByte()
	if err == nil {
		c.read++
	}

	return b, err
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
