package output

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// JSONL is an append-only, line-delimited JSON sink. Safe for concurrent use
// by multiple writers — each Write is emitted atomically.
type JSONL struct {
	mu     sync.Mutex
	w      *bufio.Writer
	closer io.Closer
	enc    *json.Encoder
}

// NewJSONLFile opens (or creates+appends) a file at path. If path is "-" the
// sink writes to stdout.
func NewJSONLFile(path string) (*JSONL, error) {
	if path == "-" || path == "" {
		return NewJSONL(os.Stdout, nil), nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return NewJSONL(f, f), nil
}

// NewJSONL wraps an arbitrary io.Writer. The optional closer is called on Close.
func NewJSONL(w io.Writer, closer io.Closer) *JSONL {
	bw := bufio.NewWriterSize(w, 64<<10)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	return &JSONL{
		w:      bw,
		closer: closer,
		enc:    enc,
	}
}

// Write serializes one record as a single JSON line.
func (j *JSONL) Write(r Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	// json.Encoder.Encode already appends a newline.
	return j.enc.Encode(r)
}

// Close flushes the buffer and closes the underlying file if one was given.
func (j *JSONL) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.w.Flush(); err != nil {
		return err
	}
	if j.closer != nil {
		return j.closer.Close()
	}
	return nil
}

// Flush forces any buffered output to the underlying writer.
func (j *JSONL) Flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.w.Flush()
}
