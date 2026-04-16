package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/jeffdhooton/trawl/internal/sitemap"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type mapOpts struct {
	sources        string
	depth          int
	sameDomain     bool
	limit          int
	timeout        time.Duration
	outputPath     string
	verbose        bool
	ignoreRobots   bool
	concurrency    int
	ratePerSec     float64
	sitemapMax     int
	politenessPath string
	proxy          proxyOpts
	evasion        evasionOpts
}

func newMapCmd() *cobra.Command {
	var opts mapOpts

	cmd := &cobra.Command{
		Use:   "map <url>",
		Short: "Enumerate every URL you can find on a site",
		Long: `Discover URLs from a site by combining two sources:

  1. Sitemap — robots.txt Sitemap: directives plus /sitemap.xml and
     /sitemap_index.xml, recursively expanded (same as "trawl sitemap").
  2. HTML crawl — BFS link discovery from the seed page using the HTTP
     engine. No chromium, no extraction, no record writes. If a site is
     SPA-heavy and you need nav links that only render after hydration,
     use "trawl crawl" instead.

Output is a plain URL list to stdout, one per line, composable with
trawl batch:

  trawl map https://example.com --depth 2 > urls.txt
  trawl batch urls.txt --selector 'title=h1'

URLs are deduped in first-seen order: sitemap URLs land first (they
are publisher-declared and authoritative), crawl-discovered URLs fill
the gaps.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMap(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.sources, "sources", "both",
		`URL sources to use: "sitemap", "crawl", or "both"`)
	cmd.Flags().IntVar(&opts.depth, "depth", 2,
		"BFS depth for the crawl source (seed is depth 0)")
	cmd.Flags().BoolVar(&opts.sameDomain, "same-domain", true,
		"restrict crawl-discovered URLs to the seed's host")
	cmd.Flags().IntVar(&opts.limit, "limit", 10000,
		"hard cap on URLs emitted to stdout. 0 = unlimited.")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 30*time.Second,
		"per-fetch timeout")
	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "-",
		`output file — "-" for stdout`)
	cmd.Flags().BoolVar(&opts.verbose, "verbose", false,
		"log discovery trace to stderr")
	cmd.Flags().BoolVar(&opts.ignoreRobots, "ignore-robots", false,
		"bypass robots.txt for the crawl source (logs a warning)")
	cmd.Flags().IntVarP(&opts.concurrency, "concurrency", "c", 8,
		"max concurrent HTTP fetches for the crawl source")
	cmd.Flags().Float64Var(&opts.ratePerSec, "rate", 2,
		"requests per second per domain for the crawl source")
	cmd.Flags().IntVar(&opts.sitemapMax, "sitemap-max", 50000,
		"cap total URLs pulled from sitemaps (0 = unlimited)")
	cmd.Flags().StringVar(&opts.politenessPath, "politeness", "",
		"YAML file with per-host rate/concurrency overrides (see docs/examples/politeness.yaml)")
	registerProxyFlags(cmd, &opts.proxy)
	registerEvasionFlags(cmd, &opts.evasion)

	return cmd
}

func runMap(parentCtx context.Context, seedURL string, opts mapOpts) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch opts.sources {
	case "sitemap", "crawl", "both":
	default:
		return fmt.Errorf(`invalid --sources %q (want "sitemap", "crawl", or "both")`, opts.sources)
	}
	if opts.depth < 0 {
		return fmt.Errorf("--depth must be >= 0")
	}
	if opts.limit < 0 {
		return fmt.Errorf("--limit must be >= 0")
	}

	sink, closeSink, err := openMapSink(opts.outputPath)
	if err != nil {
		return err
	}
	defer closeSink()

	canonSeed, err := canonical.Canonicalize(seedURL, canonical.Options{})
	if err != nil {
		return fmt.Errorf("canonicalize seed: %w", err)
	}

	// Shared emission buffer: every URL that makes it to stdout goes
	// through emit() so the --limit cap and dedup apply uniformly
	// across sources.
	seen := make(map[string]struct{})
	var seenMu sync.Mutex
	var emitted int
	emit := func(u string) bool {
		seenMu.Lock()
		defer seenMu.Unlock()
		if _, dup := seen[u]; dup {
			return true
		}
		if opts.limit > 0 && emitted >= opts.limit {
			return false
		}
		seen[u] = struct{}{}
		emitted++
		_, werr := fmt.Fprintln(sink, u)
		return werr == nil
	}

	// 1. Sitemap source (runs first so its URLs take first-seen
	//    priority in the dedup map).
	if opts.sources == "sitemap" || opts.sources == "both" {
		urls, trace, serr := sitemap.Discover(ctx, canonSeed, sitemap.Options{
			MaxURLs:  opts.sitemapMax,
			MaxDepth: 3,
			Timeout:  opts.timeout,
		})
		if serr != nil {
			if opts.sources == "sitemap" {
				return fmt.Errorf("sitemap: %w", serr)
			}
			log.Warn().Err(serr).Msg("sitemap discovery failed, continuing with crawl")
		}
		if opts.verbose {
			log.Info().
				Int("sitemap_urls", len(urls)).
				Int("sources_attempted", len(trace.Sources)).
				Bool("truncated", trace.Truncated).
				Msg("sitemap source complete")
		}
		for _, u := range urls {
			canon, cerr := canonical.Canonicalize(u, canonical.Options{})
			if cerr != nil {
				continue
			}
			if !emit(canon) {
				break // hit --limit
			}
		}
	}

	// 2. Crawl source. Runs after sitemap so any overlap is deduped
	//    against the sitemap URLs that already landed. The seed
	//    itself is emitted here so downstream pipelines always see it
	//    even when --sources=crawl alone is requested.
	if (opts.sources == "crawl" || opts.sources == "both") && (opts.limit == 0 || emitted < opts.limit) {
		if !emit(canonSeed) {
			// Hit limit on the seed itself — nothing more to do.
		} else {
			cerr := job.RunMap(ctx, canonSeed, job.MapOpts{
				Depth:          opts.depth,
				SameDomain:     opts.sameDomain,
				Timeout:        opts.timeout,
				IgnoreRobots:   opts.ignoreRobots,
				Concurrency:    opts.concurrency,
				RatePerSec:     opts.ratePerSec,
				PolitenessPath: opts.politenessPath,
				Proxy:          opts.proxy.toJob(),
				Evasion:        opts.evasion.toJob(),
			}, emit)
			if cerr != nil && opts.sources == "crawl" {
				return cerr
			}
			if cerr != nil {
				log.Warn().Err(cerr).Msg("crawl source finished with error")
			}
		}
	}

	if emitted == 0 {
		fmt.Fprintln(os.Stderr, "trawl map: no URLs discovered (try --verbose for details)")
	}
	if opts.verbose {
		log.Info().Int("emitted", emitted).Msg("map complete")
	}
	return nil
}

// openMapSink resolves --output to a writer. "-" (and "") go to
// stdout. Caller must invoke the returned close function.
func openMapSink(path string) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open output: %w", err)
	}
	return f, func() { _ = f.Close() }, nil
}
