// Package output writes scrape results to a destination sink. P0 ships JSONL;
// CSV/Parquet/SQLite land in P1/P2.
package output

import (
	"encoding/json"
	"time"
)

// Record is the canonical shape of one scraped page's output.
// The JSON tags are the stable on-disk format.
type Record struct {
	URL          string         `json:"url"`
	CanonicalURL string         `json:"canonical_url"`
	FetchedAt    time.Time      `json:"fetched_at"`
	Tier         string         `json:"tier"`
	StatusCode   int            `json:"status_code"`
	DurationMS   int64          `json:"duration_ms"`
	ContentHash  string         `json:"content_hash,omitempty"`
	Extracted    map[string]any `json:"extracted,omitempty"`
	Metadata     Metadata       `json:"metadata"`
	Error        string         `json:"error,omitempty"`
}

// Metadata holds bookkeeping fields that don't belong in Extracted.
type Metadata struct {
	ContentType string   `json:"content_type,omitempty"`
	BodyBytes   int      `json:"body_bytes,omitempty"`
	FinalURL    string   `json:"final_url,omitempty"`
	Redirects   []string `json:"redirects,omitempty"`
}

// Sink is the minimal writer interface for any output format.
type Sink interface {
	Write(Record) error
	Close() error
}

// MarshalLine returns the JSONL-encoded form of a record (no trailing newline).
func MarshalLine(r Record) ([]byte, error) {
	return json.Marshal(r)
}
