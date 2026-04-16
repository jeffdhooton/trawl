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
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type crawlOpts struct {
	// content / routing — mirror batchOpts so the same JSONL shape comes out.
	selectors      []string
	outputPath     string
	ignoreRobots   bool
	timeout        time.Duration
	concurrency    int
	ratePerSec     float64
	jobID          string
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

	// Crawl-specific knobs.
	depth      int
	sameDomain bool
	limit      int
}

func newCrawlCmd() *cobra.Command {
	var opts crawlOpts

	cmd := &cobra.Command{
		Use:   "crawl <seed-url>",
		Short: "BFS-crawl a site starting from a seed URL",
		Long: `Walk a site breadth-first from <seed-url>, routing each discovered
page through the same tiered pipeline as scrape/batch. Link discovery
runs on the body of every successful fetch; failed fetches are recorded
but not traversed.

The --limit flag caps the number of URLs ENQUEUED (seed + children),
not the number of successful fetches. The default is 1000; pass 0 to
disable. The --depth flag caps BFS depth (seed is depth 0).

Example:
  trawl crawl https://example.com \
    --depth 2 --same-domain --limit 500 \
    --format markdown --readability -o site.jsonl
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCrawl(cmd.Context(), args[0], opts)
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
	cmd.Flags().IntVarP(&opts.concurrency, "concurrency", "c", 8,
		"max concurrent in-flight requests across all domains")
	cmd.Flags().Float64Var(&opts.ratePerSec, "rate", 1,
		"requests per second per domain")
	cmd.Flags().StringVar(&opts.jobID, "job-id", "",
		"reuse/create a specific job ID (default: auto-generated)")
	cmd.Flags().StringVar(&opts.tiers, "tiers", "http,chromium",
		"comma-separated engine tiers to try in order (http, chromium)")
	cmd.Flags().StringVar(&opts.forceTier, "force-tier", "",
		"pin a single tier for this run (overrides --tiers)")
	cmd.Flags().BoolVar(&opts.noTierLearning, "no-tier-learning", false,
		"disable the cross-job host→tier cache")
	cmd.Flags().StringVar(&opts.tierCachePath, "tier-cache-path", "",
		"override the default tier-cache directory ($TRAWL_HOME/tier-cache)")
	cmd.Flags().StringVar(&opts.format, "format", "",
		`body format in output records: "html", "markdown", or "json". Empty omits the body field.`)
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
			"Only valid when -o ends in .csv or .tsv.")
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

	cmd.Flags().IntVar(&opts.depth, "depth", 2,
		"maximum BFS depth relative to the seed (seed is depth 0)")
	cmd.Flags().BoolVar(&opts.sameDomain, "same-domain", true,
		"only follow links on the same host (or sibling subdomains) as the seed")
	cmd.Flags().IntVar(&opts.limit, "limit", 1000,
		"hard cap on URLs enqueued (seed + children). 0 = unlimited.")

	return cmd
}

func runCrawl(parentCtx context.Context, seedURL string, opts crawlOpts) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if _, err := job.ParseFieldSpecs(opts.selectors); err != nil {
		return err
	}
	if err := job.ValidateFormat(opts.format); err != nil {
		return err
	}
	if err := opts.pdf.toJob().Validate(); err != nil {
		return err
	}
	opts.pdf.toJob().Log()
	if opts.depth < 0 {
		return fmt.Errorf("--depth must be >= 0")
	}
	if opts.limit < 0 {
		return fmt.Errorf("--limit must be >= 0")
	}

	id := opts.jobID
	if id == "" {
		id = job.NewID()
	}
	dir, err := job.DirFor(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir job dir: %w", err)
	}

	cfg := &job.Config{
		ID:              id,
		CreatedAt:       time.Now().UTC(),
		Selectors:       opts.selectors,
		OutputPath:      resolveOutputPath(opts.outputPath, dir),
		Concurrency:     opts.concurrency,
		IgnoreRobots:    opts.ignoreRobots,
		RatePerSec:      opts.ratePerSec,
		Timeout:         opts.timeout.String(),
		Tiers:           opts.tiers,
		ForceTier:       opts.forceTier,
		NoTierLearning:  opts.noTierLearning,
		TierCachePath:   opts.tierCachePath,
		Format:          opts.format,
		Readability:     opts.readability,
		NoMetadata:      opts.noMetadata,
		ScreenshotDir:   opts.screenshotDir,
		CacheEnabled:    opts.cacheEnabled,
		CacheTTL:        opts.cacheTTL.String(),
		CachePath:       opts.cachePath,
		SchemaPath:      opts.schemaPath,
		CSVColumns:      opts.csvColumns,
		Retries:         opts.retries,
		RetryDelay:      opts.retryDelay.String(),
		PolitenessPath:  opts.politenessPath,
		ProxyURL:          opts.proxy.proxyURL,
		ProxyFile:         opts.proxy.proxyFile,
		RotateOnStatus:    opts.proxy.rotateOnStatus,
		RotateRetries:     opts.proxy.rotateRetries,
		BrowserLike:       opts.evasion.browserLike,
		UserAgentStrategy: opts.evasion.userAgentStrategy,
		Stealth:           opts.evasion.stealth,
		NoJitter:          opts.evasion.noJitter,
		TLSMatch:          opts.evasion.tlsMatch,
		InlineActions:     opts.inlineActions,
		ActionsPath:       opts.actionsPath,
		OCR:               opts.pdf.ocr,
		OCRLang:           opts.pdf.ocrLang,
		PDFMaxPages:       opts.pdf.pdfMaxPages,
		CrawlMode:       true,
		CrawlMaxDepth:   opts.depth,
		CrawlSameDomain: opts.sameDomain,
		CrawlLimit:      opts.limit,
		CrawlSeed:       seedURL,
	}
	if err := cfg.Save(dir); err != nil {
		return err
	}

	log.Info().
		Str("job_id", id).
		Str("job_dir", dir).
		Str("seed", seedURL).
		Int("depth", opts.depth).
		Int("limit", opts.limit).
		Bool("same_domain", opts.sameDomain).
		Msg("starting crawl job")

	if err := enqueueSeed(dir, seedURL); err != nil {
		return err
	}

	return job.Run(ctx, dir, cfg)
}

// enqueueSeed adds the crawl seed at depth 0. It is idempotent — on
// resume the seed will already exist and the Enqueue call returns
// added=false, which we silently tolerate.
func enqueueSeed(jobDir, seedURL string) error {
	f, err := frontier.Open(filepath.Join(jobDir, "frontier"))
	if err != nil {
		return fmt.Errorf("open frontier: %w", err)
	}
	defer f.Close()

	_, added, err := f.EnqueueWithDepth(seedURL, 0)
	if err != nil {
		return fmt.Errorf("enqueue seed: %w", err)
	}
	if added {
		log.Info().Str("seed", seedURL).Msg("seeded crawl frontier")
	} else {
		log.Info().Str("seed", seedURL).Msg("seed already in frontier (resume)")
	}
	return nil
}
