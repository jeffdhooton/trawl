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
	URL             string         `json:"url"`
	CanonicalURL    string         `json:"canonical_url"`
	FetchedAt       time.Time      `json:"fetched_at"`
	Tier            string         `json:"tier"`
	StatusCode      int            `json:"status_code"`
	DurationMS      int64          `json:"duration_ms"`
	ContentHash     string         `json:"content_hash,omitempty"`
	Extracted       map[string]any `json:"extracted,omitempty"`
	Metadata        Metadata       `json:"metadata"`
	Error           string         `json:"error,omitempty"`
	// FailureCategory is the classified bucket for stats aggregation.
	// Always populated — "success" for non-error records, one of the
	// values in internal/failure for anything else.
	FailureCategory string `json:"failure_category,omitempty"`
}

// Metadata holds bookkeeping fields that don't belong in Extracted.
type Metadata struct {
	ContentType string           `json:"content_type,omitempty"`
	BodyBytes   int              `json:"body_bytes,omitempty"`
	FinalURL    string           `json:"final_url,omitempty"`
	Redirects   []string         `json:"redirects,omitempty"`
	Extraction  *ExtractionStats `json:"extraction,omitempty"`
	Discovery   *DiscoveryStats  `json:"discovery,omitempty"`
}

// DiscoveryStats records how a record's target URL was discovered when
// hybrid discovery (--fallback-column + --fallback-selector) is in play.
// Only populated for rows whose seed had a fallback URL.
//
// Path is either "primary" (the seed's URL column worked on the first try)
// or "fallback" (the seed's primary failed with a trigger category and the
// fallback URL was resolved via --fallback-selector). PrimaryURL is always
// the original seed URL; FallbackURL is present only on the fallback path.
type DiscoveryStats struct {
	Path        string `json:"path"`
	PrimaryURL  string `json:"primary_url"`
	FallbackURL string `json:"fallback_url,omitempty"`
}

// ExtractionStats makes "no selectors requested" distinguishable from
// "selectors requested but all missed." Only populated when extraction ran.
type ExtractionStats struct {
	// Fields is the number of selectors the user asked for.
	Fields int `json:"fields"`
	// Hits is how many of those selectors matched at least one node.
	// Fields > 0 && Hits == 0 means the selectors are almost certainly wrong.
	Hits int `json:"hits"`
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
