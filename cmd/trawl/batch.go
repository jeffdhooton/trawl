package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jeffdhooton/trawl/internal/frontier"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type batchOpts struct {
	selectors        []string
	outputPath       string
	ignoreRobots     bool
	timeout          time.Duration
	concurrency      int
	ratePerSec       float64
	jobID            string
	tiers            string
	forceTier        string
	urlColumn        string
	fallbackColumn   string
	fallbackSelector string
	noTierLearning   bool
	tierCachePath    string
	format           string
	readability      bool
	noMetadata       bool
	screenshotDir    string
	cacheEnabled     bool
	cacheTTL         time.Duration
	cachePath        string
	schemaPath       string
}

func newBatchCmd() *cobra.Command {
	var opts batchOpts

	cmd := &cobra.Command{
		Use:   "batch <url-file>",
		Short: "Scrape a list of URLs from a file",
		Long: `Read URLs (one per line) from a file, enqueue them in a persistent
frontier, and drain them concurrently through the HTTP tier. Results are
written as JSONL. The job is resumable: SIGINT/SIGTERM shuts workers down
gracefully and prints a resume command.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBatch(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().StringArrayVarP(&opts.selectors, "selector", "s", nil,
		`extraction rule: "name=selector" (repeatable)`)
	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "results.jsonl",
		"JSONL output file")
	cmd.Flags().BoolVar(&opts.ignoreRobots, "ignore-robots", false,
		"bypass robots.txt (logs a warning)")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 30*time.Second,
		"HTTP request timeout per URL")
	cmd.Flags().IntVarP(&opts.concurrency, "concurrency", "c", 20,
		"max concurrent in-flight requests across all domains")
	cmd.Flags().Float64Var(&opts.ratePerSec, "rate", 1,
		"requests per second per domain")
	cmd.Flags().StringVar(&opts.jobID, "job-id", "",
		"reuse/create a specific job ID (default: auto-generated)")
	cmd.Flags().StringVar(&opts.tiers, "tiers", "http,chromium",
		"comma-separated engine tiers to try in order (http, chromium)")
	cmd.Flags().StringVar(&opts.forceTier, "force-tier", "",
		"pin a single tier for this run (overrides --tiers)")
	cmd.Flags().StringVar(&opts.urlColumn, "url-column", "",
		`for CSV/TSV input: column name holding the primary URL (default: "url" or first column)`)
	cmd.Flags().StringVar(&opts.fallbackColumn, "fallback-column", "",
		`for CSV/TSV input: column name holding a fallback URL to try when the primary`+
			` returns http_4xx or dns_failure. Requires --fallback-selector.`)
	cmd.Flags().StringVar(&opts.fallbackSelector, "fallback-selector", "",
		`CSS selector for an <a> to follow from the fallback URL (e.g. 'a[href*="pricing"]').`+
			` Only applied when the primary fetch failed with a trigger category AND the seed`+
			` row had a fallback URL. Requires --fallback-column.`)
	cmd.Flags().BoolVar(&opts.noTierLearning, "no-tier-learning", false,
		"disable the cross-job host→tier cache (each URL starts at the cheapest tier)")
	cmd.Flags().StringVar(&opts.tierCachePath, "tier-cache-path", "",
		"override the default tier-cache directory ($TRAWL_HOME/tier-cache)")
	cmd.Flags().StringVar(&opts.format, "format", "",
		`body format in output records: "html" or "markdown". Empty omits the body field.`)
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

	return cmd
}

func runBatch(parentCtx context.Context, urlFile string, opts batchOpts) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Validate selectors early.
	if _, err := parseFieldSpecs(opts.selectors); err != nil {
		return err
	}

	// Hybrid discovery flags are only meaningful together.
	if (opts.fallbackColumn == "") != (opts.fallbackSelector == "") {
		return fmt.Errorf("--fallback-column and --fallback-selector must be used together")
	}

	if err := validateFormat(opts.format); err != nil {
		return err
	}

	id := opts.jobID
	if id == "" {
		id = newJobID()
	}
	dir, err := jobDirFor(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir job dir: %w", err)
	}

	cfg := &JobConfig{
		ID:               id,
		CreatedAt:        time.Now().UTC(),
		Selectors:        opts.selectors,
		OutputPath:       resolveOutputPath(opts.outputPath, dir),
		Concurrency:      opts.concurrency,
		IgnoreRobots:     opts.ignoreRobots,
		RatePerSec:       opts.ratePerSec,
		Timeout:          opts.timeout.String(),
		Tiers:            opts.tiers,
		ForceTier:        opts.forceTier,
		URLColumn:        opts.urlColumn,
		FallbackColumn:   opts.fallbackColumn,
		FallbackSelector: opts.fallbackSelector,
		NoTierLearning:   opts.noTierLearning,
		TierCachePath:    opts.tierCachePath,
		Format:           opts.format,
		Readability:      opts.readability,
		NoMetadata:       opts.noMetadata,
		ScreenshotDir:    opts.screenshotDir,
		CacheEnabled:     opts.cacheEnabled,
		CacheTTL:         opts.cacheTTL.String(),
		CachePath:        opts.cachePath,
		SchemaPath:       opts.schemaPath,
	}
	if err := cfg.save(dir); err != nil {
		return err
	}

	log.Info().
		Str("job_id", id).
		Str("job_dir", dir).
		Str("output", cfg.OutputPath).
		Msg("starting batch job")

	if err := enqueueFromFile(dir, urlFile, opts.urlColumn, opts.fallbackColumn); err != nil {
		return err
	}

	return runJob(ctx, dir, cfg)
}

// resolveOutputPath interprets the --output flag:
//
//   - "-"              → stdout
//   - absolute path    → used as-is
//   - relative path    → resolved relative to the CWD at invocation time
//     (NOT the job dir — users expect `-o results.jsonl` to land in ".")
func resolveOutputPath(path, _ string) string {
	if path == "-" || path == "" {
		return "-"
	}
	if filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func enqueueFromFile(jobDir, urlFile, urlColumn, fallbackColumn string) error {
	rows, err := readURLList(urlFile, urlColumn, fallbackColumn)
	if err != nil {
		return err
	}

	f, err := frontier.Open(filepath.Join(jobDir, "frontier"))
	if err != nil {
		return fmt.Errorf("open frontier: %w", err)
	}
	defer f.Close()

	added, dupes, skipped, withFallback := 0, 0, 0, 0
	for i, row := range rows {
		_, wasAdded, err := f.EnqueueWithFallback(row.URL, row.Fallback)
		if err != nil {
			log.Warn().Int("row", i+1).Str("url", row.URL).Err(err).Msg("skipping invalid url")
			skipped++
			continue
		}
		if wasAdded {
			added++
			if row.Fallback != "" {
				withFallback++
			}
		} else {
			dupes++
		}
	}
	log.Info().
		Int("added", added).
		Int("duplicates", dupes).
		Int("skipped", skipped).
		Int("with_fallback", withFallback).
		Msg("enqueued urls")
	return nil
}
