// Package output writes scrape results to a destination sink. P0 ships JSONL;
// CSV/Parquet/SQLite land in P1/P2.
package output

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/jeffdhooton/trawl/internal/extract"
)

// PDFInfo is the durable per-document metadata extracted from a PDF
// response by internal/pdf. Populated only when the source was a PDF;
// nil for every HTML record. Mirrors internal/pdf.Info — kept separate
// so the output package has no dependency on internal/pdf.
type PDFInfo struct {
	PageCount     int       `json:"page_count,omitempty"`
	Title         string    `json:"title,omitempty"`
	Author        string    `json:"author,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	HasTextLayer  bool      `json:"has_text_layer,omitempty"`
	UsedOCR       bool      `json:"used_ocr,omitempty"`
	ExtractorTier string    `json:"extractor_tier,omitempty"` // "pdftotext" | "pdftotext-layout" | "tesseract"
	Pages         []PDFPage `json:"pages,omitempty"`
}

// PDFPage is one page's extracted text with its 1-based page number.
// Consumers that need per-page access (citation linking, region
// selection) reach for metadata.pdf.pages rather than re-splitting the
// markdown body.
type PDFPage struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

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
	// Body holds the fetched content in the format requested via --format.
	// Empty when --format is not set, so existing JSONL consumers that
	// never asked for a body don't see record bloat.
	Body       string `json:"body,omitempty"`
	BodyFormat string `json:"body_format,omitempty"` // "html" | "markdown"
	Metadata        Metadata       `json:"metadata"`
	Error           string         `json:"error,omitempty"`
	// FailureCategory is the classified bucket for stats aggregation.
	// Always populated — "success" for non-error records, one of the
	// values in internal/failure for anything else.
	FailureCategory string `json:"failure_category,omitempty"`
}

// Metadata holds bookkeeping fields that don't belong in Extracted.
type Metadata struct {
	ContentType    string                `json:"content_type,omitempty"`
	BodyBytes      int                   `json:"body_bytes,omitempty"`
	FinalURL       string                `json:"final_url,omitempty"`
	Redirects      []string              `json:"redirects,omitempty"`
	Extraction     *ExtractionStats      `json:"extraction,omitempty"`
	Discovery      *DiscoveryStats       `json:"discovery,omitempty"`
	Page           *extract.PageMetadata `json:"page,omitempty"`
	// ScreenshotPath is the absolute path to a PNG written by the
	// chromium engine when --screenshot-dir was set AND chromium was
	// the tier that served the page. HTTP-served records leave it empty.
	ScreenshotPath string `json:"screenshot_path,omitempty"`
	// FromCache is true when the record was reconstructed from the
	// cross-job content cache rather than a live fetch. Set by the
	// router when a cache hit short-circuits the tier loop.
	FromCache bool `json:"from_cache,omitempty"`
	// Evasion records which opt-in anti-detection features were active
	// when the page was fetched. Nil when no evasion was used; the
	// pointer-omitempty pattern keeps default-mode records the same
	// size they were before evasion shipped. The presence of this
	// field is the audit trail for "was this crawl polite or not."
	Evasion *EvasionStats `json:"evasion,omitempty"`
	// SoftBlock is non-nil when any tier returned a 200 OK whose body
	// matched an anti-bot challenge marker (Cloudflare "Just a moment",
	// Akamai challenge, DataDome captcha, etc.). Populated even when a
	// later tier succeeded — it's a forensic signal that a host serves
	// walls on at least one tier, not a final outcome. The final outcome
	// is in FailureCategory (= "soft_block" only when ALL tiers walled).
	SoftBlock *SoftBlockInfo `json:"soft_block,omitempty"`
	// PDF is populated when the fetched content was application/pdf
	// and internal/pdf.Extract successfully produced markdown. Nil for
	// HTML records. The pointer keeps default JSONL records the same
	// size they were before the PDF engine shipped.
	PDF *PDFInfo `json:"pdf,omitempty"`
}

// SoftBlockInfo captures the result of the soft-block heuristic across
// every tier the router tried. Detected is true whenever at least one
// tier's body matched a challenge marker. Vendor and Marker report the
// first (highest-specificity) hit seen — markers are listed in
// internal/validity in most-specific → most-generic order, so a real
// Cloudflare challenge is never misclassified as "generic" just because
// the page happens to also contain an "Access denied" string.
//
// Tiers lists the tier names whose attempts hit a soft-block, preserving
// order. This lets a consumer answer "did HTTP wall but chromium
// succeed?" without re-parsing the router's full Attempts list.
type SoftBlockInfo struct {
	Detected bool     `json:"detected"`
	Vendor   string   `json:"vendor,omitempty"`
	Marker   string   `json:"marker,omitempty"`
	Tiers    []string `json:"tiers,omitempty"`
}

// EvasionStats records the active anti-detection features for one
// fetch. Populated by the engine (BrowserLike/Stealth/UserAgent) and
// the politeness gate (JitterMS) and combined at record-build time.
// Every field uses omitempty so partial-evasion records (e.g. jitter
// only, no browser-like headers) stay tight in JSONL.
type EvasionStats struct {
	BrowserLike bool   `json:"browser_like,omitempty"`
	Stealth     bool   `json:"stealth,omitempty"`
	UserAgent   string `json:"user_agent,omitempty"`
	JitterMS    int64  `json:"jitter_ms,omitempty"`
	// TLSMatch is the active --tls-match preset (e.g. "chrome") when
	// Tier 3 evasion forged the ClientHello for this fetch. Empty
	// when the stdlib transport handled it.
	TLSMatch string `json:"tls_match,omitempty"`
	Proxy    bool   `json:"proxy,omitempty"`
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

// NewFile is the high-level constructor that picks a sink based on
// the output path's extension: .csv → CSV with comma separator,
// .tsv → CSV with tab separator, everything else (and stdout) → JSONL.
// csvColumns is ignored for non-CSV paths; callers are expected to
// validate "csv columns set but output is JSONL" upstream so the
// error message can reference the user's actual flag.
func NewFile(path string, csvColumns []string) (Sink, error) {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".csv"), strings.HasSuffix(lower, ".tsv"):
		return NewCSVFile(path, csvColumns)
	default:
		return NewJSONLFile(path)
	}
}

// IsCSVPath reports whether a given path would be written as CSV/TSV
// by NewFile. Callers use this to validate that --csv-columns is only
// set when the output path is actually a CSV file.
func IsCSVPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".csv") || strings.HasSuffix(lower, ".tsv")
}
