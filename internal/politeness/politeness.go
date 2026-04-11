// Package politeness enforces robots.txt, per-domain rate limits, and
// concurrency caps. Politeness is on by default; opting out requires
// explicit configuration.
package politeness

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
	"golang.org/x/time/rate"
)

// Config controls the politeness layer.
type Config struct {
	// UserAgent is used for robots.txt matching and for the robots fetch request.
	UserAgent string
	// RatePerDomain is the steady-state requests-per-second allowed per host.
	RatePerDomain rate.Limit
	// BurstPerDomain is the token-bucket burst size per host.
	BurstPerDomain int
	// MaxConcurrentPerDomain caps in-flight requests to a single host.
	MaxConcurrentPerDomain int
	// MaxConcurrentGlobal caps in-flight requests across all hosts.
	MaxConcurrentGlobal int
	// IgnoreRobots bypasses robots.txt entirely (logs a warning elsewhere).
	IgnoreRobots bool
	// RobotsTimeout caps how long we wait when fetching robots.txt.
	RobotsTimeout time.Duration
	// JitterFraction is the ±fraction of the rate-limiter's base
	// interval to add as a uniformly-random sleep after the limiter
	// grants a token. 0.2 means each Acquire returns somewhere in
	// [0, 1.2 * baseInterval] of extra delay (negative jitter is
	// clipped to zero — the limiter already enforces the minimum).
	// Zero disables jitter entirely; the Acquire fast-path skips the
	// math and reports JitterMS == 0.
	JitterFraction float64
}

// Default returns sane polite defaults per SPEC §3.5.
func Default() Config {
	return Config{
		UserAgent:              "trawl",
		RatePerDomain:          rate.Limit(1),
		BurstPerDomain:         1,
		MaxConcurrentPerDomain: 4,
		MaxConcurrentGlobal:    200,
		RobotsTimeout:          10 * time.Second,
	}
}

// Gate is the runtime enforcement object. One Gate per job.
type Gate struct {
	cfg        Config
	httpClient *http.Client

	mu        sync.Mutex
	byDomain  map[string]*domainState
	globalSem chan struct{}
	// hostRules is an optional per-host override set loaded from
	// --politeness. nil means "no overrides, cfg values apply to all
	// hosts." Consulted inside stateFor when building a new
	// domainState; changing it after workers are running will only
	// affect hosts seen for the first time AFTER the change.
	hostRules *HostRules
}

type domainState struct {
	limiter *rate.Limiter
	sem     chan struct{}
	robots  *robotstxt.Group
	loaded  bool
	// jitterFrac is the ±fraction of base interval to sleep after the
	// limiter grants a token. Resolved from the Gate's config plus any
	// per-host override at the moment the domainState is built. Zero
	// means "no jitter on this host."
	jitterFrac float64
}

// NewGate builds a Gate. The httpClient is used only for fetching robots.txt;
// callers should pass the same client the HTTP engine uses, or nil for default.
func NewGate(cfg Config, httpClient *http.Client) *Gate {
	if cfg.RatePerDomain == 0 {
		cfg.RatePerDomain = rate.Limit(1)
	}
	if cfg.BurstPerDomain == 0 {
		cfg.BurstPerDomain = 1
	}
	if cfg.MaxConcurrentPerDomain == 0 {
		cfg.MaxConcurrentPerDomain = 4
	}
	if cfg.MaxConcurrentGlobal == 0 {
		cfg.MaxConcurrentGlobal = 200
	}
	if cfg.RobotsTimeout == 0 {
		cfg.RobotsTimeout = 10 * time.Second
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "trawl"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.RobotsTimeout}
	}
	return &Gate{
		cfg:        cfg,
		httpClient: httpClient,
		byDomain:   make(map[string]*domainState),
		globalSem:  make(chan struct{}, cfg.MaxConcurrentGlobal),
	}
}

// Allowed reports whether robots.txt permits fetching the URL.
// Returns (true, nil) if IgnoreRobots is set.
func (g *Gate) Allowed(ctx context.Context, targetURL string) (bool, error) {
	if g.cfg.IgnoreRobots {
		return true, nil
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return false, fmt.Errorf("parse url: %w", err)
	}
	ds := g.stateFor(u.Host)
	if err := g.ensureRobots(ctx, u, ds); err != nil {
		// Per SPEC §3.5: on robots fetch failure, default to allowed.
		// Failing closed would punish the crawl for a third-party outage.
		return true, nil
	}
	if ds.robots == nil {
		return true, nil
	}
	return ds.robots.Test(u.Path), nil
}

// Acquire blocks until the URL's domain is below both concurrency caps
// and its rate limiter grants a token. When jitter is configured for
// the host, an additional uniformly-random delay is applied AFTER the
// limiter token to make pacing look less metronomic to behavioral
// detectors. Returns a release function that MUST be called when the
// fetch completes (use defer) and the jitter delay actually applied
// in milliseconds — the cmd-layer stamps it onto the record's
// metadata.evasion.jitter_ms.
func (g *Gate) Acquire(ctx context.Context, targetURL string) (release func(), jitterMS int64, err error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, 0, fmt.Errorf("parse url: %w", err)
	}
	ds := g.stateFor(u.Host)

	// Global concurrency cap.
	select {
	case g.globalSem <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}

	// Per-domain concurrency cap.
	select {
	case ds.sem <- struct{}{}:
	case <-ctx.Done():
		<-g.globalSem
		return nil, 0, ctx.Err()
	}

	// Per-domain rate limit.
	if err := ds.limiter.Wait(ctx); err != nil {
		<-ds.sem
		<-g.globalSem
		return nil, 0, err
	}

	// Optional jitter: a uniformly-random sleep on top of the rate
	// limiter's steady-state pacing. Only paid when JitterFraction > 0
	// — the default polite Gate skips this entirely.
	if ds.jitterFrac > 0 {
		jitter := jitterDelay(ds.limiter.Limit(), ds.jitterFrac)
		if jitter > 0 {
			t := time.NewTimer(jitter)
			select {
			case <-t.C:
				jitterMS = jitter.Milliseconds()
			case <-ctx.Done():
				t.Stop()
				<-ds.sem
				<-g.globalSem
				return nil, 0, ctx.Err()
			}
		}
	}

	return func() {
		<-ds.sem
		<-g.globalSem
	}, jitterMS, nil
}

// jitterDelay computes the jitter sleep for one Acquire call.
// baseInterval is 1/limit (the rate limiter's steady-state period);
// the actual sleep is uniform over [0, frac * baseInterval]. Negative
// values aren't possible — we never SHORTEN past the limiter's pace,
// only ADD on top of it, because shortening would defeat the polite
// floor that's the whole point of having a rate limiter.
//
// rate.Inf (effectively "no rate limit") has no meaningful base
// interval, so jitter is a no-op in that mode.
func jitterDelay(limit rate.Limit, frac float64) time.Duration {
	if limit <= 0 || limit == rate.Inf {
		return 0
	}
	baseInterval := time.Duration(float64(time.Second) / float64(limit))
	max := time.Duration(float64(baseInterval) * frac)
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Float64() * float64(max))
}

func (g *Gate) stateFor(host string) *domainState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ds, ok := g.byDomain[host]; ok {
		return ds
	}
	// Resolve the effective rate and per-host concurrency cap,
	// consulting per-host rules if the Gate has any. Burst stays
	// global for v1 — nobody has asked for per-host burst.
	effRate, effConc, effJitter := g.effectiveHostConfig(host)
	if effConc <= 0 {
		effConc = g.cfg.MaxConcurrentPerDomain
	}
	ds := &domainState{
		limiter:    rate.NewLimiter(effRate, g.cfg.BurstPerDomain),
		sem:        make(chan struct{}, effConc),
		jitterFrac: effJitter,
	}
	g.byDomain[host] = ds
	return ds
}

func (g *Gate) ensureRobots(ctx context.Context, u *url.URL, ds *domainState) error {
	g.mu.Lock()
	if ds.loaded {
		g.mu.Unlock()
		return nil
	}
	g.mu.Unlock()

	// Fetch with a bounded timeout independent of the caller's context,
	// so a slow robots.txt doesn't block a long crawl's context.
	fetchCtx, cancel := context.WithTimeout(ctx, g.cfg.RobotsTimeout)
	defer cancel()

	robotsURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}).String()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", g.cfg.UserAgent)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		g.markLoaded(ds, nil)
		return err
	}
	defer resp.Body.Close()

	// 4xx → treat as "no robots.txt, everything allowed".
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		g.markLoaded(ds, nil)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		g.markLoaded(ds, nil)
		return err
	}
	data, err := robotstxt.FromBytes(body)
	if err != nil {
		g.markLoaded(ds, nil)
		return err
	}
	g.markLoaded(ds, data.FindGroup(g.cfg.UserAgent))
	return nil
}

func (g *Gate) markLoaded(ds *domainState, grp *robotstxt.Group) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ds.robots = grp
	ds.loaded = true
}
