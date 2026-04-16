package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// EmitFunc is the callback RunMap invokes for each discovered URL.
// Returning false signals the caller has hit its emit cap (e.g.
// --limit on the CLI) and the crawl should stop. Implementations
// must be safe for concurrent use — RunMap calls them from multiple
// worker goroutines.
type EmitFunc func(string) bool

// MapOpts captures the knobs for RunMap. Mirrors the `trawl map`
// flag set but lives in this package so MCP/library callers don't
// need to depend on cmd/trawl. Sources are the caller's concern —
// RunMap only handles the HTML crawl source. Sitemap discovery is
// already a standalone call (sitemap.Discover).
type MapOpts struct {
	Depth        int
	SameDomain   bool
	Timeout      time.Duration
	IgnoreRobots bool
	Concurrency  int
	RatePerSec   float64

	// Optional politeness/proxy/evasion knobs — same semantics as
	// the other entry points.
	PolitenessPath string
	Proxy          ProxyOpts
	Evasion        EvasionOpts
}

// RunMap is the lightweight in-memory BFS used by `trawl map`'s
// crawl source. It is deliberately separate from Run: no routing
// ladder (HTTP tier only), no frontier DB, no extraction, no JSONL
// records. The only output is URL strings fed to emit().
//
// Termination: when every worker is blocked on an empty queue AND
// no worker is still processing a page, the crawl is quiescent and
// done. The emit callback returning false (limit hit) also drains
// the queue and ends.
//
// The caller is responsible for emitting the seed itself if desired —
// RunMap emits only discovered children. (This matches the existing
// `trawl map` behavior where the seed was emitted by the outer loop
// before runMapCrawl was invoked.)
func RunMap(ctx context.Context, seed string, opts MapOpts, emit EmitFunc) error {
	httpCfg := engine.DefaultHTTPConfig()
	if opts.Timeout > 0 {
		httpCfg.Timeout = opts.Timeout
	}

	gateCfg := politeness.Default()
	gateCfg.UserAgent = httpCfg.UserAgent
	gateCfg.IgnoreRobots = opts.IgnoreRobots
	if opts.RatePerSec > 0 {
		gateCfg.RatePerDomain = rate.Limit(opts.RatePerSec)
	}
	if opts.Concurrency > 0 {
		gateCfg.MaxConcurrentGlobal = opts.Concurrency
	}
	// chromiumCfg is unused (map runs HTTP-only) but ApplyEvasion
	// expects all three pointers; pass a throwaway value to reuse
	// the helper instead of duplicating its UA-strategy parsing.
	chromiumCfg := engine.DefaultChromiumConfig()
	if err := ApplyEvasion(&httpCfg, &gateCfg, &chromiumCfg, opts.Evasion); err != nil {
		return err
	}
	if _, err := ApplyProxy(&httpCfg, &chromiumCfg, opts.Proxy); err != nil {
		return err
	}
	// Map uses HTTP-only (no router), so proxy rotation is not wired
	// here — the rotator only fires inside router.Route().
	LogEvasion(opts.Evasion)
	LogProxy(opts.Proxy)

	eng := engine.NewHTTP(httpCfg)
	defer eng.Close()

	gate := politeness.NewGate(gateCfg, nil)
	if opts.PolitenessPath != "" {
		hr, err := politeness.LoadHostRules(opts.PolitenessPath)
		if err != nil {
			return fmt.Errorf("load politeness rules: %w", err)
		}
		gate.WithHostRules(hr)
		log.Info().
			Str("politeness", opts.PolitenessPath).
			Int("rules", len(hr.Hosts)).
			Msg("per-host politeness rules loaded for map crawl")
	}

	if opts.IgnoreRobots {
		log.Warn().Msg("robots.txt is being ignored for the map crawl source")
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

	// Seed the queue with the seed URL itself so the first worker can
	// fetch it. The seed is NOT emitted here — the caller already did
	// (or chose not to).
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

	concurrency := opts.Concurrency
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
				// which exceeds opts.Depth. Skip the fetch entirely so
				// leaf pages don't cost an HTTP round-trip just to
				// have their links thrown away.
				if next.depth >= opts.Depth {
					mu.Lock()
					inFlight--
					cond.Broadcast()
					mu.Unlock()
					continue
				}

				links, fetchErr := fetchLinksForMap(ctx, eng, gate, next.url, opts.SameDomain)
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

// fetchLinksForMap runs one HTTP fetch through the politeness gate
// and returns the filtered link list. Independent of the tiered
// router so map stays fast and single-tier.
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

// CanonicalizeSeed runs canonical.Canonicalize on a seed URL. Exposed
// here so cmd/trawl's map command can reuse it without importing the
// canonical package directly.
func CanonicalizeSeed(seed string) (string, error) {
	return canonical.Canonicalize(seed, canonical.Options{})
}
