package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/jeffdhooton/trawl/internal/sitemap"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
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

	// Canonicalize the seed once so all same-host comparisons downstream
	// use the same form. A bad seed fails loudly here rather than silently
	// producing zero URLs.
	canonSeed, err := canonical.Canonicalize(seedURL, canonical.Options{})
	if err != nil {
		return fmt.Errorf("canonicalize seed: %w", err)
	}

	// Shared emission buffer: every URL that makes it to stdout goes
	// through emit() so the --limit cap and dedup apply uniformly across
	// sources.
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

	// 1. Sitemap source (runs first so its URLs take first-seen priority
	//    in the dedup map).
	if opts.sources == "sitemap" || opts.sources == "both" {
		urls, trace, serr := sitemap.Discover(ctx, canonSeed, sitemap.Options{
			MaxURLs:  opts.sitemapMax,
			MaxDepth: 3,
			Timeout:  opts.timeout,
		})
		if serr != nil {
			// Sitemap failure is non-fatal when both sources are requested —
			// the crawl source may still find URLs. Log and continue.
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

	// 2. Crawl source. Runs after sitemap so any overlap is deduped against
	//    the sitemap URLs that already landed.
	if (opts.sources == "crawl" || opts.sources == "both") && (opts.limit == 0 || emitted < opts.limit) {
		cerr := runMapCrawl(ctx, canonSeed, opts, emit)
		if cerr != nil && opts.sources == "crawl" {
			return cerr
		}
		if cerr != nil {
			log.Warn().Err(cerr).Msg("crawl source finished with error")
		}
	}

	// Flush + zero-URL warning.
	if emitted == 0 {
		fmt.Fprintln(os.Stderr, "trawl map: no URLs discovered (try --verbose for details)")
	}
	if opts.verbose {
		log.Info().Int("emitted", emitted).Msg("map complete")
	}
	return nil
}

// openMapSink resolves --output to a writer. "-" (and "") go to stdout.
// Caller must invoke the returned close function.
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

// runMapCrawl is the lightweight in-memory BFS used by `trawl map`. It is
// deliberately separate from the `trawl crawl` pipeline: no routing ladder
// (HTTP tier only), no frontier DB, no extraction, no JSONL records. The
// only output is URL strings fed to emit().
//
// Termination: when every worker is blocked on an empty queue AND no worker
// is still processing a page, the crawl is quiescent and done. The emit
// callback returning false (limit hit) also drains the queue and ends.
func runMapCrawl(ctx context.Context, seed string, opts mapOpts, emit func(string) bool) error {
	httpCfg := engine.DefaultHTTPConfig()
	httpCfg.Timeout = opts.timeout

	gateCfg := politeness.Default()
	gateCfg.UserAgent = httpCfg.UserAgent
	gateCfg.IgnoreRobots = opts.ignoreRobots
	if opts.ratePerSec > 0 {
		gateCfg.RatePerDomain = rate.Limit(opts.ratePerSec)
	}
	if opts.concurrency > 0 {
		gateCfg.MaxConcurrentGlobal = opts.concurrency
	}
	// chromiumCfg is unused here (map runs HTTP-only) but applyEvasion
	// expects all three pointers; we pass a throwaway value so we can
	// reuse the helper instead of duplicating its UA-strategy parsing.
	chromiumCfg := engine.DefaultChromiumConfig()
	if err := applyEvasion(&httpCfg, &gateCfg, &chromiumCfg, opts.evasion); err != nil {
		return err
	}
	if _, err := applyProxy(&httpCfg, &chromiumCfg, opts.proxy); err != nil {
		return err
	}
	// Map uses HTTP-only (no router), so proxy rotation is not wired here.
	logEvasion(opts.evasion)
	logProxy(opts.proxy)

	eng := engine.NewHTTP(httpCfg)
	defer eng.Close()

	gate := politeness.NewGate(gateCfg, nil)
	if opts.politenessPath != "" {
		hr, err := politeness.LoadHostRules(opts.politenessPath)
		if err != nil {
			return fmt.Errorf("load politeness rules: %w", err)
		}
		gate.WithHostRules(hr)
		log.Info().
			Str("politeness", opts.politenessPath).
			Int("rules", len(hr.Hosts)).
			Msg("per-host politeness rules loaded for map crawl")
	}

	if opts.ignoreRobots {
		log.Warn().Msg("robots.txt is being ignored for the map crawl source")
	}

	// Emit the seed itself so downstream pipelines always see it even
	// when depth=0 prunes all discovery.
	if !emit(seed) {
		return nil
	}

	type item struct {
		url   string
		depth int
	}
	var (
		mu       sync.Mutex
		queue    []item
		visited  = make(map[string]struct{})
		inFlight int
		stopped  bool
	)
	cond := sync.NewCond(&mu)
	push := func(u string, d int) {
		if _, dup := visited[u]; dup {
			return
		}
		visited[u] = struct{}{}
		queue = append(queue, item{url: u, depth: d})
		cond.Signal()
	}

	// Seed the queue.
	mu.Lock()
	push(seed, 0)
	mu.Unlock()

	// ctx-cancel watchdog: wake everyone so blocked workers exit.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			stopped = true
			cond.Broadcast()
			mu.Unlock()
		case <-done:
		}
	}()

	concurrency := opts.concurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(queue) == 0 && inFlight > 0 && !stopped {
					cond.Wait()
				}
				if stopped || (len(queue) == 0 && inFlight == 0) {
					// Wake any sibling that is still in Wait so they can
					// also see quiescence and exit.
					cond.Broadcast()
					mu.Unlock()
					return
				}
				next := queue[0]
				queue = queue[1:]
				inFlight++
				mu.Unlock()

				// Depth cap: if this node is already AT the configured
				// depth, any children we discover would be at depth+1
				// which exceeds opts.depth. Skip the fetch entirely so
				// leaf pages don't cost an HTTP round-trip just to have
				// their links thrown away.
				if next.depth >= opts.depth {
					mu.Lock()
					inFlight--
					cond.Broadcast()
					mu.Unlock()
					continue
				}

				links, fetchErr := fetchLinksForMap(ctx, eng, gate, next.url, opts.sameDomain)
				if fetchErr != nil {
					log.Debug().Str("url", next.url).Err(fetchErr).Msg("map: fetch failed")
				}

				mu.Lock()
				childDepth := next.depth + 1
				for _, link := range links {
					if _, dup := visited[link]; dup {
						continue
					}
					if !emit(link) {
						stopped = true
						cond.Broadcast()
						break
					}
					push(link, childDepth)
				}
				inFlight--
				cond.Broadcast()
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// fetchLinksForMap runs one HTTP fetch through the politeness gate and
// returns the filtered link list. It is deliberately independent of the
// tiered router so map stays fast and single-tier. extract.AllLinks
// already resolves same-domain against the base URL, so no separate
// seedHost argument is needed.
func fetchLinksForMap(
	ctx context.Context,
	eng *engine.HTTP,
	gate *politeness.Gate,
	targetURL string,
	sameDomain bool,
) ([]string, error) {
	allowed, err := gate.Allowed(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("robots: %w", err)
	}
	if !allowed {
		return nil, errors.New("blocked by robots.txt")
	}
	release, _, err := gate.Acquire(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("politeness: %w", err)
	}
	defer release()

	res, err := eng.Fetch(ctx, engine.Request{URL: targetURL})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	if !isHTML(res.ContentType) {
		return nil, nil
	}

	base := res.FinalURL
	if base == "" {
		base = targetURL
	}
	links, err := extract.AllLinks(res.Body, base, extract.LinkOptions{SameDomain: sameDomain})
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	// Canonicalize each link before returning so dedup upstream sees the
	// same form regardless of tracking params, default ports, etc.
	out := make([]string, 0, len(links))
	for _, l := range links {
		c, cerr := canonical.Canonicalize(l, canonical.Options{})
		if cerr != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
