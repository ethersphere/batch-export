// Package verify checks that a refreshed snapshot is a strict extension of
// the snapshot it was resumed from: everything the old file held, byte for
// byte and in order, followed only by newer entries.
package verify

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/ethersphere/batch-export/pkg/resume"
)

var (
	// ErrMismatch indicates the new snapshot does not begin with the old
	// snapshot's content: an entry was dropped, mutated, or reordered.
	ErrMismatch = errors.New("new snapshot does not extend the old snapshot")
	// ErrOrder indicates an appended entry out of (blockNumber, logIndex)
	// order relative to what precedes it.
	ErrOrder = errors.New("appended entries out of order")
	// ErrTruncatedNew indicates the new snapshot ends in an interrupted
	// write; a completed export never does.
	ErrTruncatedNew = errors.New("new snapshot ends in an interrupted write")
)

// chunkSize is how many bytes of both snapshots are compared per read.
const chunkSize = 64 * 1024

// Result reports what a successful verification established.
type Result struct {
	// LastBlock is the block number of the new snapshot's final entry.
	LastBlock uint64
	// Appended is how many entries the new snapshot adds.
	Appended int
	// OldTruncated reports whether the old snapshot carried an interrupted
	// tail, excluded from the comparison the way resuming excludes it from
	// the copy.
	OldTruncated bool
}

// Verify checks that the snapshot at newPath extends the one at oldPath.
func Verify(oldPath, newPath string) (Result, error) {
	oldCursor, err := resume.Read(oldPath)
	if err != nil {
		return Result{}, fmt.Errorf("old snapshot: %w", err)
	}
	newCursor, err := resume.Read(newPath)
	if err != nil {
		return Result{}, fmt.Errorf("new snapshot: %w", err)
	}
	if newCursor.Truncated {
		return Result{}, ErrTruncatedNew
	}

	oldClean, err := resume.OpenClean(oldPath, oldCursor)
	if err != nil {
		return Result{}, fmt.Errorf("old snapshot: %w", err)
	}
	defer oldClean.Close()

	newClean, err := resume.OpenClean(newPath, newCursor)
	if err != nil {
		return Result{}, fmt.Errorf("new snapshot: %w", err)
	}
	defer newClean.Close()

	newBuffered := bufio.NewReaderSize(newClean, chunkSize)
	if err := comparePrefix(oldClean, newBuffered); err != nil {
		return Result{}, err
	}

	appended, err := checkTail(newBuffered, oldCursor)
	if err != nil {
		return Result{}, err
	}

	return Result{
		LastBlock:    newCursor.BlockNumber,
		Appended:     appended,
		OldTruncated: oldCursor.Truncated,
	}, nil
}

// comparePrefix requires every byte of oldR to appear at the start of newR.
func comparePrefix(oldR io.Reader, newR *bufio.Reader) error {
	oldBuf := make([]byte, chunkSize)
	newBuf := make([]byte, chunkSize)

	var offset int64
	for {
		n, err := oldR.Read(oldBuf)
		if n > 0 {
			_, rerr := io.ReadFull(newR, newBuf[:n])
			switch {
			// A short new file is a mismatch; anything else is a read
			// failure and must not be reported as if the snapshot were short.
			case errors.Is(rerr, io.EOF), errors.Is(rerr, io.ErrUnexpectedEOF):
				return fmt.Errorf("%w: new snapshot ends at byte %d of the old content", ErrMismatch, offset)
			case rerr != nil:
				return fmt.Errorf("error reading new snapshot: %w", rerr)
			}
			if !bytes.Equal(oldBuf[:n], newBuf[:n]) {
				return fmt.Errorf("%w: content diverges within bytes %d..%d of the old content", ErrMismatch, offset, offset+int64(n))
			}
			offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("error reading old snapshot: %w", err)
		}
	}
}

// checkTail walks the entries the new snapshot appends after the old
// content, requiring each to advance the (blockNumber, logIndex) order.
func checkTail(r *bufio.Reader, prev *resume.Cursor) (int, error) {
	last := *prev

	var (
		appended int
		lineNum  int
		line     []byte
	)
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > resume.MaxLineBytes {
			return 0, fmt.Errorf("appended line %d exceeds %d bytes", lineNum+1, resume.MaxLineBytes)
		}

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			// Defensive: Verify rejects a truncated new file up front, and a
			// clean gzip member ending mid-line is already refused by
			// resume.Read, so this should be unreachable in practice.
			if len(line) > 0 {
				return 0, fmt.Errorf("appended line %d ends without a newline", lineNum+1)
			}
			return appended, nil
		case err != nil:
			return 0, fmt.Errorf("error reading new snapshot: %w", err)
		}

		lineNum++
		entry, err := resume.ParseEntry(line)
		if err != nil {
			return 0, fmt.Errorf("appended line %d is not a log entry: %w", lineNum, err)
		}
		if !after(entry, &last) {
			return 0, fmt.Errorf("%w: appended line %d holds (block %d, log %d) after (block %d, log %d)",
				ErrOrder, lineNum, entry.BlockNumber, entry.LogIndex, last.BlockNumber, last.LogIndex)
		}
		last = *entry
		appended++
		line = line[:0]
	}
}

// after reports whether entry strictly follows prev in log order.
func after(entry, prev *resume.Cursor) bool {
	if entry.BlockNumber != prev.BlockNumber {
		return entry.BlockNumber > prev.BlockNumber
	}
	return entry.LogIndex > prev.LogIndex
}
