package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jeffdhooton/trawl/internal/frontier"
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// crawlArgs is the input schema for trawl_crawl.
type crawlArgs struct {
	Seed  string `json:"seed" jsonschema:"the seed URL to start the BFS crawl from"`
	Depth int    `json:"depth" jsonschema:"max BFS depth relative to the seed (seed is depth 0). Recommended 1-3."`
	Limit int    `json:"limit" jsonschema:"hard cap on URLs enqueued (seed + children). REQUIRED. Capped at 500 by the MCP tool — for larger crawls use 'trawl crawl' on the CLI."`

	SameDomain bool `json:"sameDomain,omitempty" jsonschema:"only follow links on the same host as the seed (default true)"`

	Format      string   `json:"format,omitempty" jsonschema:"body output format: 'markdown', 'html', 'json', or '' (omit body)"`
	Readability bool     `json:"readability,omitempty" jsonschema:"strip nav/footer/ads boilerplate before extraction"`
	NoMetadata  bool     `json:"noMetadata,omitempty" jsonschema:"skip the automatic page-metadata extractor"`
	Selectors   []string `json:"selectors,omitempty" jsonschema:"CSS extraction rules in 'name=selector' form"`
	SchemaPath  string   `json:"schemaPath,omitempty" jsonschema:"path to a YAML/JSON schema for nested structured extraction"`

	Tiers       string  `json:"tiers,omitempty" jsonschema:"engine ladder, default 'http,chromium'"`
	ForceTier   string  `json:"forceTier,omitempty" jsonschema:"pin a single engine: 'http' or 'chromium'"`
	TimeoutMS   int     `json:"timeoutMs,omitempty" jsonschema:"per-request timeout in milliseconds, default 30000"`
	Concurrency int     `json:"concurrency,omitempty" jsonschema:"max concurrent in-flight requests, default 8"`
	RatePerSec  float64 `json:"ratePerSec,omitempty" jsonschema:"requests per second per domain, default 1"`

	IgnoreRobots bool `json:"ignoreRobots,omitempty" jsonschema:"bypass robots.txt for this crawl. Off by default."`

	BrowserLike bool   `json:"browserLike,omitempty" jsonschema:"enable Tier 1 browser mimicry"`
	Stealth     bool   `json:"stealth,omitempty" jsonschema:"Tier 2 chromium stealth init script"`
	TLSMatch    string `json:"tlsMatch,omitempty" jsonschema:"Tier 3: forge ClientHello to match a real browser"`

	OCR     bool   `json:"ocr,omitempty" jsonschema:"enable Tier 3 OCR for scanned PDFs"`
	OCRLang string `json:"ocrLang,omitempty" jsonschema:"tesseract language pack, default 'eng'"`
}

func registerCrawlTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trawl_crawl",
		Title:       "BFS-crawl a site",
		Description: crawlToolDescription,
	}, crawlHandler)
}

const crawlToolDescription = `Walk a site breadth-first from a seed URL, fetching each discovered ` +
	`page through the tiered router and returning structured records. ` +
	`Same-domain by default. Link discovery runs on the body of every ` +
	`successful fetch.

Use this when:
  - You need clean content from many pages on one site (e.g. "give me
    every page on docs.example.com").
  - The site has no sitemap or the sitemap is incomplete.
  - You can't enumerate URLs up front and want depth/breadth-controlled
    discovery.

Required: depth and limit. Limit is capped at 500 per call. For larger
crawls use 'trawl crawl' on the CLI for persistent frontier + resume.

Do NOT use for:
  - URL discovery without fetching content — use trawl_map (much
    faster, no body extraction).
  - One specific URL — use trawl_scrape.
  - A precomputed list of URLs — use trawl_batch.`

func crawlHandler(ctx context.Context, _ *mcp.CallToolRequest, args crawlArgs) (*mcp.CallToolResult, any, error) {
	if args.Seed == "" {
		return errorResult("seed is required"), nil, nil
	}
	if args.Limit <= 0 {
		return errorResult("limit is required and must be > 0"), nil, nil
	}
	if args.Limit > CrawlURLLimit {
		return errorResult(
			"limit too large (%d > %d) — for larger crawls run `trawl crawl` on the CLI for persistent frontier + resume",
			args.Limit, CrawlURLLimit,
		), nil, nil
	}
	if args.Depth < 0 {
		return errorResult("depth must be >= 0"), nil, nil
	}

	timeout := 30 * time.Second
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	concurrency := args.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	tmpDir, err := os.MkdirTemp("", "trawl-mcp-crawl-*")
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
		ID:              id,
		CreatedAt:       time.Now().UTC(),
		Selectors:       args.Selectors,
		OutputPath:      outputPath,
		Concurrency:     concurrency,
		IgnoreRobots:    args.IgnoreRobots,
		RatePerSec:      args.RatePerSec,
		Timeout:         timeout.String(),
		Tiers:           args.Tiers,
		ForceTier:       args.ForceTier,
		Format:          args.Format,
		Readability:     args.Readability,
		NoMetadata:      args.NoMetadata,
		SchemaPath:      args.SchemaPath,
		BrowserLike:     args.BrowserLike,
		Stealth:         args.Stealth,
		TLSMatch:        args.TLSMatch,
		OCR:             args.OCR,
		OCRLang:         args.OCRLang,
		CrawlMode:       true,
		CrawlMaxDepth:   args.Depth,
		CrawlSameDomain: args.SameDomain,
		CrawlLimit:      args.Limit,
		CrawlSeed:       args.Seed,
	}

	if err := seedCrawlFrontier(jobDir, args.Seed); err != nil {
		return errorResult("seed: %s", err.Error()), nil, nil
	}

	if err := job.Run(ctx, jobDir, cfg); err != nil {
		return errorResult("crawl run: %s", err.Error()), nil, nil
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

// seedCrawlFrontier enqueues the seed URL at depth 0. Mirrors
// cmd/trawl/crawl.go's enqueueSeed but doesn't log (MCP servers
// stay quiet to keep stdout clean for JSON-RPC framing).
func seedCrawlFrontier(jobDir, seedURL string) error {
	f, err := frontier.Open(filepath.Join(jobDir, "frontier"))
	if err != nil {
		return fmt.Errorf("open frontier: %w", err)
	}
	defer f.Close()
	if _, _, err := f.EnqueueWithDepth(seedURL, 0); err != nil {
		return fmt.Errorf("enqueue seed: %w", err)
	}
	return nil
}
