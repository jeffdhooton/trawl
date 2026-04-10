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
	selectors    []string
	outputPath   string
	ignoreRobots bool
	timeout      time.Duration
	concurrency  int
	ratePerSec   float64
	jobID        string
	tiers        string
	forceTier    string
	urlColumn    string
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
		`for CSV/TSV input: column name or index holding the URL (default: "url" or first column)`)

	return cmd
}

func runBatch(parentCtx context.Context, urlFile string, opts batchOpts) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Validate selectors early.
	if _, err := parseFieldSpecs(opts.selectors); err != nil {
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
		ID:           id,
		CreatedAt:    time.Now().UTC(),
		Selectors:    opts.selectors,
		OutputPath:   resolveOutputPath(opts.outputPath, dir),
		Concurrency:  opts.concurrency,
		IgnoreRobots: opts.ignoreRobots,
		RatePerSec:   opts.ratePerSec,
		Timeout:      opts.timeout.String(),
		Tiers:        opts.tiers,
		ForceTier:    opts.forceTier,
		URLColumn:    opts.urlColumn,
	}
	if err := cfg.save(dir); err != nil {
		return err
	}

	log.Info().
		Str("job_id", id).
		Str("job_dir", dir).
		Str("output", cfg.OutputPath).
		Msg("starting batch job")

	if err := enqueueFromFile(dir, urlFile, opts.urlColumn); err != nil {
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

func enqueueFromFile(jobDir, urlFile, urlColumn string) error {
	urls, err := readURLList(urlFile, urlColumn)
	if err != nil {
		return err
	}

	f, err := frontier.Open(filepath.Join(jobDir, "frontier"))
	if err != nil {
		return fmt.Errorf("open frontier: %w", err)
	}
	defer f.Close()

	added, dupes, skipped := 0, 0, 0
	for i, raw := range urls {
		_, wasAdded, err := f.Enqueue(raw)
		if err != nil {
			log.Warn().Int("row", i+1).Str("url", raw).Err(err).Msg("skipping invalid url")
			skipped++
			continue
		}
		if wasAdded {
			added++
		} else {
			dupes++
		}
	}
	log.Info().
		Int("added", added).
		Int("duplicates", dupes).
		Int("skipped", skipped).
		Msg("enqueued urls")
	return nil
}
