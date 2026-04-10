package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type scrapeOpts struct {
	selectors    []string
	outputPath   string
	ignoreRobots bool
	timeout      time.Duration
	tiers        string
	forceTier    string
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

	rec, routeErr := routeAndBuild(ctx, r, canonURL, rawURL, fields)
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

// routeAndBuild runs one URL through the tiered router and builds the
// output.Record. It returns a non-nil error only when every tier failed or
// the failure is non-escalatable (e.g. 404, unsupported content-type); in
// those cases the record is still populated with whatever evidence the last
// attempt captured, so callers can persist it.
func routeAndBuild(ctx context.Context, r *router.Router, canonURL, origURL string, fields []extract.Field) (output.Record, error) {
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
		return rec, routeErr
	}

	if len(fields) > 0 && best != nil {
		ex, err := extract.CSS(best.Body, fields)
		if err != nil {
			rec.Error = "extract: " + err.Error()
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
	return rec, nil
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
