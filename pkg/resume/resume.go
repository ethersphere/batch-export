// Package resume locates the point at which a previous export stopped so that
// a new run can continue from there.
package resume

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	// maxLineBytes caps a single line. Exported log lines run to a few
	// hundred bytes, so anything longer is not this tool's output.
	maxLineBytes = 1 << 20
	// bufferSize is how much of a gzip file is buffered per read.
	bufferSize = 64 * 1024
)

var (
	// ErrNotAnExport indicates content this tool never writes. The file was
	// altered after export, so resuming it is refused rather than repaired.
	ErrNotAnExport = errors.New("not an untouched batch-export file")
	// ErrNoLogs indicates a file consistent with tool output that holds no
	// complete entry to resume from: it is empty, or holds only an
	// interrupted first write. The remedy is a fresh export.
	ErrNoLogs = errors.New("no complete log entry found")
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

// PrepareOutput readies outputPath for appending the continuation of the
// export at inputPath. The same file is prepared in place: an interrupted
// final write, if any, is truncated away. "Same file" is not lexical — a
// relative and an absolute spelling of one path, a symlink or hardlink, or
// two names a case-insensitive filesystem folds together all count, detected
// via os.SameFile (device+inode), because os.Create on a distinct-looking
// spelling of the input would truncate it before it could be copied from.
// With genuinely distinct files the input is never modified: its clean
// content is copied raw into outputPath, replacing whatever was there, and
// the interrupted tail is simply not copied. Either way it returns how many
// trailing bytes were left out — every entry they held falls at or after the
// cursor, so the resumed query fetches it again.
func PrepareOutput(c *Cursor, inputPath, outputPath string) (int64, error) {
	if filepath.Clean(inputPath) == filepath.Clean(outputPath) {
		return prepareInPlace(c, inputPath)
	}

	in, err := os.Open(inputPath)
	if err != nil {
		return 0, fmt.Errorf("error opening resume file: %w", err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return 0, fmt.Errorf("error inspecting resume file: %w", err)
	}

	// The same file under two names — relative vs absolute, a symlink or
	// hardlink, a case-insensitive filesystem — is in-place, not copy mode:
	// os.Create would truncate the input we are about to read.
	if outInfo, err := os.Stat(outputPath); err == nil && os.SameFile(info, outInfo) {
		return prepareInPlace(c, inputPath)
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("error creating output file: %w", err)
	}

	_, err = io.CopyN(out, in, c.CleanSize)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("error copying clean content to output file: %w", err)
	}

	return info.Size() - c.CleanSize, nil
}

// prepareInPlace drops the interrupted tail so appending continues from the
// clean boundary. It never truncates past the offset the reader identified.
func prepareInPlace(c *Cursor, path string) (int64, error) {
	if !c.Truncated {
		return 0, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("error inspecting resume file: %w", err)
	}
	if info.Size() <= c.CleanSize {
		return 0, nil
	}

	if err := os.Truncate(path, c.CleanSize); err != nil {
		return 0, fmt.Errorf("error truncating resume file: %w", err)
	}

	return info.Size() - c.CleanSize, nil
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
// It returns ErrNoLogs when there is no complete entry, and ErrNotAnExport
// when the file holds content this tool did not write.
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

// lastCursorPlain validates the tail of a plain NDJSON file and returns a
// cursor for its last complete line. The tool writes one entry per line in a
// single call, so only the tail needs examining: the last newline-terminated
// line must parse as a log entry, and the only bytes allowed after it are a
// single interrupted write — a trailing fragment with no newline.
func lastCursorPlain(file *os.File, size int64) (*Cursor, error) {
	window := min(size, 2*maxLineBytes)
	offset := size - window

	buf := make([]byte, window)
	if _, err := file.ReadAt(buf, offset); err != nil {
		return nil, fmt.Errorf("error reading resume file: %w", err)
	}

	nl := bytes.LastIndexByte(buf, '\n')
	if nl < 0 {
		// No newline at all: an interrupted first write, unless the file is
		// longer than any single line the tool writes.
		if size > maxLineBytes {
			return nil, fmt.Errorf("%w: %d bytes without a newline", ErrNotAnExport, size)
		}
		return nil, ErrNoLogs
	}
	if tail := window - int64(nl) - 1; tail > maxLineBytes {
		return nil, fmt.Errorf("%w: %d bytes without a newline after offset %d", ErrNotAnExport, tail, offset+int64(nl)+1)
	}

	start := bytes.LastIndexByte(buf[:nl], '\n') + 1
	if start == 0 && offset > 0 {
		return nil, fmt.Errorf("%w: final line is over %d bytes long", ErrNotAnExport, nl)
	}
	cursor, err := parseCursor(buf[start:nl])
	if err != nil {
		return nil, fmt.Errorf("%w: final complete line at offset %d is not a log entry: %w", ErrNotAnExport, offset+int64(start), err)
	}
	cursor.CleanSize = offset + int64(nl) + 1

	return cursor, nil
}

// lastCursorGzip walks a gzip file one member at a time and returns a cursor
// for the last entry inside the cleanly terminated prefix. Gzip cannot be
// seeked, so the whole stream is decompressed; every complete line is
// validated on the way. A member cut short by an interrupted write ends the
// walk: CleanSize stays at the last clean member boundary, and the truncated
// member's content — about to be discarded and re-fetched — never advances
// the cursor.
func lastCursorGzip(file *os.File) (*Cursor, error) {
	counter := &countingReader{reader: bufio.NewReaderSize(file, bufferSize)}

	reader, err := gzip.NewReader(counter)
	if err != nil {
		return nil, fmt.Errorf("error opening gzip resume file: %w", err)
	}
	defer reader.Close()

	var (
		cursor    *Cursor
		cleanSize int64
		sawClean  bool
	)
	for {
		// Reset turns multistream back on, so it must be switched off per
		// member, not just for the first.
		reader.Multistream(false)

		last, err := scanMember(reader)
		switch {
		case errors.Is(err, ErrNotAnExport):
			return nil, err
		case err != nil && !truncationShaped(err):
			return nil, fmt.Errorf("error reading gzip resume file: %w", err)
		case err != nil:
			// The member never got its trailer: an interrupted final write.
			return gzipResult(cursor, cleanSize, sawClean)
		}

		if last != nil {
			cursor = last
		}
		cleanSize, sawClean = counter.read, true

		// A clean end of file makes Reset report io.EOF; a partly written
		// next member fails to parse as a header and also ends the walk.
		if err := reader.Reset(counter); err != nil {
			if errors.Is(err, io.EOF) || truncationShaped(err) {
				return gzipResult(cursor, cleanSize, sawClean)
			}
			return nil, fmt.Errorf("error reading gzip resume file: %w", err)
		}
	}
}

// gzipResult finalizes the walk: a file with no clean member, or none holding
// an entry, has nothing to resume from.
func gzipResult(cursor *Cursor, cleanSize int64, sawClean bool) (*Cursor, error) {
	if !sawClean || cursor == nil {
		return nil, ErrNoLogs
	}
	cursor.CleanSize = cleanSize

	return cursor, nil
}

// truncationShaped reports whether err is what an interrupted write produces,
// as opposed to a real read failure that must not be mistaken for one.
func truncationShaped(err error) bool {
	var corrupt flate.CorruptInputError

	return errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, gzip.ErrHeader) ||
		errors.Is(err, gzip.ErrChecksum) ||
		errors.As(err, &corrupt)
}

// scanMember reads one gzip member's NDJSON and returns a cursor for its last
// entry, nil when the member is empty. A nil error means the member decoded
// to a clean end of stream on a line boundary. The tool writes members
// holding whole log lines only, so a complete line that does not parse, or a
// clean member ending mid-line, is foreign content (ErrNotAnExport); any
// other read error is returned as-is for the caller to classify.
func scanMember(r io.Reader) (*Cursor, error) {
	buffered := bufio.NewReaderSize(r, bufferSize)

	var (
		last *Cursor
		line []byte
	)
	for {
		chunk, err := buffered.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return nil, fmt.Errorf("%w: line exceeds %d bytes", ErrNotAnExport, maxLineBytes)
		}

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				return nil, fmt.Errorf("%w: gzip member ends mid-line", ErrNotAnExport)
			}
			return last, nil
		case err != nil:
			return last, err
		}

		cursor, err := parseCursor(line)
		if err != nil {
			return nil, fmt.Errorf("%w: line is not a log entry: %w", ErrNotAnExport, err)
		}
		last = cursor
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

// parseCursor builds a cursor from a single NDJSON line. Callers wrap the
// returned error with the sentinel that fits their context.
func parseCursor(line []byte) (*Cursor, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, errors.New("blank line")
	}

	var parsed cursorLine
	if err := json.Unmarshal(line, &parsed); err != nil {
		return nil, err
	}
	if parsed.BlockNumber == nil || parsed.LogIndex == nil {
		return nil, errors.New("missing blockNumber or logIndex")
	}

	return &Cursor{
		BlockNumber: uint64(*parsed.BlockNumber),
		LogIndex:    uint(*parsed.LogIndex),
	}, nil
}
