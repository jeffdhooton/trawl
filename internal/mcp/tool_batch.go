package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jeffdhooton/trawl/internal/frontier"
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// batchArgs is the input schema for the trawl_batch tool.
type batchArgs struct {
	URLs []string `json:"urls" jsonschema:"list of URLs to scrape (capped at 50 per call — for larger lists use 'trawl batch' on the CLI for the persistent frontier + resume guarantees)"`

	// Content extraction (mirrors scrapeArgs).
	Format      string   `json:"format,omitempty" jsonschema:"body output format: 'markdown', 'html', 'json', or '' (omit body field)"`
	Readability bool     `json:"readability,omitempty" jsonschema:"strip nav/footer/ads boilerplate before extraction"`
	NoMetadata  bool     `json:"noMetadata,omitempty" jsonschema:"skip the automatic page-metadata extractor"`
	Selectors   []string `json:"selectors,omitempty" jsonschema:"CSS extraction rules in 'name=selector' form"`
	SchemaPath  string   `json:"schemaPath,omitempty" jsonschema:"path to a YAML/JSON schema file for nested structured extraction"`

	// Routing.
	Tiers       string  `json:"tiers,omitempty" jsonschema:"engine ladder, default 'http,chromium'"`
	ForceTier   string  `json:"forceTier,omitempty" jsonschema:"pin a single engine: 'http' or 'chromium'"`
	TimeoutMS   int     `json:"timeoutMs,omitempty" jsonschema:"per-request timeout in milliseconds, default 30000"`
	Concurrency int     `json:"concurrency,omitempty" jsonschema:"max concurrent in-flight requests, default 8"`
	RatePerSec  float64 `json:"ratePerSec,omitempty" jsonschema:"requests per second per domain, default 1"`

	// Politeness.
	IgnoreRobots bool `json:"ignoreRobots,omitempty" jsonschema:"bypass robots.txt for this batch. Off by default."`

	// Anti-detection.
	BrowserLike bool   `json:"browserLike,omitempty" jsonschema:"enable Tier 1 browser mimicry"`
	Stealth     bool   `json:"stealth,omitempty" jsonschema:"Tier 2 chromium stealth init script"`
	TLSMatch    string `json:"tlsMatch,omitempty" jsonschema:"Tier 3: forge ClientHello to match a real browser. 'chrome' supported."`

	// PDF.
	OCR     bool   `json:"ocr,omitempty" jsonschema:"enable Tier 3 OCR for scanned PDFs"`
	OCRLang string `json:"ocrLang,omitempty" jsonschema:"tesseract language pack, default 'eng'"`
}

func registerBatchTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trawl_batch",
		Title:       "Scrape a list of URLs",
		Description: batchToolDescription,
	}, batchHandler)
}

const batchToolDescription = `Fetch a list of URLs concurrently through the tiered router and ` +
	`return one structured record per URL. Records have the same shape as ` +
	`trawl_scrape — body, page metadata, extractions, routing telemetry, ` +
	`failure_category for failed rows.

Use this when:
  - You have a known list of URLs (≤50) and want all of them scraped now.
  - You're collecting a small dataset (e.g. 10 product pages) for the
    agent's reasoning step.
  - The URLs may share a domain — per-domain rate limiting and tier
    learning kick in automatically.

Hard cap: 50 URLs per call. For larger batches, run 'trawl batch' on
the CLI directly — it gives you a persistent frontier, resume on
SIGINT, and full per-job stats. The MCP tool is for inline-in-the-loop
use, not multi-hour jobs.

Do NOT use for:
  - Discovering URLs first — use trawl_map or trawl_sitemap, then
    feed the result back through trawl_batch.
  - Walking a single site by following links — use trawl_crawl.`

func batchHandler(ctx context.Context, _ *mcp.CallToolRequest, args batchArgs) (*mcp.CallToolResult, any, error) {
	if len(args.URLs) == 0 {
		return errorResult("urls is required (non-empty list)"), nil, nil
	}
	if len(args.URLs) > BatchURLLimit {
		return errorResult(
			"too many urls (%d > %d) — for larger batches run `trawl batch` on the CLI for persistent frontier + resume",
			len(args.URLs), BatchURLLimit,
		), nil, nil
	}

	timeout := 30 * time.Second
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	concurrency := args.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	// Spawn a transient job dir under the OS temp root. We delete it
	// at the end — MCP tool calls are not designed to survive
	// SIGINT/resume the way CLI batches are.
	tmpDir, err := os.MkdirTemp("", "trawl-mcp-batch-*")
	if err != nil {
		return errorResult("could not create temp job dir: %s", err.Error()), nil, nil
	}
	defer os.RemoveAll(tmpDir)

	id := job.NewID()
	jobDir := filepath.Join(tmpDir, id)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return errorResult("mkdir job dir: %s", err.Error()), nil, nil
	}
	outputPath := filepath.Join(jobDir, "results.jsonl")

	cfg := &job.Config{
		ID:           id,
		CreatedAt:    time.Now().UTC(),
		Selectors:    args.Selectors,
		OutputPath:   outputPath,
		Concurrency:  concurrency,
		IgnoreRobots: args.IgnoreRobots,
		RatePerSec:   args.RatePerSec,
		Timeout:      timeout.String(),
		Tiers:        args.Tiers,
		ForceTier:    args.ForceTier,
		Format:       args.Format,
		Readability:  args.Readability,
		NoMetadata:   args.NoMetadata,
		SchemaPath:   args.SchemaPath,
		BrowserLike:  args.BrowserLike,
		Stealth:      args.Stealth,
		TLSMatch:     args.TLSMatch,
		OCR:          args.OCR,
		OCRLang:      args.OCRLang,
	}

	if err := enqueueURLs(jobDir, args.URLs); err != nil {
		return errorResult("enqueue: %s", err.Error()), nil, nil
	}

	if err := job.Run(ctx, jobDir, cfg); err != nil {
		return errorResult("batch run: %s", err.Error()), nil, nil
	}

	records, err := readJSONLRecords(outputPath)
	if err != nil {
		return errorResult("read results: %s", err.Error()), nil, nil
	}

	res, err := recordResult(records)
	if err != nil {
		return nil, nil, err
	}
	return res, nil, nil
}

// enqueueURLs seeds the frontier at jobDir with the given URLs.
// Skips invalid URLs silently — invalid lines are logged but don't
// fail the whole call (mirrors the CLI batch behavior).
func enqueueURLs(jobDir string, urls []string) error {
	f, err := frontier.Open(filepath.Join(jobDir, "frontier"))
	if err != nil {
		return fmt.Errorf("open frontier: %w", err)
	}
	defer f.Close()

	for _, u := range urls {
		if _, _, err := f.EnqueueWithFallback(u, ""); err != nil {
			// Log but continue — the resulting record set will simply
			// omit the bad URL. The caller can spot missing rows by
			// counting input vs output.
			continue
		}
	}
	return nil
}

// readJSONLRecords reads a JSONL file and returns its records. Used
// to collect job.Run's output back into memory after it ran. Order
// is the order workers wrote rows (NOT input order — concurrency).
func readJSONLRecords(path string) ([]output.Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var records []output.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20) // 4MB max line — handles big bodies
	for sc.Scan() {
		var r output.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("parse jsonl: %w", err)
		}
		records = append(records, r)
	}
	return records, sc.Err()
}
