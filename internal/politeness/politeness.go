// Package politeness enforces robots.txt, per-domain rate limits, and
// concurrency caps. Politeness is on by default; opting out requires
// explicit configuration.
package politeness

import (
	"context"
	"fmt"
	"io"
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

// Acquire blocks until the URL's domain is below both concurrency caps and
// its rate limiter grants a token. Returns a release function that MUST be
// called when the fetch completes (use defer).
func (g *Gate) Acquire(ctx context.Context, targetURL string) (release func(), err error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	ds := g.stateFor(u.Host)

	// Global concurrency cap.
	select {
	case g.globalSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Per-domain concurrency cap.
	select {
	case ds.sem <- struct{}{}:
	case <-ctx.Done():
		<-g.globalSem
		return nil, ctx.Err()
	}

	// Per-domain rate limit.
	if err := ds.limiter.Wait(ctx); err != nil {
		<-ds.sem
		<-g.globalSem
		return nil, err
	}

	return func() {
		<-ds.sem
		<-g.globalSem
	}, nil
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
	effRate, effConc := g.effectiveHostConfig(host)
	if effConc <= 0 {
		effConc = g.cfg.MaxConcurrentPerDomain
	}
	ds := &domainState{
		limiter: rate.NewLimiter(effRate, g.cfg.BurstPerDomain),
		sem:     make(chan struct{}, effConc),
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
