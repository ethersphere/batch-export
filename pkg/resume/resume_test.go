package resume_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/filestore"
	"github.com/ethersphere/batch-export/pkg/gzipstore"
	"github.com/ethersphere/batch-export/pkg/resume"
)

// Export file names. The format is detected from the file's leading bytes, so
// the name a case uses never decides how it is read.
const (
	plainFile = "export.ndjson"
	gzipFile  = "export.ndjson.gzip"
)

// Format names reused across several tables of test cases.
const (
	nameFormatPlain = "plain ndjson"
	nameFormatGzip  = "gzip"
)

// testLog builds a log shaped like the ones the exporter writes. types.Log
// always marshals blockNumber and logIndex, even at zero.
func testLog(blockNumber uint64, logIndex uint) types.Log {
	return types.Log{
		Address:     common.HexToAddress("0x45a1502382541cd610cc9068e88727426b696293"),
		Topics:      []common.Hash{common.HexToHash("0xae46785019700e30375a5d7b4f91e32f8060ef085111f896ebf889450aa2ab5a")},
		Data:        bytes.Repeat([]byte{0xab}, 32),
		BlockNumber: blockNumber,
		TxHash:      common.HexToHash("0xb08f07656eaafa8efc458e2aa90773648d95ec8119873d212b4377dea5190cc0"),
		TxIndex:     9,
		BlockHash:   common.HexToHash("0x86dc5f9da5fcba5191f6b3d2ba995bd75532ef369a7baa3970b3fb292ae91324"),
		Index:       logIndex,
		Removed:     false,
	}
}

// ndjson renders logs as newline-delimited JSON, the exporter's output format.
func ndjson(t *testing.T, logs ...types.Log) []byte {
	t.Helper()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range logs {
		if err := enc.Encode(l); err != nil {
			t.Fatalf("encode log: %v", err)
		}
	}
	return buf.Bytes()
}

// gz compresses b into a single gzip member.
func gz(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// truncateLast drops the final n bytes of b, standing in for a write that a
// hard kill cut short.
func truncateLast(b []byte, n int) []byte {
	return b[:len(b)-n]
}

// write puts content in a temp file and returns its path.
func write(t *testing.T, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestReadCursor(t *testing.T) {
	t.Parallel()

	threeLogs := ndjson(t, testLog(100, 0), testLog(101, 1), testLog(102, 7))

	// many is large enough to span several of the gzip path's 64 KiB member
	// buffers, exercising the buffered scan across bufio.ReadSlice refills
	// rather than a single in-memory read.
	manyLogs := make([]types.Log, 0, 2000)
	for i := range 2000 {
		manyLogs = append(manyLogs, testLog(uint64(1000+i), uint(i%16)))
	}
	many := ndjson(t, manyLogs...)

	// twoLogsEnd is the offset just past the newline closing the second of
	// threeLogs: the last clean boundary once the third line loses its own.
	twoLogsEnd := int64(len(ndjson(t, testLog(100, 0), testLog(101, 1))))
	threeLogsEnd := int64(len(threeLogs))

	tests := []struct {
		name           string
		content        []byte
		wantBlock      uint64
		wantIndex      uint
		wantCompressed bool
		// wantTruncated says whether bytes follow the last clean boundary.
		// When it is false the boundary must be the end of the file, so
		// wantCleanSize is only consulted for the truncated cases.
		wantTruncated bool
		wantCleanSize int64
	}{
		{
			name:      nameFormatPlain,
			content:   threeLogs,
			wantBlock: 102,
			wantIndex: 7,
		},
		{
			name:      "single line",
			content:   ndjson(t, testLog(55, 3)),
			wantBlock: 55,
			wantIndex: 3,
		},
		{
			// A line cut short by an interrupted write. The cursor is the
			// line before it and the partial bytes are reported, so a caller
			// discards them instead of appending onto them.
			name:          "truncated trailing line is excluded from the clean boundary",
			content:       append(append([]byte{}, threeLogs...), []byte(`{"address":"0x45a15","topics":["0xae`)...),
			wantBlock:     102,
			wantIndex:     7,
			wantTruncated: true,
			wantCleanSize: threeLogsEnd,
		},
		{
			// json.Encoder writes a value and its newline in one call, so a
			// last line that parses but has no newline is still a partial
			// write: the cursor must fall back to the line before it, or the
			// unterminated log would be skipped on resume and lost.
			name:          "trailing line without a newline is not a clean end",
			content:       bytes.TrimSuffix(threeLogs, []byte("\n")),
			wantBlock:     101,
			wantIndex:     1,
			wantTruncated: true,
			wantCleanSize: twoLogsEnd,
		},
		{
			name:      "many logs",
			content:   many,
			wantBlock: 2999,
			wantIndex: 15,
		},
		{
			name:           nameFormatGzip,
			content:        gz(t, threeLogs),
			wantBlock:      102,
			wantIndex:      7,
			wantCompressed: true,
		},
		{
			name:           "gzip spanning many logs",
			content:        gz(t, many),
			wantBlock:      2999,
			wantIndex:      15,
			wantCompressed: true,
		},
		{
			name:           "multi member gzip reads through to the last member",
			content:        append(gz(t, threeLogs), gz(t, ndjson(t, testLog(200, 2)))...),
			wantBlock:      200,
			wantIndex:      2,
			wantCompressed: true,
		},
		{
			// A resume interrupted before the second member was flushed: the
			// header is present but carries no decodable data. The clean
			// boundary is the end of the first member, so the half-written
			// header is reported rather than appended onto.
			name:           "multi member gzip with unflushed final member",
			content:        append(gz(t, threeLogs), []byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 0xff}...),
			wantBlock:      102,
			wantIndex:      7,
			wantCompressed: true,
			wantTruncated:  true,
			wantCleanSize:  int64(len(gz(t, threeLogs))),
		},
		{
			// Only the last member's trailer is lost. Everything before it is
			// still a member boundary, so the file stays recoverable.
			name:           "multi member gzip with a truncated final member",
			content:        truncateLast(append(gz(t, threeLogs), gz(t, ndjson(t, testLog(200, 2)))...), 6),
			wantBlock:      102,
			wantIndex:      7,
			wantCompressed: true,
			wantTruncated:  true,
			wantCleanSize:  int64(len(gz(t, threeLogs))),
		},
		{
			name:           "empty trailing gzip member is valid",
			content:        append(gz(t, threeLogs), gz(t, nil)...),
			wantBlock:      102,
			wantIndex:      7,
			wantCompressed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resume.Read(write(t, plainFile, tt.content))
			if err != nil {
				t.Fatalf("Read() error = %v, want nil", err)
			}
			if got.BlockNumber != tt.wantBlock {
				t.Errorf("BlockNumber = %d, want %d", got.BlockNumber, tt.wantBlock)
			}
			if got.LogIndex != tt.wantIndex {
				t.Errorf("LogIndex = %d, want %d", got.LogIndex, tt.wantIndex)
			}
			if got.Compressed != tt.wantCompressed {
				t.Errorf("Compressed = %t, want %t", got.Compressed, tt.wantCompressed)
			}
			if got.Truncated != tt.wantTruncated {
				t.Errorf("Truncated = %t, want %t", got.Truncated, tt.wantTruncated)
			}

			// A file reported as untruncated must be clean all the way to its
			// end, which is the invariant an appending caller relies on.
			wantCleanSize := int64(len(tt.content))
			if tt.wantTruncated {
				wantCleanSize = tt.wantCleanSize
			}
			if got.CleanSize != wantCleanSize {
				t.Errorf("CleanSize = %d, want %d", got.CleanSize, wantCleanSize)
			}
		})
	}
}

// TestReadRefusals covers §5's strict contract: the only irregularity
// tolerated is the tool's own interrupted final write. Content the tool never
// writes is ErrNotAnExport; a file consistent with tool output but holding no
// complete entry is ErrNoLogs.
func TestReadRefusals(t *testing.T) {
	t.Parallel()

	logs := ndjson(t, testLog(100, 0), testLog(101, 0))

	tests := []struct {
		name    string
		content []byte
		wantErr error
	}{
		{
			name:    "empty file",
			content: []byte{},
			wantErr: resume.ErrNoLogs,
		},
		{
			name:    "plain file holding one unterminated line",
			content: bytes.TrimSuffix(ndjson(t, testLog(100, 0)), []byte("\n")),
			wantErr: resume.ErrNoLogs,
		},
		{
			// The tool never writes a blank line.
			name:    "only a newline",
			content: []byte("\n"),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "trailing blank line after valid logs",
			content: append(append([]byte{}, logs...), '\n'),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// A complete line that is not a log entry means the file was
			// altered after export; refusing beats guessing what to cut.
			name:    "trailing non-log line after valid logs",
			content: append(append([]byte{}, logs...), []byte("not-json\n")...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "trailing log line missing blockNumber",
			content: append(append([]byte{}, logs...), []byte("{\"address\":\"0x1\",\"topics\":[],\"data\":\"0x\"}\n")...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "only garbage lines",
			content: bytes.Repeat([]byte("not-json\n"), 10),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// Finding #4's shape: refused from a single tail read, no
			// backward scan through the junk.
			name:    "newline-free tail longer than a line can be",
			content: append(append([]byte{}, logs...), bytes.Repeat([]byte("x"), 1<<20+1)...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "single unterminated line larger than the cap",
			content: bytes.Repeat([]byte("x"), 2<<20),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// A cleanly terminated member whose content stops mid-line
			// cannot come from this tool: members hold whole lines, and an
			// interrupted write cannot produce a valid trailer.
			name:    "gzip member ending mid line",
			content: gz(t, bytes.TrimSuffix(logs, []byte("\n"))),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// Finding #6's shape: foreign data concatenated as its own valid
			// member. Refused rather than treated as a removable tail.
			name:    "gzip junk member after valid member",
			content: append(gz(t, logs), gz(t, []byte("not-json\nalso-not\n"))...),
			wantErr: resume.ErrNotAnExport,
		},
		{
			name:    "gzip member holding a non-log line between logs",
			content: gz(t, append(append([]byte{}, logs...), []byte("not-json\n")...)),
			wantErr: resume.ErrNotAnExport,
		},
		{
			// A sole member without its trailer is an interrupted first
			// write: nothing to resume from, so a fresh export is the remedy.
			name:    "gzip with a single truncated member",
			content: truncateLast(gz(t, logs), 6),
			wantErr: resume.ErrNoLogs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := resume.Read(write(t, plainFile, tt.content)); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Read() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestReadMissingFile(t *testing.T) {
	t.Parallel()

	_, err := resume.Read(filepath.Join(t.TempDir(), "does-not-exist.ndjson"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read() error = %v, want os.ErrNotExist", err)
	}
}

func TestCursorSkip(t *testing.T) {
	t.Parallel()

	cursor := &resume.Cursor{BlockNumber: 100, LogIndex: 5}

	tests := []struct {
		name string
		log  types.Log
		want bool
	}{
		{name: "earlier block", log: testLog(99, 0), want: true},
		{name: "same block earlier index", log: testLog(100, 4), want: true},
		{name: "same block same index", log: testLog(100, 5), want: true},
		{name: "same block later index", log: testLog(100, 6), want: false},
		{name: "later block index zero", log: testLog(101, 0), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := cursor.Skip(tt.log); got != tt.want {
				t.Errorf("Skip() = %t, want %t", got, tt.want)
			}
		})
	}
}

// decompressAll reads every byte of a (possibly multi-member) gzip stream.
// Multistream is on by default, so concatenated members read as one stream.
func decompressAll(t *testing.T, b []byte) []byte {
	t.Helper()

	reader, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}

	return got
}

// TestAppendResumeRoundTrip exercises resume, filestore and gzipstore together
// with no RPC: read the cursor, replay the boundary block plus new blocks
// through AppendLogsAsync filtered by Skip, check the result byte-for-byte
// against the original plus exactly the new logs, then resume again to confirm
// the appended file is itself resumable.
func TestAppendResumeRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		compressed bool
	}{
		{name: nameFormatPlain},
		{name: nameFormatGzip, compressed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// original spans three blocks, the last of which carries two
			// logs, so resuming must handle a genuinely partial block.
			original := []types.Log{
				testLog(100, 0),
				testLog(101, 0),
				testLog(102, 0),
				testLog(102, 1),
			}
			// boundaryHigher shares the cursor's block but was never saved,
			// so it must be written rather than skipped.
			boundaryHigher := testLog(102, 2)
			newer := []types.Log{
				testLog(103, 0),
				testLog(104, 0),
			}

			plain := ndjson(t, original...)

			var (
				originalBytes []byte
				name          string
			)
			if tt.compressed {
				originalBytes = gz(t, plain)
				name = "export.ndjson.gz"
			} else {
				originalBytes = plain
				name = plainFile
			}
			path := write(t, name, originalBytes)

			cursor, err := resume.Read(path)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if cursor.BlockNumber != 102 || cursor.LogIndex != 1 {
				t.Fatalf("cursor = {%d,%d}, want {102,1}", cursor.BlockNumber, cursor.LogIndex)
			}
			if cursor.Compressed != tt.compressed {
				t.Fatalf("Compressed = %t, want %t", cursor.Compressed, tt.compressed)
			}

			var w io.WriteCloser
			if cursor.Compressed {
				w, err = gzipstore.AppendWriter(path)
			} else {
				w, err = filestore.AppendWriter(path)
			}
			if err != nil {
				t.Fatalf("AppendWriter() error = %v", err)
			}

			// replay mimics a resumed export re-querying the boundary block:
			// its already-written logs must be skipped, its never-saved
			// higher-index log must not be, and the later blocks are new.
			replay := make([]types.Log, 0, 3+len(newer))
			replay = append(replay, testLog(102, 0), testLog(102, 1), boundaryHigher)
			replay = append(replay, newer...)

			ch := make(chan types.Log, len(replay))
			for _, l := range replay {
				ch <- l
			}
			close(ch)

			if err := filestore.AppendLogsAsync(t.Context(), ch, w, cursor.Skip); err != nil {
				t.Fatalf("AppendLogsAsync() error = %v", err)
			}

			gotRaw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			// The original bytes must survive as an unchanged prefix:
			// appending must never rewrite existing content. For gzip this
			// also proves a genuine second member was added rather than the
			// archive being decompressed and rewritten.
			if len(gotRaw) < len(originalBytes) || !bytes.Equal(gotRaw[:len(originalBytes)], originalBytes) {
				t.Fatalf("original bytes are not an unchanged prefix of the appended file")
			}

			gotPlain := gotRaw
			if tt.compressed {
				gotPlain = decompressAll(t, gotRaw)
			}

			all := make([]types.Log, 0, len(original)+1+len(newer))
			all = append(all, original...)
			all = append(all, boundaryHigher)
			all = append(all, newer...)
			want := ndjson(t, all...)

			if !bytes.Equal(gotPlain, want) {
				t.Fatalf("content = %s, want %s", gotPlain, want)
			}

			cursor2, err := resume.Read(path)
			if err != nil {
				t.Fatalf("second Read() error = %v", err)
			}
			if cursor2.BlockNumber != 104 || cursor2.LogIndex != 0 {
				t.Errorf("resumed cursor = {%d,%d}, want {104,0}", cursor2.BlockNumber, cursor2.LogIndex)
			}
			if cursor2.Compressed != tt.compressed {
				t.Errorf("resumed Compressed = %t, want %t", cursor2.Compressed, tt.compressed)
			}
		})
	}
}

// logsIn returns every log an export file holds, decompressing gzip first.
// Anything that is not a whole, newline-terminated NDJSON line fails the test,
// so a file that merely looks recovered is caught here rather than downstream.
func logsIn(t *testing.T, path string) []types.Log {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
		raw = decompressAll(t, raw)
	}
	if len(raw) == 0 {
		return nil
	}
	if raw[len(raw)-1] != '\n' {
		t.Fatalf("file does not end on a line boundary, last 80 bytes: %q", raw[max(0, len(raw)-80):])
	}

	var logs []types.Log
	for i, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
		var l types.Log
		if err := json.Unmarshal(line, &l); err != nil {
			t.Fatalf("line %d does not parse: %v: %q", i+1, err, line)
		}
		logs = append(logs, l)
	}

	return logs
}

// ids renders logs as block/index pairs, the identity in which "no log
// duplicated and none lost" is measured.
func ids(logs []types.Log) []string {
	out := make([]string, 0, len(logs))
	for _, l := range logs {
		out = append(out, fmt.Sprintf("%d/%d", l.BlockNumber, l.Index))
	}

	return out
}

// appendWriter opens the append writer that matches the cursor's format.
func appendWriter(t *testing.T, path string, cursor *resume.Cursor) io.WriteCloser {
	t.Helper()

	var (
		w   io.WriteCloser
		err error
	)
	if cursor.Compressed {
		w, err = gzipstore.AppendWriter(path)
	} else {
		w, err = filestore.AppendWriter(path)
	}
	if err != nil {
		t.Fatalf("AppendWriter() error = %v", err)
	}

	return w
}

// feed returns a closed channel already holding logs.
func feed(logs ...types.Log) <-chan types.Log {
	ch := make(chan types.Log, len(logs))
	for _, l := range logs {
		ch <- l
	}
	close(ch)

	return ch
}

// TestCopyResumeRoundTrip is the spec's recommended workflow (§4): resume an
// archived snapshot into a NEW file. The input must come out byte-identical;
// the output must hold the input's content plus exactly the new entries and
// be itself resumable.
func TestCopyResumeRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		compressed bool
	}{
		{name: nameFormatPlain},
		{name: nameFormatGzip, compressed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			original := []types.Log{testLog(100, 0), testLog(101, 0), testLog(102, 0), testLog(102, 1)}
			boundaryHigher := testLog(102, 2)
			newer := []types.Log{testLog(103, 0), testLog(104, 0)}

			inputBytes := ndjson(t, original...)
			if tt.compressed {
				inputBytes = gz(t, inputBytes)
			}
			dir := t.TempDir()
			input := filepath.Join(dir, "prev.snapshot")
			output := filepath.Join(dir, "next.snapshot")
			if err := os.WriteFile(input, inputBytes, 0o644); err != nil {
				t.Fatalf("write input: %v", err)
			}

			cursor, err := resume.Read(input)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}

			discarded, err := resume.PrepareOutput(cursor, input, output)
			if err != nil {
				t.Fatalf("PrepareOutput() error = %v", err)
			}
			if discarded != 0 {
				t.Errorf("discarded = %d, want 0 for a clean input", discarded)
			}

			w := appendWriter(t, output, cursor)
			replay := []types.Log{testLog(102, 0), testLog(102, 1), boundaryHigher}
			replay = append(replay, newer...)
			if err := filestore.AppendLogsAsync(t.Context(), feed(replay...), w, cursor.Skip); err != nil {
				t.Fatalf("AppendLogsAsync() error = %v", err)
			}

			// The input is untouched, byte for byte.
			gotInput, err := os.ReadFile(input)
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			if !bytes.Equal(gotInput, inputBytes) {
				t.Fatal("input file was modified by a copy-mode resume")
			}

			// The output begins with the input's exact bytes (raw prefix
			// copy, no recompression) and holds the full sequence.
			gotOutput, err := os.ReadFile(output)
			if err != nil {
				t.Fatalf("read output: %v", err)
			}
			if len(gotOutput) < len(inputBytes) || !bytes.Equal(gotOutput[:len(inputBytes)], inputBytes) {
				t.Fatal("input bytes are not an unchanged prefix of the output")
			}
			all := append(append(append([]types.Log{}, original...), boundaryHigher), newer...)
			if got, want := ids(logsIn(t, output)), ids(all); !slices.Equal(got, want) {
				t.Fatalf("output logs = %v, want %v", got, want)
			}

			cursor2, err := resume.Read(output)
			if err != nil {
				t.Fatalf("Read(output) error = %v", err)
			}
			if cursor2.BlockNumber != 104 || cursor2.LogIndex != 0 || cursor2.Truncated {
				t.Errorf("output cursor = {%d,%d,truncated=%t}, want {104,0,false}", cursor2.BlockNumber, cursor2.LogIndex, cursor2.Truncated)
			}
		})
	}
}

// TestCopyResumeFromInterruptedInput: copy mode never repairs the input — the
// interrupted tail stays in it — while the output gets only clean content
// plus the re-fetched entries.
func TestCopyResumeFromInterruptedInput(t *testing.T) {
	t.Parallel()

	saved := ndjson(t, testLog(100, 0), testLog(101, 0))

	tests := []struct {
		name       string
		content    []byte
		compressed bool
	}{
		{
			name:    "plain with a truncated last line",
			content: append(append([]byte{}, saved...), []byte(`{"address":"0x45a15`)...),
		},
		{
			name:       "gzip with a truncated final member",
			content:    append(gz(t, saved), truncateLast(gz(t, ndjson(t, testLog(102, 0))), 6)...),
			compressed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			input := filepath.Join(dir, "prev.snapshot")
			output := filepath.Join(dir, "next.snapshot")
			if err := os.WriteFile(input, tt.content, 0o644); err != nil {
				t.Fatalf("write input: %v", err)
			}

			cursor, err := resume.Read(input)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if !cursor.Truncated {
				t.Fatal("test setup: input should be truncated")
			}

			discarded, err := resume.PrepareOutput(cursor, input, output)
			if err != nil {
				t.Fatalf("PrepareOutput() error = %v", err)
			}
			if want := int64(len(tt.content)) - cursor.CleanSize; discarded != want {
				t.Errorf("discarded = %d, want %d", discarded, want)
			}

			w := appendWriter(t, output, cursor)
			replay := []types.Log{testLog(101, 0), testLog(102, 0), testLog(103, 0)}
			if err := filestore.AppendLogsAsync(t.Context(), feed(replay...), w, cursor.Skip); err != nil {
				t.Fatalf("AppendLogsAsync() error = %v", err)
			}

			gotInput, err := os.ReadFile(input)
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			if !bytes.Equal(gotInput, tt.content) {
				t.Fatal("input was modified, interrupted tail included it must stay")
			}

			want := []types.Log{testLog(100, 0), testLog(101, 0), testLog(102, 0), testLog(103, 0)}
			if got := ids(logsIn(t, output)); !slices.Equal(got, ids(want)) {
				t.Fatalf("output logs = %v, want %v", got, ids(want))
			}
		})
	}
}

// TestPrepareOutputSameFileUnderDifferentSpellings pins Fix 1: two spellings
// of the SAME underlying file — an absolute path against a relative one, or a
// symlink pointed at the input — must be treated as in-place, not copy mode.
// Before the fix, PrepareOutput compared filepath.Clean strings, missed both
// cases, and its os.Create(outputPath) truncated the input to zero bytes
// before io.CopyN ever got to read from it, destroying the file it was
// supposed to leave untouched.
//
// Each case covers both a clean input (must survive byte for byte) and one
// with an interrupted final write (must be truncated to exactly CleanSize,
// the same result prepareInPlace produces for a same-path call).
func TestPrepareOutputSameFileUnderDifferentSpellings(t *testing.T) {
	logs := ndjson(t, testLog(100, 0), testLog(101, 0))
	interrupted := append(append([]byte{}, logs...), []byte(`{"address":"0x45a1502382541`)...)

	contents := []struct {
		name    string
		content []byte
	}{
		{name: "clean input", content: logs},
		{name: "interrupted write", content: interrupted},
	}

	// spelling produces an outputPath that names the same file as input by
	// some route other than an identical string.
	spellings := []struct {
		name    string
		outPath func(t *testing.T, dir, input string) string
	}{
		{
			// t.Chdir affects the whole process, so this case (and therefore
			// the whole test) cannot run in parallel with anything else.
			name: "absolute input, relative output",
			outPath: func(t *testing.T, dir, input string) string {
				t.Chdir(dir)
				return filepath.Base(input)
			},
		},
		{
			name: "symlink to the input",
			outPath: func(t *testing.T, dir, input string) string {
				link := filepath.Join(dir, "alias-of-input")
				if err := os.Symlink(input, link); err != nil {
					t.Skipf("symlinks unsupported on this filesystem: %v", err)
				}
				return link
			},
		},
	}

	for _, ct := range contents {
		for _, sp := range spellings {
			t.Run(ct.name+"/"+sp.name, func(t *testing.T) {
				dir := t.TempDir()
				input := filepath.Join(dir, "snapshot.ndjson")
				if err := os.WriteFile(input, ct.content, 0o644); err != nil {
					t.Fatalf("write input: %v", err)
				}

				cursor, err := resume.Read(input)
				if err != nil {
					t.Fatalf("Read() error = %v", err)
				}

				output := sp.outPath(t, dir, input)

				discarded, err := resume.PrepareOutput(cursor, input, output)
				if err != nil {
					t.Fatalf("PrepareOutput() error = %v", err)
				}

				wantDiscarded := int64(len(ct.content)) - cursor.CleanSize
				if discarded != wantDiscarded {
					t.Errorf("discarded = %d, want %d", discarded, wantDiscarded)
				}

				got, err := os.ReadFile(input)
				if err != nil {
					t.Fatalf("read input: %v", err)
				}
				want := ct.content[:cursor.CleanSize]
				if !bytes.Equal(got, want) {
					t.Fatalf("input holds %d bytes, want the %d-byte clean prefix preserved (copy mode must not have truncated the input)", len(got), len(want))
				}
			})
		}
	}
}

// TestReadRejectsCorruptedChecksum pins Fix 3: a member whose body is intact
// but whose trailing CRC has been altered is corruption or tampering, not the
// tool's own interrupted write, and must be refused with ErrNotAnExport
// rather than silently treated as a truncated final member.
func TestReadRejectsCorruptedChecksum(t *testing.T) {
	t.Parallel()

	content := gz(t, ndjson(t, testLog(100, 0), testLog(101, 0)))
	// The trailer is the last 8 bytes: a 4-byte CRC32 followed by a 4-byte
	// ISIZE. Flipping a byte within the first 4 corrupts the CRC alone,
	// leaving the member body and its length untouched.
	content[len(content)-8] ^= 0xff

	_, err := resume.Read(write(t, gzipFile, content))
	if !errors.Is(err, resume.ErrNotAnExport) {
		t.Fatalf("Read() error = %v, want ErrNotAnExport", err)
	}
}

// TestReadGzipCutInsideHeader pins Fix 4: a file cut short before the gzip
// header is even complete is an interrupted first write, exactly like the
// plain format's equivalent, and must report ErrNoLogs rather than a bare
// wrapped error that escapes the two-sentinel taxonomy.
func TestReadGzipCutInsideHeader(t *testing.T) {
	t.Parallel()

	full := gz(t, ndjson(t, testLog(100, 0)))

	_, err := resume.Read(write(t, gzipFile, full[:5]))
	if !errors.Is(err, resume.ErrNoLogs) {
		t.Fatalf("Read() error = %v, want ErrNoLogs", err)
	}
}

// TestResumeAfterInterruptedWrite covers the three shapes a run killed
// mid-write leaves behind: a plain line cut in half, a plain line that parses
// but never got its newline, and a gzip member that never got its trailer.
// Appending onto any of them blindly corrupts the file.
//
// Each case walks the whole recovery — read the cursor, discard past the clean
// boundary, append what a resumed query returns — and requires the file to
// parse end to end holding exactly the four logs, in order, none duplicated
// and none lost.
func TestResumeAfterInterruptedWrite(t *testing.T) {
	t.Parallel()

	// saved is what the interrupted run had managed to write whole.
	saved := ndjson(t, testLog(100, 0), testLog(101, 0))
	savedFirst := ndjson(t, testLog(100, 0))

	// want is the full export: what the file must hold once resumed.
	want := []types.Log{testLog(100, 0), testLog(101, 0), testLog(102, 0), testLog(103, 0)}

	tests := []struct {
		name string
		// content is the file the interrupted run left behind.
		content    []byte
		fileName   string
		compressed bool
		// wantBlock and wantIndex are the last entry that is complete and
		// properly terminated.
		wantBlock     uint64
		wantIndex     uint
		wantCleanSize int64
		// replay is what the resumed query returns from wantBlock onwards.
		replay []types.Log
	}{
		{
			// Appending onto the half line glues the next log onto it,
			// destroying block 102.
			name:          "plain file with a truncated last line",
			content:       append(append([]byte{}, saved...), []byte(`{"address":"0x45a1502382541cd610cc9068e88727426b6`)...),
			fileName:      plainFile,
			wantBlock:     101,
			wantIndex:     0,
			wantCleanSize: int64(len(saved)),
			replay:        []types.Log{testLog(101, 0), testLog(102, 0), testLog(103, 0)},
		},
		{
			// The nastiest: the line parses, so a cursor naming it would make
			// the resumed query skip block 101 while the append fuses the next
			// log onto it — unparseable, and never re-fetched. The cursor must
			// name block 100 so that 101 is fetched again.
			name:          "plain file with a valid but unterminated last line",
			content:       bytes.TrimSuffix(saved, []byte("\n")),
			fileName:      plainFile,
			wantBlock:     100,
			wantIndex:     0,
			wantCleanSize: int64(len(savedFirst)),
			replay:        []types.Log{testLog(100, 0), testLog(101, 0), testLog(102, 0), testLog(103, 0)},
		},
		{
			// The final member lost its trailer, so a member appended after
			// it sits behind corrupt data and can never be read back, while
			// every later resume appends more of the same and reports
			// success.
			name:          "gzip file with a truncated final member",
			content:       append(gz(t, savedFirst), truncateLast(gz(t, ndjson(t, testLog(101, 0))), 6)...),
			fileName:      gzipFile,
			compressed:    true,
			wantBlock:     100,
			wantIndex:     0,
			wantCleanSize: int64(len(gz(t, savedFirst))),
			replay:        []types.Log{testLog(100, 0), testLog(101, 0), testLog(102, 0), testLog(103, 0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := write(t, tt.fileName, tt.content)

			cursor, err := resume.Read(path)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if cursor.BlockNumber != tt.wantBlock || cursor.LogIndex != tt.wantIndex {
				t.Fatalf("cursor = {%d,%d}, want {%d,%d}", cursor.BlockNumber, cursor.LogIndex, tt.wantBlock, tt.wantIndex)
			}
			if cursor.Compressed != tt.compressed {
				t.Fatalf("Compressed = %t, want %t", cursor.Compressed, tt.compressed)
			}
			if !cursor.Truncated {
				t.Fatalf("Truncated = false, want true: the partial write went unreported")
			}
			if cursor.CleanSize != tt.wantCleanSize {
				t.Fatalf("CleanSize = %d, want %d", cursor.CleanSize, tt.wantCleanSize)
			}

			// Recovery: drop the partial tail, exactly as the export command
			// does, and never further than the reported boundary.
			discarded, err := resume.PrepareOutput(cursor, path, path)
			if err != nil {
				t.Fatalf("PrepareOutput() error = %v", err)
			}
			if want := int64(len(tt.content)) - cursor.CleanSize; discarded != want {
				t.Errorf("discarded = %d, want %d", discarded, want)
			}

			if err := filestore.AppendLogsAsync(t.Context(), feed(tt.replay...), appendWriter(t, path, cursor), cursor.Skip); err != nil {
				t.Fatalf("AppendLogsAsync() error = %v", err)
			}

			if got := ids(logsIn(t, path)); !slices.Equal(got, ids(want)) {
				t.Fatalf("logs = %v, want %v", got, ids(want))
			}

			// The recovered file must itself resume cleanly, or a second
			// interruption would start the corruption over again.
			cursor2, err := resume.Read(path)
			if err != nil {
				t.Fatalf("second Read() error = %v", err)
			}
			if cursor2.BlockNumber != 103 || cursor2.LogIndex != 0 {
				t.Errorf("resumed cursor = {%d,%d}, want {103,0}", cursor2.BlockNumber, cursor2.LogIndex)
			}
			if cursor2.Truncated {
				t.Errorf("Truncated = true, want false: the recovered file is not clean")
			}
		})
	}
}

// TestReadReportsGzipReadErrors pins Fix 2: a gzip stream that stops early
// must not be reported as a clean read just because some lines decoded. The
// old code returned the last cursor it had seen with a nil error, which let
// every later resume append more data behind the corruption, for ever.
func TestReadReportsGzipReadErrors(t *testing.T) {
	t.Parallel()

	clean := gz(t, ndjson(t, testLog(100, 0)))
	content := append(append([]byte{}, clean...), truncateLast(gz(t, ndjson(t, testLog(101, 0), testLog(102, 0))), 6)...)

	cursor, err := resume.Read(write(t, gzipFile, content))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !cursor.Truncated {
		t.Error("Truncated = false, want true: the unclean stream was reported as clean")
	}
	if cursor.CleanSize != int64(len(clean)) {
		t.Errorf("CleanSize = %d, want %d", cursor.CleanSize, len(clean))
	}
	// The cursor must not name a log that only the broken member holds: it is
	// about to be discarded, and a cursor past it would skip the refetch.
	if cursor.BlockNumber != 100 || cursor.LogIndex != 0 {
		t.Errorf("cursor = {%d,%d}, want {100,0}", cursor.BlockNumber, cursor.LogIndex)
	}
}

// TestGzipCleanSizeIsAMemberBoundary checks the offset the gzip walk reports
// is exactly a member boundary and not a byte off, which is what makes
// truncating to it safe. Reading the file back after cutting it there must
// yield the members that came before, whole.
func TestGzipCleanSizeIsAMemberBoundary(t *testing.T) {
	t.Parallel()

	first := ndjson(t, testLog(100, 0), testLog(101, 0))
	second := ndjson(t, testLog(102, 0))

	members := append(gz(t, first), gz(t, second)...)
	boundary := int64(len(members))
	content := append(append([]byte{}, members...), truncateLast(gz(t, ndjson(t, testLog(103, 0))), 4)...)

	path := write(t, gzipFile, content)

	cursor, err := resume.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if cursor.CleanSize != boundary {
		t.Fatalf("CleanSize = %d, want %d", cursor.CleanSize, boundary)
	}

	if err := os.Truncate(path, cursor.CleanSize); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got, want := decompressAll(t, raw), append(append([]byte{}, first...), second...); !bytes.Equal(got, want) {
		t.Fatalf("content = %s, want %s", got, want)
	}
}
