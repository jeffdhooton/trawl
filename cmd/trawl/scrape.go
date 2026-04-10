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
	"github.com/jeffdhooton/trawl/internal/validity"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type scrapeOpts struct {
	selectors    []string
	outputPath   string
	ignoreRobots bool
	timeout      time.Duration
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

	cfg := engine.DefaultHTTPConfig()
	cfg.Timeout = opts.timeout
	httpEngine := engine.NewHTTP(cfg)
	defer httpEngine.Close()

	gateCfg := politeness.Default()
	gateCfg.UserAgent = cfg.UserAgent
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

	rec, err := fetchAndBuild(ctx, httpEngine, canonURL, rawURL, fields)
	if err != nil {
		return err
	}
	if err := sink.Write(rec); err != nil {
		return fmt.Errorf("write record: %w", err)
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

// fetchAndBuild runs one URL through the HTTP engine, validity check, and
// extractor, returning an output.Record ready to be written.
func fetchAndBuild(ctx context.Context, e engine.Engine, canonURL, origURL string, fields []extract.Field) (output.Record, error) {
	fetched, err := e.Fetch(ctx, engine.Request{URL: canonURL})
	if err != nil {
		return output.Record{
			URL:          origURL,
			CanonicalURL: canonURL,
			FetchedAt:    time.Now().UTC(),
			Tier:         e.Name(),
			Error:        err.Error(),
		}, err
	}

	checker := validity.NewChecker(validity.Default())
	vr := checker.Check(validity.Page{
		URL:         canonURL,
		StatusCode:  fetched.StatusCode,
		ContentType: fetched.ContentType,
		Body:        fetched.Body,
	})

	rec := output.Record{
		URL:          origURL,
		CanonicalURL: canonURL,
		FetchedAt:    time.Now().UTC(),
		Tier:         e.Name(),
		StatusCode:   fetched.StatusCode,
		DurationMS:   fetched.Duration.Milliseconds(),
		ContentHash:  hashBody(fetched.Body),
		Metadata: output.Metadata{
			ContentType: fetched.ContentType,
			BodyBytes:   len(fetched.Body),
			FinalURL:    fetched.FinalURL,
			Redirects:   fetched.Redirects,
		},
	}

	if !vr.Valid {
		rec.Error = vr.Reason
		return rec, nil
	}

	if len(fields) > 0 {
		ex, err := extract.CSS(fetched.Body, fields)
		if err != nil {
			rec.Error = "extract: " + err.Error()
			return rec, nil
		}
		rec.Extracted = ex
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
