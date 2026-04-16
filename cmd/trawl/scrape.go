package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/jeffdhooton/trawl/internal/output"

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

	if len(opts.csvColumns) > 0 && !output.IsCSVPath(opts.outputPath) {
		return fmt.Errorf("--csv-columns is only valid with a .csv or .tsv output path")
	}
	sink, err := output.NewFile(opts.outputPath, opts.csvColumns)
	if err != nil {
		return err
	}
	defer sink.Close()

	scrapeOpts := job.ScrapeOpts{
		Selectors:      opts.selectors,
		Output:         sink,
		IgnoreRobots:   opts.ignoreRobots,
		Timeout:        opts.timeout,
		Tiers:          opts.tiers,
		ForceTier:      opts.forceTier,
		NoTierLearning: opts.noTierLearning,
		TierCachePath:  opts.tierCachePath,
		Format:         opts.format,
		Readability:    opts.readability,
		NoMetadata:     opts.noMetadata,
		ScreenshotDir:  opts.screenshotDir,
		CacheEnabled:   opts.cacheEnabled,
		CacheTTL:       opts.cacheTTL,
		CachePath:      opts.cachePath,
		SchemaPath:     opts.schemaPath,
		Retries:        opts.retries,
		RetryDelay:     opts.retryDelay,
		PolitenessPath: opts.politenessPath,
		Proxy:          opts.proxy.toJob(),
		Evasion:        opts.evasion.toJob(),
		InlineActions:  opts.inlineActions,
		ActionsPath:    opts.actionsPath,
		PDF:            opts.pdf.toJob(),
	}

	if _, err := job.RunOne(ctx, rawURL, scrapeOpts); err != nil {
		return err
	}
	return nil
}

