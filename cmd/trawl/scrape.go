package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jeffdhooton/trawl/internal/action"
	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/failure"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/jeffdhooton/trawl/internal/pdf"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/jeffdhooton/trawl/internal/schema"

	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type scrapeOpts struct {
	selectors      []string
	outputPath     string
	ignoreRobots   bool
	timeout        time.Duration
	tiers          string
	forceTier      string
	noTierLearning bool
	tierCachePath  string
	format         string
	readability    bool
	noMetadata     bool
	screenshotDir  string
	cacheEnabled   bool
	cacheTTL       time.Duration
	cachePath      string
	schemaPath     string
	csvColumns     []string
	retries        int
	retryDelay     time.Duration
	politenessPath string
	proxy          proxyOpts
	evasion        evasionOpts
	inlineActions  []string
	actionsPath    string
	pdf            pdfFlags
}

func newScrapeCmd() *cobra.Command {
	var opts scrapeOpts

	cmd := &cobra.Command{
		Use:   "scrape <url>",
		Short: "Scrape a single URL",
		Long: `Scrape a single URL through the HTTP tier and emit one JSONL record.

Use --selector name=css multiple times to extract structured fields:

  trawl scrape https://example.com \
    --selector "title=h1" \
    --selector "price=.price" \
    --selector "images=img.pic[]@src"
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScrape(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().StringArrayVarP(&opts.selectors, "selector", "s", nil,
		`extraction rule: "name=selector" (repeatable)`)
	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "-",
		`output file (JSONL) — "-" for stdout`)
	cmd.Flags().BoolVar(&opts.ignoreRobots, "ignore-robots", false,
		"bypass robots.txt (logs a warning)")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 30*time.Second,
		"HTTP request timeout")
	cmd.Flags().StringVar(&opts.tiers, "tiers", "http,chromium",
		"comma-separated engine tiers to try in order (http, chromium)")
	cmd.Flags().StringVar(&opts.forceTier, "force-tier", "",
		"pin a single tier for this run (overrides --tiers)")
	cmd.Flags().BoolVar(&opts.noTierLearning, "no-tier-learning", false,
		"disable the cross-job host→tier cache")
	cmd.Flags().StringVar(&opts.tierCachePath, "tier-cache-path", "",
		"override the default tier-cache directory ($TRAWL_HOME/tier-cache)")
	cmd.Flags().StringVar(&opts.format, "format", "",
		`body format in the output record: "html", "markdown", or "json". Empty omits the body field.`)
	cmd.Flags().BoolVar(&opts.readability, "readability", false,
		"strip nav/footer/ads boilerplate before CSS extraction and markdown conversion")
	cmd.Flags().BoolVar(&opts.noMetadata, "no-metadata", false,
		"skip automatic page metadata extraction (title, OG, canonical, JSON-LD)")
	cmd.Flags().StringVar(&opts.screenshotDir, "screenshot-dir", "",
		"directory to write full-page PNG screenshots into. Only chromium-served pages produce a file.")
	cmd.Flags().BoolVar(&opts.cacheEnabled, "cache", false,
		"opt in to the cross-job content cache. Cached entries short-circuit the tier loop on hit.")
	cmd.Flags().DurationVar(&opts.cacheTTL, "cache-ttl", 24*time.Hour,
		"max age of a cache entry before it counts as a miss. 0 = never expire.")
	cmd.Flags().StringVar(&opts.cachePath, "cache-path", "",
		"override the default content-cache directory ($TRAWL_HOME/content-cache)")
	cmd.Flags().StringVar(&opts.schemaPath, "schema", "",
		"YAML/JSON schema file for structured extraction (see docs/examples/)")
	cmd.Flags().StringSliceVar(&opts.csvColumns, "csv-columns", nil,
		"comma-separated columns for CSV output (dot-paths like extracted.title). "+
			"Only valid when -o ends in .csv or .tsv. If unset, base columns plus "+
			"auto-discovered extracted.* keys from the first record are used.")
	cmd.Flags().IntVar(&opts.retries, "retries", 2,
		"max retry attempts for transient HTTP failures (429, 5xx, connection errors). 0 disables retries.")
	cmd.Flags().DurationVar(&opts.retryDelay, "retry-delay", 500*time.Millisecond,
		"base delay for exponential backoff between retries (±25% jitter, capped at 10s)")
	cmd.Flags().StringVar(&opts.politenessPath, "politeness", "",
		"YAML file with per-host rate/concurrency overrides (see docs/examples/politeness.yaml)")
	cmd.Flags().StringArrayVar(&opts.inlineActions, "action", nil,
		`pre-scrape interaction: "click:.btn", "wait:#el", "scroll:bottom", "type:#in:text", "sleep:2s", "evaluate:js" (repeatable, chromium only)`)
	cmd.Flags().StringVar(&opts.actionsPath, "actions", "",
		"YAML/JSON file with a sequence of pre-scrape actions (chromium only)")
	registerProxyFlags(cmd, &opts.proxy)
	registerEvasionFlags(cmd, &opts.evasion)
	registerPDFFlags(cmd, &opts.pdf)

	return cmd
}

func runScrape(parentCtx context.Context, rawURL string, opts scrapeOpts) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	canonURL, err := canonical.Canonicalize(rawURL, canonical.Options{})
	if err != nil {
		return fmt.Errorf("canonicalize: %w", err)
	}

	fields, err := parseFieldSpecs(opts.selectors)
	if err != nil {
		return err
	}

	if len(opts.csvColumns) > 0 && !output.IsCSVPath(opts.outputPath) {
		return fmt.Errorf("--csv-columns is only valid with a .csv or .tsv output path")
	}
	sink, err := output.NewFile(opts.outputPath, opts.csvColumns)
	if err != nil {
		return err
	}
	defer sink.Close()

	httpCfg := engine.DefaultHTTPConfig()
	httpCfg.Timeout = opts.timeout
	httpCfg.MaxRetries = opts.retries
	if opts.retryDelay > 0 {
		httpCfg.RetryBaseDelay = opts.retryDelay
	}
	gateCfg := politeness.Default()
	gateCfg.UserAgent = httpCfg.UserAgent
	gateCfg.IgnoreRobots = opts.ignoreRobots
	chromiumCfg := engine.DefaultChromiumConfig()
	if err := applyEvasion(&httpCfg, &gateCfg, &chromiumCfg, opts.evasion); err != nil {
		return err
	}
	pr, err := applyProxy(&httpCfg, &chromiumCfg, opts.proxy)
	if err != nil {
		return err
	}
	tiers := parseTierList(opts.tiers)
	if len(tiers) == 0 && opts.forceTier == "" {
		tiers = []string{"http", "chromium"}
	}
	r, err := buildRouter(tiers, opts.forceTier, httpCfg, chromiumCfg)
	if err != nil {
		return err
	}
	defer r.Close()

	r.WithProxyRotation(pr.rotator, pr.rotateCodes, pr.rotateMax)

	tierCache, _, _ := openTierCache(opts.noTierLearning, opts.tierCachePath)
	defer tierCache.Close()
	r.WithCache(tierCache)

	contentCache, contentCachePath, _ := openContentCache(opts.cacheEnabled, opts.cachePath, opts.cacheTTL)
	defer contentCache.Close()
	r.WithContentCache(contentCache)
	if contentCachePath != "" {
		log.Info().
			Str("content_cache", contentCachePath).
			Str("ttl", opts.cacheTTL.String()).
			Msg("content cache enabled")
	}

	gate := politeness.NewGate(gateCfg, nil)
	if opts.politenessPath != "" {
		hr, err := politeness.LoadHostRules(opts.politenessPath)
		if err != nil {
			return fmt.Errorf("load politeness rules: %w", err)
		}
		gate.WithHostRules(hr)
		log.Info().Str("politeness", opts.politenessPath).Int("rules", len(hr.Hosts)).Msg("per-host politeness rules loaded")
	}

	if opts.ignoreRobots {
		log.Warn().Str("url", canonURL).Msg("robots.txt is being ignored")
	}
	logEvasion(opts.evasion)
	logProxy(opts.proxy)

	allowed, err := gate.Allowed(ctx, canonURL)
	if err != nil {
		return fmt.Errorf("check robots: %w", err)
	}
	if !allowed {
		return fmt.Errorf("blocked by robots.txt: %s", canonURL)
	}

	release, jitterMS, err := gate.Acquire(ctx, canonURL)
	if err != nil {
		return fmt.Errorf("politeness: %w", err)
	}
	defer release()

	if err := validatePDFFlags(opts.pdf); err != nil {
		return err
	}
	logPDFConfig(opts.pdf)

	copts := contentOpts{
		format:        opts.format,
		readability:   opts.readability,
		noMetadata:    opts.noMetadata,
		screenshotDir: opts.screenshotDir,
		pdfOpts:       applyPDFFlags(opts.pdf),
	}
	if err := validateFormat(copts.format); err != nil {
		return err
	}
	if opts.schemaPath != "" {
		s, err := schema.Load(opts.schemaPath)
		if err != nil {
			return fmt.Errorf("load schema: %w", err)
		}
		copts.schema = s
	}
	if cda, err := parseActions(opts.inlineActions, opts.actionsPath); err != nil {
		return err
	} else if cda != nil {
		copts.actions = cda
	}

	rec, best, routeErr := routeAndBuildWithResult(ctx, r, canonURL, rawURL, fields, copts)
	stampEvasion(&rec, best, jitterMS)
	if err := sink.Write(rec); err != nil {
		return fmt.Errorf("write record: %w", err)
	}
	if routeErr != nil {
		return routeErr
	}
	if rec.Error != "" {
		return errors.New(rec.Error)
	}
	return nil
}

// stampEvasion combines the engine-side evasion info (BrowserLike,
// Stealth, UserAgent — populated by the HTTP/chromium engines on the
// Result) with the gate-side jitter delay (measured by Acquire) and
// writes both onto rec.Metadata.Evasion. When neither side reports
// any evasion, rec.Metadata.Evasion stays nil and the JSONL output
// is byte-identical to the pre-evasion default. Both inputs may be
// zero/nil — failed fetches still get whatever evasion partial-state
// the engine managed to record.
func stampEvasion(rec *output.Record, best *engine.Result, jitterMS int64) {
	hasEngine := best != nil && best.Evasion != nil
	if !hasEngine && jitterMS == 0 {
		return
	}
	stats := output.EvasionStats{JitterMS: jitterMS}
	if hasEngine {
		stats.BrowserLike = best.Evasion.BrowserLike
		stats.Stealth = best.Evasion.Stealth
		stats.UserAgent = best.Evasion.UserAgent
		stats.TLSMatch = best.Evasion.TLSMatch
		stats.Proxy = best.Evasion.Proxy
	}
	rec.Metadata.Evasion = &stats
}

func parseFieldSpecs(specs []string) ([]extract.Field, error) {
	fields := make([]extract.Field, 0, len(specs))
	for _, spec := range specs {
		f, err := extract.ParseFieldSpec(spec)
		if err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// contentOpts bundles the content-extraction knobs that scrape/batch
// expose and thread through routeAndBuild. Kept as a struct so adding
// new content-phase features (e.g. --format xml) doesn't require
// touching every call site.
type contentOpts struct {
	// format is "", "html", "markdown", or "json". Empty means "don't emit a
	// body field" — preserves backward compatibility with existing
	// JSONL consumers that never asked for one.
	format string
	// readability runs boilerplate removal on the body BEFORE CSS
	// extraction and markdown conversion. Metadata extraction still
	// uses the original body because readability strips <head> content.
	readability bool
	// noMetadata skips automatic PageMetadata scraping. Escape hatch;
	// off by default since metadata extraction is cheap.
	noMetadata bool
	// screenshotDir, when non-empty, asks the router to set
	// Request.WantScreenshot and writes any returned PNG to
	// <dir>/<sha256-of-canonical-url>.png. Only chromium produces
	// screenshots; HTTP-served records leave metadata.screenshot_path
	// empty.
	screenshotDir string
	// schema, when non-nil, runs nested structured extraction against
	// the same contentBody as the flat CSS extractor. Results are
	// merged into Record.Extracted under the schema's field names;
	// schema keys win on collision with --selector flat keys.
	schema *schema.Schema
	// actions are pre-scrape chromedp actions (click, scroll, wait,
	// etc.) that run after page load before DOM capture. Only the
	// chromium engine executes them; HTTP ignores them.
	actions []chromedp.Action
	// pdfOpts controls the PDF engine's tier behavior for responses
	// whose Content-Type is application/pdf. Zero value means "Tier
	// 1+2 only, no OCR" — the default when --ocr isn't set. Phase 5
	// wires the CLI flags that populate the non-zero fields.
	pdfOpts pdf.Opts
}

// routeAndBuild runs one URL through the tiered router and builds the
// output.Record. It returns a non-nil error only when every tier failed or
// the failure is non-escalatable (e.g. 404, unsupported content-type); in
// those cases the record is still populated with whatever evidence the last
// attempt captured, so callers can persist it.
func routeAndBuild(ctx context.Context, r *router.Router, canonURL, origURL string, fields []extract.Field, copts contentOpts) (output.Record, error) {
	rec, _, err := routeAndBuildWithResult(ctx, r, canonURL, origURL, fields, copts)
	return rec, err
}

// routeAndBuildWithResult is routeAndBuild that additionally exposes the
// raw engine.Result used to populate the record. The crawl worker needs
// the body for link discovery; returning it here avoids a second fetch.
// The returned *engine.Result may be nil when every tier failed without
// producing any result at all (DNS failure, etc).
func routeAndBuildWithResult(ctx context.Context, r *router.Router, canonURL, origURL string, fields []extract.Field, copts contentOpts) (output.Record, *engine.Result, error) {
	outcome, routeErr := r.Route(ctx, engine.Request{
		URL:            canonURL,
		WantScreenshot: copts.screenshotDir != "",
		Actions:        copts.actions,
	})

	// Pick the best result available for the record (success > last attempt).
	best := outcome.Result
	if best == nil {
		best = outcome.LastResult
	}

	rec := output.Record{
		URL:          origURL,
		CanonicalURL: canonURL,
		FetchedAt:    time.Now().UTC(),
		Tier:         outcome.Tier,
	}
	if best != nil {
		rec.StatusCode = best.StatusCode
		rec.DurationMS = best.Duration.Milliseconds()
		rec.ContentHash = hashBody(best.Body)
		rec.Metadata = output.Metadata{
			ContentType: best.ContentType,
			BodyBytes:   len(best.Body),
			FinalURL:    best.FinalURL,
			Redirects:   best.Redirects,
		}
	}

	if routeErr != nil {
		if rec.Tier == "" && len(outcome.Attempts) > 0 {
			rec.Tier = outcome.Attempts[len(outcome.Attempts)-1].Tier
		}
		rec.Error = routeErr.Error()
		rec.FailureCategory = string(failure.Classify(routeErr, rec.StatusCode, rec.Error))
		return rec, best, routeErr
	}

	// PDF transform runs BEFORE the HTML-gated pipeline. On success,
	// best.Body becomes extracted markdown and best.ContentType flips
	// to text/markdown, so the isHTML() gates below correctly skip
	// HTML-specific steps. On hard extraction failure (encrypted,
	// damaged, empty) rec.Error is set and we short-circuit the
	// pipeline — the raw bytes stay on best.Body for downstream.
	// On soft-fail (pdftotext missing) the raw PDF bytes are preserved
	// and we still short-circuit so no HTML pipeline runs on PDF bytes.
	if wasPDF := transformPDFIfNeeded(best, &rec, copts.pdfOpts); wasPDF && rec.Error != "" {
		return rec, best, nil
	}

	// PDF title is the most useful signal when a consumer is doing
	// `jq '.metadata.page.title'` across mixed HTML+PDF batches. Copy
	// it into metadata.page.title when the HTML metadata extractor
	// won't run (PDFs skip isHTML).
	if rec.Metadata.PDF != nil && rec.Metadata.PDF.Title != "" {
		if rec.Metadata.Page == nil {
			rec.Metadata.Page = &extract.PageMetadata{}
		}
		if rec.Metadata.Page.Title == "" {
			rec.Metadata.Page.Title = rec.Metadata.PDF.Title
		}
	}

	// Page metadata is extracted from the ORIGINAL body, before any
	// readability pass strips the <head>. og:* tags, canonical URL,
	// and JSON-LD all live in <head> and must survive.
	if best != nil && !copts.noMetadata && isHTML(best.ContentType) {
		if pm := extract.Metadata(best.Body, rec.CanonicalURL); pm != nil {
			rec.Metadata.Page = pm
		}
	}

	// Content body for CSS extraction, markdown conversion, and the
	// Record.Body field. If --readability is on, run the boilerplate
	// remover first — its output becomes the source for everything
	// below. On failure Readable returns the original body, so this
	// is always safe.
	contentBody := []byte{}
	if best != nil {
		contentBody = best.Body
		if copts.readability && isHTML(best.ContentType) {
			contentBody = extract.Readable(best.Body, rec.CanonicalURL)
		}
	}

	if len(fields) > 0 && best != nil {
		ex, err := extract.CSS(contentBody, fields)
		if err != nil {
			rec.Error = "extract: " + err.Error()
			rec.FailureCategory = string(failure.CatExtractionFailed)
			return rec, best, nil
		}
		rec.Metadata.Extraction = &output.ExtractionStats{
			Fields: len(fields),
			Hits:   len(ex),
		}
		if len(ex) > 0 {
			rec.Extracted = ex
		}
	}

	// Schema-based structured extraction. Runs on the SAME contentBody
	// as the flat CSS extractor, so --readability applies uniformly.
	// Schema results merge into rec.Extracted; schema keys win on
	// collision with --selector flat keys (schema is more specific).
	if copts.schema != nil && best != nil && isHTML(best.ContentType) {
		sx, err := schema.Extract(contentBody, rec.CanonicalURL, copts.schema)
		if err != nil {
			rec.Error = "schema: " + err.Error()
			rec.FailureCategory = string(failure.CatExtractionFailed)
			return rec, best, nil
		}
		if rec.Extracted == nil {
			rec.Extracted = map[string]any{}
		}
		for k, v := range sx {
			rec.Extracted[k] = v
		}
	}

	// Populate the Body field if --format was set. Empty format means
	// existing JSONL consumers stay backward-compatible (no body in the
	// record). html passes through verbatim; markdown runs the
	// converter. When the source was a PDF, --format html is silently
	// upgraded to markdown — PDF bytes aren't useful HTML, and the
	// extracted text already is valid CommonMark.
	if best != nil && copts.format != "" {
		fromPDF := rec.Metadata.PDF != nil
		switch copts.format {
		case "html":
			if fromPDF {
				pdfFormatOverrideOnce.Do(func() {
					log.Warn().
						Str("url", rec.CanonicalURL).
						Msg("--format html requested on PDF response; emitting markdown (PDF bytes are not useful HTML)")
				})
				rec.Body = string(contentBody)
				rec.BodyFormat = "markdown"
			} else {
				rec.Body = string(contentBody)
				rec.BodyFormat = "html"
			}
		case "markdown":
			if fromPDF {
				// contentBody is already the PDF-extracted markdown;
				// running it through the HTML-to-markdown converter
				// would corrupt it.
				rec.Body = string(contentBody)
				rec.BodyFormat = "markdown"
				break
			}
			md, err := extract.ToMarkdown(contentBody, rec.CanonicalURL)
			if err != nil {
				// Converter failure → log into Metadata but don't fail
				// the row. Markdown is best-effort; downstream can fall
				// back to re-running the request or ignoring the row.
				rec.Body = md // fallback is the raw HTML per ToMarkdown contract
				rec.BodyFormat = "html"
			} else {
				rec.Body = md
				rec.BodyFormat = "markdown"
			}
		case "json":
			if rec.Extracted != nil {
				j, _ := json.Marshal(rec.Extracted)
				rec.Body = string(j)
			} else {
				rec.Body = "{}"
			}
			rec.BodyFormat = "json"
		}
	}

	// Screenshot: write the PNG to <dir>/<sha256-of-canonical-url>.png if
	// the engine captured one. Only chromium produces screenshots; HTTP-
	// served rows leave metadata.screenshot_path empty. On I/O failure we
	// log and continue — screenshots are best-effort.
	if best != nil && len(best.Screenshot) > 0 && copts.screenshotDir != "" {
		if path, werr := writeScreenshot(copts.screenshotDir, rec.CanonicalURL, best.Screenshot); werr != nil {
			log.Warn().Str("url", rec.CanonicalURL).Err(werr).Msg("screenshot write failed")
		} else {
			rec.Metadata.ScreenshotPath = path
		}
	}

	// from_cache flag is surfaced from the router outcome so downstream
	// consumers can distinguish "live fetch" rows from "served from cache."
	if outcome.FromCache {
		rec.Metadata.FromCache = true
	}

	// Final classification — uses the router error (if any), the final
	// status code, and the formatted reason field together.
	rec.FailureCategory = string(failure.Classify(routeErr, rec.StatusCode, rec.Error))
	return rec, best, nil
}

// writeScreenshot persists a PNG to <dir>/<sha256-of-url>.png. The
// filename is deterministic so re-running the same scrape overwrites
// the previous capture rather than accumulating dup files.
func writeScreenshot(dir, canonURL string, png []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	sum := sha256.Sum256([]byte(canonURL))
	name := hex.EncodeToString(sum[:]) + ".png"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil // fall back to the relative path on Abs failure
	}
	return abs, nil
}

// validateFormat returns an error if the --format value isn't one of the
// supported choices. Empty is valid and means "don't emit the body field."
func validateFormat(format string) error {
	switch format {
	case "", "html", "markdown", "json":
		return nil
	default:
		return fmt.Errorf("invalid --format %q (want html, markdown, json, or empty)", format)
	}
}

// isHTML returns true if the Content-Type header smells like HTML.
// Metadata, readability, and markdown conversion only make sense on
// HTML bodies; applying them to application/json would be nonsensical.
func isHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return ct == "" || strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// isPDF reports whether a Content-Type header indicates a PDF response.
// Unlike isHTML, empty content-type is NOT treated as PDF — we only
// transform bytes when the server explicitly labeled them.
func isPDF(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "application/pdf") || strings.Contains(ct, "application/x-pdf")
}

// pdfToolingHintOnce logs the "install poppler-utils" hint at most once
// per run so batch jobs don't spam N identical lines. Reset per-process
// is fine — trawl runs are short-lived.
var pdfToolingHintOnce sync.Once

// pdfFormatOverrideOnce logs the "--format html on PDF emits markdown"
// warning once per run. PDF.md §6.2 established that PDF bytes aren't
// useful HTML, so we override silently-but-transparently.
var pdfFormatOverrideOnce sync.Once

// transformPDFIfNeeded runs internal/pdf.Extract when the result's
// Content-Type indicates a PDF. On success, it replaces best.Body with
// extracted markdown, swaps best.ContentType to text/markdown, and
// populates rec.Metadata.PDF. On soft-fail (missing pdftotext), it sets
// rec.FailureCategory so the row flows through as a reachable
// extraction failure with the raw PDF bytes intact. On hard-fail
// (ErrEncrypted, damaged PDF, ErrEmptyExtraction), it sets rec.Error
// and returns — the caller treats this as a row-level extraction error
// and stops the HTML-gated pipeline from running against PDF bytes.
//
// Returns true when the transform (attempted or completed) should stop
// the HTML-gated pipeline from running. For soft-fail with raw bytes
// preserved, callers should still skip HTML-shaped processing because
// the bytes are PDF, not HTML.
func transformPDFIfNeeded(best *engine.Result, rec *output.Record, opts pdf.Opts) bool {
	if best == nil || !isPDF(best.ContentType) {
		return false
	}

	md, info, err := pdf.Extract(best.Body, opts)
	if err != nil {
		// Missing binary → soft-fail: keep the raw PDF in best.Body so
		// downstream consumers can handle it, stamp a distinct failure
		// category, log the install hint once.
		if errors.Is(err, pdf.ErrPdftotextMissing) {
			rec.FailureCategory = string(failure.CatPDFToolingMissing)
			rec.Error = err.Error()
			pdfToolingHintOnce.Do(func() {
				log.Warn().
					Str("hint", pdf.InstallHint("pdftotext")).
					Msg("PDF encountered but pdftotext is not installed; raw bytes preserved in body")
			})
			return true
		}
		// Hard extraction failures (encrypted, damaged, empty). Record
		// the error but keep the raw bytes available for downstream
		// troubleshooting — same shape as the soft-fail path.
		rec.Error = "pdf: " + err.Error()
		rec.FailureCategory = string(failure.CatExtractionFailed)
		return true
	}

	// Success: swap in the extracted markdown and update metadata.
	best.Body = md
	best.ContentType = "text/markdown"
	rec.Metadata.ContentType = "text/markdown"
	rec.Metadata.BodyBytes = len(md)
	rec.Metadata.PDF = &output.PDFInfo{
		PageCount:     info.PageCount,
		Title:         info.Title,
		Author:        info.Author,
		CreatedAt:     info.CreatedAt,
		HasTextLayer:  info.HasTextLayer,
		UsedOCR:       info.UsedOCR,
		ExtractorTier: info.ExtractorTier,
		Pages:         toOutputPages(info.Pages),
	}
	return true
}

// toOutputPages copies the internal/pdf.Page slice into the
// output.PDFPage shape. Distinct types keep the output package free of
// an internal/pdf import.
func toOutputPages(pages []pdf.Page) []output.PDFPage {
	if len(pages) == 0 {
		return nil
	}
	out := make([]output.PDFPage, len(pages))
	for i, p := range pages {
		out[i] = output.PDFPage{Number: p.Number, Text: p.Text}
	}
	return out
}

func hashBody(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// parseActions combines inline --action flags and a --actions file into
// a single []chromedp.Action slice. Returns nil when no actions are
// configured. Inline actions run first, then file actions.
func parseActions(inline []string, filePath string) ([]chromedp.Action, error) {
	if len(inline) == 0 && filePath == "" {
		return nil, nil
	}

	var seq action.Sequence
	seq.Version = 1

	for _, spec := range inline {
		a, err := action.ParseInline(spec)
		if err != nil {
			return nil, fmt.Errorf("--action: %w", err)
		}
		seq.Actions = append(seq.Actions, a)
	}

	if filePath != "" {
		fileSeq, err := action.Load(filePath)
		if err != nil {
			return nil, fmt.Errorf("--actions: %w", err)
		}
		seq.Actions = append(seq.Actions, fileSeq.Actions...)
	}

	if err := seq.Validate(); err != nil {
		return nil, fmt.Errorf("actions: %w", err)
	}
	return seq.ChromedpActions()
}

// exitOnCtxDone returns an error if ctx is done, else nil. Used to make
// graceful shutdown explicit in command loops.
func exitOnCtxDone(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// stderrf is a tiny helper so command code can still write to stderr without
// pulling log formatting into every call site. Only for user-facing messages.
func stderrf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
}
