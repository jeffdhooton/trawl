package mcp

import (
	"context"
	"time"

	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// scrapeArgs is the input schema for the trawl_scrape tool.
//
// jsonschema tags are surfaced to the agent verbatim — they ARE the
// tool's documented surface. Keep them concrete and grounded in real
// use cases rather than abstract rephrasings of the field name.
type scrapeArgs struct {
	URL string `json:"url" jsonschema:"the URL to scrape"`

	// Content extraction.
	Format        string   `json:"format,omitempty" jsonschema:"body output format: 'markdown' (clean text, default for most uses), 'html' (raw), 'json' (when the page is already JSON), or '' (omit body field). Most agent use cases want 'markdown'."`
	Readability   bool     `json:"readability,omitempty" jsonschema:"strip nav/footer/ads boilerplate before extraction. Recommended for article-shaped pages."`
	NoMetadata    bool     `json:"noMetadata,omitempty" jsonschema:"skip the automatic page-metadata extractor (title, OG, canonical, JSON-LD). Off by default."`
	Selectors     []string `json:"selectors,omitempty" jsonschema:"CSS extraction rules in 'name=selector' form (e.g. ['title=h1', 'price=.price']). Each match becomes a key in the extracted object."`
	SchemaPath    string   `json:"schemaPath,omitempty" jsonschema:"path to a YAML/JSON schema file for nested structured extraction. See trawl docs/examples/."`
	ScreenshotDir string   `json:"screenshotDir,omitempty" jsonschema:"directory to write a full-page PNG into. Only chromium-served pages produce a file."`

	// Routing / behavior.
	Tiers     string `json:"tiers,omitempty" jsonschema:"comma-separated engine ladder, default 'http,chromium'. Set to 'http' to disable browser escalation; set to 'chromium' to force browser."`
	ForceTier string `json:"forceTier,omitempty" jsonschema:"pin a single engine for this call (overrides tiers). One of 'http', 'chromium'."`
	TimeoutMS int    `json:"timeoutMs,omitempty" jsonschema:"per-request timeout in milliseconds. Default 30000."`

	// Politeness.
	IgnoreRobots bool `json:"ignoreRobots,omitempty" jsonschema:"bypass robots.txt for this fetch. Off by default. Use sparingly — robots.txt exists for a reason."`

	// Anti-detection (Tier 1+2).
	BrowserLike bool   `json:"browserLike,omitempty" jsonschema:"enable Tier 1 browser mimicry: rotating UA, full Chrome header set, in-memory cookie jar, jittered timing. Use when basic Chrome-like requests get through but the default trawl UA does not."`
	Stealth     bool   `json:"stealth,omitempty" jsonschema:"Tier 2: chromium injects a stealth init script (navigator.webdriver, plugins, WebGL fingerprints) before navigation. Only effective when chromium tier is reached."`
	TLSMatch    string `json:"tlsMatch,omitempty" jsonschema:"Tier 3: forge ClientHello to match a real browser. Currently only 'chrome' supported. Affects only the http engine."`

	// Pre-scrape interactive actions (chromium only).
	Actions []string `json:"actions,omitempty" jsonschema:"chromium pre-scrape actions, each as a single string: 'click:.btn', 'wait:#el', 'scroll:bottom', 'type:#in:text', 'sleep:2s', 'evaluate:js'. Ignored by the http tier."`

	// PDF.
	OCR     bool   `json:"ocr,omitempty" jsonschema:"enable Tier 3 OCR for scanned PDFs (requires tesseract + pdftoppm on PATH). Off by default."`
	OCRLang string `json:"ocrLang,omitempty" jsonschema:"tesseract language pack (e.g. 'eng', 'eng+deu'). Default 'eng'."`
}

func (a scrapeArgs) toJobOpts() job.ScrapeOpts {
	timeout := 30 * time.Second
	if a.TimeoutMS > 0 {
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	return job.ScrapeOpts{
		Selectors:     a.Selectors,
		IgnoreRobots:  a.IgnoreRobots,
		Timeout:       timeout,
		Tiers:         a.Tiers,
		ForceTier:     a.ForceTier,
		Format:        a.Format,
		Readability:   a.Readability,
		NoMetadata:    a.NoMetadata,
		ScreenshotDir: a.ScreenshotDir,
		SchemaPath:    a.SchemaPath,
		InlineActions: a.Actions,
		Evasion: job.EvasionOpts{
			BrowserLike: a.BrowserLike,
			Stealth:     a.Stealth,
			TLSMatch:    a.TLSMatch,
		},
		PDF: job.PDFOpts{
			OCR:     a.OCR,
			OCRLang: a.OCRLang,
		},
	}
}

func registerScrapeTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trawl_scrape",
		Title:       "Scrape one URL",
		Description: scrapeToolDescription,
	}, scrapeHandler)
}

const scrapeToolDescription = `Fetch one URL through trawl's tiered router (HTTP first, ` +
	`chromium escalation when the body is empty/stub) and return a single ` +
	`structured record. The record includes the fetched body (in your ` +
	`requested format), automatic page metadata (title, OG, canonical, ` +
	`JSON-LD), any --selector or schema extractions, and routing telemetry ` +
	`(tier used, status code, duration).

Use this when:
  - You want clean markdown / structured fields from one specific URL.
  - The agent flow needs the page contents now (synchronous, sub-second
    for HTTP-tier pages, a few seconds for chromium escalations).
  - You're sampling one page from a site before deciding whether to
    crawl it.

Do NOT use for:
  - Bulk URL lists — use trawl_batch instead.
  - Walking a site you don't have a URL list for — use trawl_crawl.
  - Discovering URLs on a site — use trawl_map or trawl_sitemap.`

func scrapeHandler(ctx context.Context, _ *mcp.CallToolRequest, args scrapeArgs) (*mcp.CallToolResult, any, error) {
	if args.URL == "" {
		return errorResult("url is required"), nil, nil
	}
	rec, err := job.RunOne(ctx, args.URL, args.toJobOpts())
	if err != nil {
		// RunOne returns an error for setup-time failures (bad URL,
		// blocked by robots) AND for fetch failures where the record
		// is still populated. The latter case has rec.URL set — emit
		// the record so the agent sees the failure_category instead
		// of just an opaque error string.
		if rec.URL != "" || rec.CanonicalURL != "" {
			res, _ := recordResult([]output.Record{rec})
			return res, nil, nil
		}
		return errorResult("scrape failed: %s", err.Error()), nil, nil
	}
	res, err := recordResult([]output.Record{rec})
	if err != nil {
		return nil, nil, err
	}
	return res, nil, nil
}
