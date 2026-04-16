package job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeffdhooton/trawl/internal/action"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/failure"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/jeffdhooton/trawl/internal/pdf"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/jeffdhooton/trawl/internal/schema"

	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog/log"
)

// ContentOpts bundles the content-extraction knobs that scrape/batch/
// crawl expose and thread through routeAndBuild. Kept as a struct so
// adding new content-phase features (e.g. --format xml) doesn't
// require touching every call site.
type ContentOpts struct {
	// Format is "", "html", "markdown", or "json". Empty means "don't
	// emit a body field" — preserves backward compatibility with
	// existing JSONL consumers that never asked for one.
	Format string
	// Readability runs boilerplate removal on the body BEFORE CSS
	// extraction and markdown conversion. Metadata extraction still
	// uses the original body because readability strips <head> content.
	Readability bool
	// NoMetadata skips automatic PageMetadata scraping. Escape hatch;
	// off by default since metadata extraction is cheap.
	NoMetadata bool
	// ScreenshotDir, when non-empty, asks the router to set
	// Request.WantScreenshot and writes any returned PNG to
	// <dir>/<sha256-of-canonical-url>.png. Only chromium produces
	// screenshots; HTTP-served records leave metadata.screenshot_path
	// empty.
	ScreenshotDir string
	// Schema, when non-nil, runs nested structured extraction against
	// the same contentBody as the flat CSS extractor. Results are
	// merged into Record.Extracted under the schema's field names;
	// schema keys win on collision with --selector flat keys.
	Schema *schema.Schema
	// Actions are pre-scrape chromedp actions (click, scroll, wait,
	// etc.) that run after page load before DOM capture. Only the
	// chromium engine executes them; HTTP ignores them.
	Actions []chromedp.Action
	// PDF controls the PDF engine's tier behavior for responses whose
	// Content-Type is application/pdf. Zero value means "Tier 1+2
	// only, no OCR" — the default when --ocr isn't set.
	PDF pdf.Opts
}

// ParseFieldSpecs parses the --selector strings into extract.Field
// values. Bubbles the underlying parser error so users see exactly
// which spec was malformed.
func ParseFieldSpecs(specs []string) ([]extract.Field, error) {
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

// ParseActions combines inline --action flags and a --actions file
// into a single []chromedp.Action slice. Returns nil when no actions
// are configured. Inline actions run first, then file actions.
func ParseActions(inline []string, filePath string) ([]chromedp.Action, error) {
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

// ValidateFormat returns an error if the --format value isn't one of
// the supported choices. Empty is valid and means "don't emit the
// body field."
func ValidateFormat(format string) error {
	switch format {
	case "", "html", "markdown", "json":
		return nil
	default:
		return fmt.Errorf("invalid --format %q (want html, markdown, json, or empty)", format)
	}
}

// routeAndBuild runs one URL through the tiered router and builds the
// output.Record. Returns a non-nil error only when every tier failed
// or the failure is non-escalatable (e.g. 404, unsupported content-
// type); in those cases the record is still populated with whatever
// evidence the last attempt captured, so callers can persist it.
func routeAndBuild(ctx context.Context, r *router.Router, canonURL, origURL string, fields []extract.Field, copts ContentOpts) (output.Record, error) {
	rec, _, err := routeAndBuildWithResult(ctx, r, canonURL, origURL, fields, copts)
	return rec, err
}

// routeAndBuildWithResult is routeAndBuild that additionally exposes
// the raw engine.Result used to populate the record. The crawl worker
// needs the body for link discovery; returning it here avoids a
// second fetch. The returned *engine.Result may be nil when every
// tier failed without producing any result at all (DNS failure, etc).
func routeAndBuildWithResult(ctx context.Context, r *router.Router, canonURL, origURL string, fields []extract.Field, copts ContentOpts) (output.Record, *engine.Result, error) {
	outcome, routeErr := r.Route(ctx, engine.Request{
		URL:            canonURL,
		WantScreenshot: copts.ScreenshotDir != "",
		Actions:        copts.Actions,
	})

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

	if sb := aggregateSoftBlock(outcome.Attempts); sb != nil {
		rec.Metadata.SoftBlock = sb
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
	// HTML-specific steps.
	if wasPDF := transformPDFIfNeeded(best, &rec, copts.PDF); wasPDF && rec.Error != "" {
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

	if best != nil && !copts.NoMetadata && isHTML(best.ContentType) {
		if pm := extract.Metadata(best.Body, rec.CanonicalURL); pm != nil {
			rec.Metadata.Page = pm
		}
	}

	contentBody := []byte{}
	if best != nil {
		contentBody = best.Body
		if copts.Readability && isHTML(best.ContentType) {
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

	if copts.Schema != nil && best != nil && isHTML(best.ContentType) {
		sx, err := schema.Extract(contentBody, rec.CanonicalURL, copts.Schema)
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

	if best != nil && copts.Format != "" {
		fromPDF := rec.Metadata.PDF != nil
		fromJSON := isJSON(best.ContentType)
		switch copts.Format {
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
				rec.Body = string(contentBody)
				rec.BodyFormat = "markdown"
				break
			}
			if fromJSON {
				rec.Body = string(contentBody)
				rec.BodyFormat = "json"
				break
			}
			md, err := extract.ToMarkdown(contentBody, rec.CanonicalURL)
			if err != nil {
				rec.Body = md
				rec.BodyFormat = "html"
			} else {
				rec.Body = md
				rec.BodyFormat = "markdown"
			}
		case "json":
			switch {
			case rec.Extracted != nil:
				j, _ := json.Marshal(rec.Extracted)
				rec.Body = string(j)
			case fromJSON:
				rec.Body = string(contentBody)
			default:
				rec.Body = "{}"
			}
			rec.BodyFormat = "json"
		}
	}

	if best != nil && len(best.Screenshot) > 0 && copts.ScreenshotDir != "" {
		if path, werr := writeScreenshot(copts.ScreenshotDir, rec.CanonicalURL, best.Screenshot); werr != nil {
			log.Warn().Str("url", rec.CanonicalURL).Err(werr).Msg("screenshot write failed")
		} else {
			rec.Metadata.ScreenshotPath = path
		}
	}

	if outcome.FromCache {
		rec.Metadata.FromCache = true
	}

	rec.FailureCategory = string(failure.Classify(routeErr, rec.StatusCode, rec.Error))
	return rec, best, nil
}

// stampEvasion combines the engine-side evasion info (BrowserLike,
// Stealth, UserAgent — populated by the HTTP/chromium engines on the
// Result) with the gate-side jitter delay (measured by Acquire) and
// writes both onto rec.Metadata.Evasion. When neither side reports
// any evasion, rec.Metadata.Evasion stays nil and the JSONL output
// is byte-identical to the pre-evasion default.
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

// pdfToolingHintOnce logs the "install poppler-utils" hint at most
// once per run so batch jobs don't spam N identical lines.
var pdfToolingHintOnce sync.Once

// pdfFormatOverrideOnce logs the "--format html on PDF emits markdown"
// warning once per run.
var pdfFormatOverrideOnce sync.Once

// transformPDFIfNeeded runs internal/pdf.Extract when the result's
// Content-Type indicates a PDF. Returns true when the transform
// (attempted or completed) should stop the HTML-gated pipeline from
// running — even on soft-fail where raw bytes are preserved, callers
// must not run HTML processing on PDF bytes.
func transformPDFIfNeeded(best *engine.Result, rec *output.Record, opts pdf.Opts) bool {
	if best == nil || !isPDF(best.ContentType) {
		return false
	}

	md, info, err := pdf.Extract(best.Body, opts)
	if err != nil {
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
		rec.Error = "pdf: " + err.Error()
		rec.FailureCategory = string(failure.CatExtractionFailed)
		return true
	}

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
// output.PDFPage shape. Distinct types keep the output package free
// of an internal/pdf import.
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

// writeScreenshot persists a PNG to <dir>/<sha256-of-url>.png. The
// filename is deterministic so re-running the same scrape overwrites
// the previous capture rather than accumulating dup files. Returns
// the absolute path on success.
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
		return path, nil
	}
	return abs, nil
}

func hashBody(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// aggregateSoftBlock walks every router attempt and, if any tier's
// validity check flagged an anti-bot challenge wall, builds the
// per-record SoftBlockInfo. The top-level Vendor/Marker come from the
// first hit (markers are ordered most-specific → most-generic in
// internal/validity, so CF challenges are never masked by a generic
// "access denied" string). Tiers lists every tier that walled, in
// attempt order — letting a consumer see patterns like "http walled,
// chromium succeeded" without re-parsing the router outcome.
func aggregateSoftBlock(attempts []router.Attempt) *output.SoftBlockInfo {
	var info *output.SoftBlockInfo
	for _, a := range attempts {
		if a.SoftBlock == nil {
			continue
		}
		if info == nil {
			info = &output.SoftBlockInfo{
				Detected: true,
				Vendor:   a.SoftBlock.Vendor,
				Marker:   a.SoftBlock.Marker,
			}
		}
		info.Tiers = append(info.Tiers, a.Tier)
	}
	return info
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

// isJSON reports whether a Content-Type header indicates a JSON body.
// When true, the body is already structured and we should not run
// HTML-oriented transforms on it. Matches application/json,
// application/ld+json, and any +json suffix.
func isJSON(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "application/json") ||
		strings.Contains(ct, "+json")
}
