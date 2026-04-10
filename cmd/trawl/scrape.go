package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/failure"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/jeffdhooton/trawl/internal/router"
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
		`body format in the output record: "html" or "markdown". Empty omits the body field.`)
	cmd.Flags().BoolVar(&opts.readability, "readability", false,
		"strip nav/footer/ads boilerplate before CSS extraction and markdown conversion")
	cmd.Flags().BoolVar(&opts.noMetadata, "no-metadata", false,
		"skip automatic page metadata extraction (title, OG, canonical, JSON-LD)")

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

	sink, err := output.NewJSONLFile(opts.outputPath)
	if err != nil {
		return err
	}
	defer sink.Close()

	httpCfg := engine.DefaultHTTPConfig()
	httpCfg.Timeout = opts.timeout
	tiers := parseTierList(opts.tiers)
	if len(tiers) == 0 && opts.forceTier == "" {
		tiers = []string{"http", "chromium"}
	}
	r, err := buildRouter(tiers, opts.forceTier, httpCfg)
	if err != nil {
		return err
	}
	defer r.Close()

	tierCache, _, _ := openTierCache(opts.noTierLearning, opts.tierCachePath)
	defer tierCache.Close()
	r.WithCache(tierCache)

	gateCfg := politeness.Default()
	gateCfg.UserAgent = httpCfg.UserAgent
	gateCfg.IgnoreRobots = opts.ignoreRobots
	gate := politeness.NewGate(gateCfg, nil)

	if opts.ignoreRobots {
		log.Warn().Str("url", canonURL).Msg("robots.txt is being ignored")
	}

	allowed, err := gate.Allowed(ctx, canonURL)
	if err != nil {
		return fmt.Errorf("check robots: %w", err)
	}
	if !allowed {
		return fmt.Errorf("blocked by robots.txt: %s", canonURL)
	}

	release, err := gate.Acquire(ctx, canonURL)
	if err != nil {
		return fmt.Errorf("politeness: %w", err)
	}
	defer release()

	copts := contentOpts{
		format:      opts.format,
		readability: opts.readability,
		noMetadata:  opts.noMetadata,
	}
	if err := validateFormat(copts.format); err != nil {
		return err
	}

	rec, routeErr := routeAndBuild(ctx, r, canonURL, rawURL, fields, copts)
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
	// format is "", "html", or "markdown". Empty means "don't emit a
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
}

// routeAndBuild runs one URL through the tiered router and builds the
// output.Record. It returns a non-nil error only when every tier failed or
// the failure is non-escalatable (e.g. 404, unsupported content-type); in
// those cases the record is still populated with whatever evidence the last
// attempt captured, so callers can persist it.
func routeAndBuild(ctx context.Context, r *router.Router, canonURL, origURL string, fields []extract.Field, copts contentOpts) (output.Record, error) {
	outcome, routeErr := r.Route(ctx, engine.Request{URL: canonURL})

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
		return rec, routeErr
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
			return rec, nil
		}
		rec.Metadata.Extraction = &output.ExtractionStats{
			Fields: len(fields),
			Hits:   len(ex),
		}
		if len(ex) > 0 {
			rec.Extracted = ex
		}
	}

	// Populate the Body field if --format was set. Empty format means
	// existing JSONL consumers stay backward-compatible (no body in the
	// record). html passes through verbatim; markdown runs the converter.
	if best != nil && copts.format != "" {
		switch copts.format {
		case "html":
			rec.Body = string(contentBody)
			rec.BodyFormat = "html"
		case "markdown":
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
		}
	}

	// Final classification — uses the router error (if any), the final
	// status code, and the formatted reason field together.
	rec.FailureCategory = string(failure.Classify(routeErr, rec.StatusCode, rec.Error))
	return rec, nil
}

// validateFormat returns an error if the --format value isn't one of the
// supported choices. Empty is valid and means "don't emit the body field."
func validateFormat(format string) error {
	switch format {
	case "", "html", "markdown":
		return nil
	default:
		return fmt.Errorf("invalid --format %q (want html, markdown, or empty)", format)
	}
}

// isHTML returns true if the Content-Type header smells like HTML.
// Metadata, readability, and markdown conversion only make sense on
// HTML bodies; applying them to application/json would be nonsensical.
func isHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return ct == "" || strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

func hashBody(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
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
