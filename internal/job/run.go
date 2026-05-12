package job

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/frontier"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/jeffdhooton/trawl/internal/schema"
	"github.com/jeffdhooton/trawl/internal/stats"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// Run drains the frontier at jobDir using the given config. Used by
// `trawl batch`, `trawl crawl`, `trawl resume`, and the MCP
// batch/crawl tools. The frontier must already be seeded before this
// is called — Run does not enqueue URLs itself.
//
// On context cancellation, in-flight URLs are left in their in_flight
// state; a follow-up Run/Resume call will Recover them. The output
// sink is flushed and closed before returning. stats.json is written
// to jobDir even on partial completion.
func Run(ctx context.Context, jobDir string, cfg *Config) error {
	fields, err := ParseFieldSpecs(cfg.Selectors)
	if err != nil {
		return err
	}

	f, err := frontier.Open(filepath.Join(jobDir, "frontier"))
	if err != nil {
		return fmt.Errorf("open frontier: %w", err)
	}
	defer f.Close()

	recovered, err := f.Recover()
	if err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	if recovered > 0 {
		log.Info().Int("recovered", recovered).Msg("requeued in-flight URLs from previous run")
	}

	if len(cfg.CSVColumns) > 0 && !output.IsCSVPath(cfg.OutputPath) {
		return fmt.Errorf("--csv-columns is only valid with a .csv or .tsv output path")
	}
	sink, err := output.NewFile(cfg.OutputPath, cfg.CSVColumns)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer sink.Close()

	// Dead-letter queue: a strict subset of the primary sink — only
	// unreachable records (DNS / TLS / 4xx / 5xx). Stays JSONL
	// regardless of the primary sink format because benchmark scripts
	// depend on the shape and nobody wants a dead-letter .csv.
	deadLetterPath := filepath.Join(jobDir, "dead_letter.jsonl")
	deadLetter, err := output.NewJSONLFile(deadLetterPath)
	if err != nil {
		return fmt.Errorf("open dead letter: %w", err)
	}
	defer deadLetter.Close()

	httpCfg := engine.DefaultHTTPConfig()
	httpCfg.Timeout = cfg.TimeoutDuration()
	httpCfg.MaxRetries = cfg.Retries
	if d := cfg.RetryDelayDuration(); d > 0 {
		httpCfg.RetryBaseDelay = d
	}

	gateCfg := politeness.Default()
	gateCfg.UserAgent = httpCfg.UserAgent
	gateCfg.IgnoreRobots = cfg.IgnoreRobots
	if cfg.RatePerSec > 0 {
		gateCfg.RatePerDomain = rate.Limit(cfg.RatePerSec)
	}
	if cfg.BurstPerSec > 0 {
		gateCfg.BurstPerDomain = cfg.BurstPerSec
	}
	gateCfg.MaxConcurrentGlobal = cfg.Concurrency

	chromiumCfg := engine.DefaultChromiumConfig()
	chromiumCfg.ViewportWidth = cfg.ViewportWidth
	chromiumCfg.ViewportHeight = cfg.ViewportHeight
	chromiumCfg.ExecPath = cfg.BrowserPath
	chromiumCfg.ExtraArgs = cfg.BrowserArgs
	jobEvasion := EvasionOpts{
		BrowserLike:       cfg.BrowserLike,
		UserAgentStrategy: cfg.UserAgentStrategy,
		Stealth:           cfg.Stealth,
		NoJitter:          cfg.NoJitter,
		TLSMatch:          cfg.TLSMatch,
	}
	if err := ApplyEvasion(&httpCfg, &gateCfg, &chromiumCfg, jobEvasion); err != nil {
		return err
	}
	jobProxy := ProxyOpts{
		URL:            cfg.ProxyURL,
		File:           cfg.ProxyFile,
		RotateOnStatus: cfg.RotateOnStatus,
		RotateRetries:  cfg.RotateRetries,
	}
	pr, err := ApplyProxy(&httpCfg, &chromiumCfg, jobProxy)
	if err != nil {
		return err
	}

	r, err := buildRouter(cfg.TierList(), cfg.ForceTier, httpCfg, chromiumCfg)
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}
	defer r.Close()

	r.WithProxyRotation(pr.Rotator, pr.RotateCodes, pr.RotateMax)

	tierCache, tierCachePath, _ := openTierCache(cfg.NoTierLearning, cfg.TierCachePath)
	defer tierCache.Close()
	r.WithCache(tierCache)
	if tierCachePath != "" {
		log.Info().Str("tier_cache", tierCachePath).Msg("tier learning enabled")
	}

	cacheTTL := parseCacheTTL(cfg.CacheTTL, 24*time.Hour)
	contentCache, contentCachePath, _ := openContentCache(cfg.CacheEnabled, cfg.CachePath, cacheTTL)
	defer contentCache.Close()
	r.WithContentCache(contentCache)
	if contentCachePath != "" {
		log.Info().
			Str("content_cache", contentCachePath).
			Str("ttl", cacheTTL.String()).
			Msg("content cache enabled")
	}

	gate := politeness.NewGate(gateCfg, nil)
	if cfg.PolitenessPath != "" {
		hr, err := politeness.LoadHostRules(cfg.PolitenessPath)
		if err != nil {
			return fmt.Errorf("load politeness rules: %w", err)
		}
		gate.WithHostRules(hr)
		log.Info().
			Str("politeness", cfg.PolitenessPath).
			Int("rules", len(hr.Hosts)).
			Msg("per-host politeness rules loaded")
	}

	if cfg.IgnoreRobots {
		log.Warn().Msg("robots.txt is being ignored for this job")
	}
	LogEvasion(jobEvasion)
	LogProxy(jobProxy)

	var wg sync.WaitGroup
	wstats := &workerStats{start: time.Now()}
	collector := stats.New()

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 20
	}

	copts := ContentOpts{
		Format:        cfg.Format,
		Readability:   cfg.Readability,
		NoMetadata:    cfg.NoMetadata,
		ScreenshotDir: cfg.ScreenshotDir,
		PDF: PDFOpts{
			OCR:      cfg.OCR,
			OCRLang:  cfg.OCRLang,
			MaxPages: cfg.PDFMaxPages,
		}.Build(),
	}
	if cfg.SchemaPath != "" {
		s, err := schema.Load(cfg.SchemaPath)
		if err != nil {
			return fmt.Errorf("load schema: %w", err)
		}
		copts.Schema = s
		log.Info().Str("schema", cfg.SchemaPath).Int("fields", len(s.Fields)).Msg("schema loaded")
	}
	if cda, err := ParseActions(cfg.InlineActions, cfg.ActionsPath); err != nil {
		return err
	} else if cda != nil {
		copts.Actions = cda
		log.Info().Int("steps", len(cda)).Msg("interactive actions loaded")
	}

	var cstate *crawlState
	if cfg.CrawlMode {
		fs, err := f.Stats()
		if err != nil {
			return fmt.Errorf("frontier stats: %w", err)
		}
		cstate = &crawlState{
			maxDepth:   cfg.CrawlMaxDepth,
			sameDomain: cfg.CrawlSameDomain,
			limit:      cfg.CrawlLimit,
		}
		cstate.enqueued.Store(int64(fs.Total))
		log.Info().
			Int("max_depth", cstate.maxDepth).
			Int("limit", cstate.limit).
			Bool("same_domain", cstate.sameDomain).
			Int("already_enqueued", fs.Total).
			Msg("crawl mode active")

		// Watchdog: on ctx cancellation wake the frontier so any
		// blocked BlockingNext returns. Exits on wg completion via
		// the done chan.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				f.Wake()
			case <-done:
			}
		}()
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWorker(ctx, id, f, gate, r, fields, sink, deadLetter, collector, wstats,
				cfg.FallbackSelector, copts, cstate)
		}(i)
	}
	wg.Wait()

	statsPath := filepath.Join(jobDir, "stats.json")
	snap := collector.Snapshot(cfg.ID)
	if err := stats.WriteJSON(statsPath, snap); err != nil {
		log.Warn().Err(err).Msg("failed to write stats.json")
	}

	s, err := f.Stats()
	if err != nil {
		return err
	}
	total := s.Done + s.Failed
	var successRate float64
	if total > 0 {
		successRate = float64(s.Done) / float64(total)
	}
	log.Info().
		Int("done", s.Done).
		Int("failed", s.Failed).
		Int("total", total).
		Float64("success_rate", successRate).
		Int("queued", s.Queued).
		Int("in_flight", s.InFlight).
		Str("elapsed", time.Since(wstats.start).Round(time.Millisecond).String()).
		Msg("job complete")

	if ctxErr := ctx.Err(); ctxErr != nil && s.Queued > 0 {
		log.Warn().
			Int("remaining", s.Queued).
			Str("job_id", cfg.ID).
			Msgf("interrupted — %d urls remain. Resume with: trawl resume %s", s.Queued, filepath.Base(jobDir))
	}
	return nil
}

// ScrapeOpts captures the per-call knobs for RunOne. Mirrors the
// `trawl scrape` flag set but lives in this package so MCP/library
// callers don't need to depend on cmd/trawl.
//
// Sink is optional — when nil, RunOne just returns the record. When
// set, the record is also written to the sink before return so CLI
// callers can keep their existing JSONL/CSV file output.
type ScrapeOpts struct {
	Selectors      []string
	Output         output.Sink // nil = just return the record
	IgnoreRobots   bool
	Timeout        time.Duration
	Tiers          string
	ForceTier      string
	NoTierLearning bool
	TierCachePath  string
	Format         string
	Readability    bool
	NoMetadata     bool
	ScreenshotDir  string
	ViewportWidth  int
	ViewportHeight int
	BrowserPath    string
	BrowserArgs    []string
	CacheEnabled   bool
	CacheTTL       time.Duration
	CachePath      string
	SchemaPath     string
	Retries        int
	RetryDelay     time.Duration
	PolitenessPath string
	Proxy          ProxyOpts
	Evasion        EvasionOpts
	InlineActions  []string
	ActionsPath    string
	PDF            PDFOpts
}

// RunOne fetches one URL through the tier ladder and returns the
// resulting output.Record. If opts.Output is non-nil, the record is
// also written to the sink before return. Used by `trawl scrape` and
// the MCP scrape tool.
//
// Errors fall into two buckets: (1) setup errors before any fetch
// happens (bad schema, bad selector, blocked by robots) — the record
// is empty/zero. (2) fetch errors after at least one tier ran — the
// record is fully populated with the failure category and any partial
// response data, mirroring the JSONL shape that batch produces.
func RunOne(parentCtx context.Context, rawURL string, opts ScrapeOpts) (output.Record, error) {
	ctx := parentCtx

	canonURL, err := canonical.Canonicalize(rawURL, canonical.Options{})
	if err != nil {
		return output.Record{}, fmt.Errorf("canonicalize: %w", err)
	}

	fields, err := ParseFieldSpecs(opts.Selectors)
	if err != nil {
		return output.Record{}, err
	}

	httpCfg := engine.DefaultHTTPConfig()
	if opts.Timeout > 0 {
		httpCfg.Timeout = opts.Timeout
	}
	httpCfg.MaxRetries = opts.Retries
	if opts.RetryDelay > 0 {
		httpCfg.RetryBaseDelay = opts.RetryDelay
	}
	gateCfg := politeness.Default()
	gateCfg.UserAgent = httpCfg.UserAgent
	gateCfg.IgnoreRobots = opts.IgnoreRobots
	chromiumCfg := engine.DefaultChromiumConfig()
	chromiumCfg.ViewportWidth = opts.ViewportWidth
	chromiumCfg.ViewportHeight = opts.ViewportHeight
	chromiumCfg.ExecPath = opts.BrowserPath
	chromiumCfg.ExtraArgs = opts.BrowserArgs
	if err := ApplyEvasion(&httpCfg, &gateCfg, &chromiumCfg, opts.Evasion); err != nil {
		return output.Record{}, err
	}
	pr, err := ApplyProxy(&httpCfg, &chromiumCfg, opts.Proxy)
	if err != nil {
		return output.Record{}, err
	}
	tiers := parseTierList(opts.Tiers)
	if len(tiers) == 0 && opts.ForceTier == "" {
		tiers = []string{"http", "chromium"}
	}
	r, err := buildRouter(tiers, opts.ForceTier, httpCfg, chromiumCfg)
	if err != nil {
		return output.Record{}, err
	}
	defer r.Close()

	r.WithProxyRotation(pr.Rotator, pr.RotateCodes, pr.RotateMax)

	tierCache, _, _ := openTierCache(opts.NoTierLearning, opts.TierCachePath)
	defer tierCache.Close()
	r.WithCache(tierCache)

	contentCache, contentCachePath, _ := openContentCache(opts.CacheEnabled, opts.CachePath, opts.CacheTTL)
	defer contentCache.Close()
	r.WithContentCache(contentCache)
	if contentCachePath != "" {
		log.Info().
			Str("content_cache", contentCachePath).
			Str("ttl", opts.CacheTTL.String()).
			Msg("content cache enabled")
	}

	gate := politeness.NewGate(gateCfg, nil)
	if opts.PolitenessPath != "" {
		hr, err := politeness.LoadHostRules(opts.PolitenessPath)
		if err != nil {
			return output.Record{}, fmt.Errorf("load politeness rules: %w", err)
		}
		gate.WithHostRules(hr)
		log.Info().Str("politeness", opts.PolitenessPath).Int("rules", len(hr.Hosts)).Msg("per-host politeness rules loaded")
	}

	if opts.IgnoreRobots {
		log.Warn().Str("url", canonURL).Msg("robots.txt is being ignored")
	}
	LogEvasion(opts.Evasion)
	LogProxy(opts.Proxy)

	allowed, err := gate.Allowed(ctx, canonURL)
	if err != nil {
		return output.Record{}, fmt.Errorf("check robots: %w", err)
	}
	if !allowed {
		return output.Record{}, fmt.Errorf("blocked by robots.txt: %s", canonURL)
	}

	release, jitterMS, err := gate.Acquire(ctx, canonURL)
	if err != nil {
		return output.Record{}, fmt.Errorf("politeness: %w", err)
	}
	defer release()

	if err := opts.PDF.Validate(); err != nil {
		return output.Record{}, err
	}
	opts.PDF.Log()

	copts := ContentOpts{
		Format:        opts.Format,
		Readability:   opts.Readability,
		NoMetadata:    opts.NoMetadata,
		ScreenshotDir: opts.ScreenshotDir,
		PDF:           opts.PDF.Build(),
	}
	if err := ValidateFormat(copts.Format); err != nil {
		return output.Record{}, err
	}
	if opts.SchemaPath != "" {
		s, err := schema.Load(opts.SchemaPath)
		if err != nil {
			return output.Record{}, fmt.Errorf("load schema: %w", err)
		}
		copts.Schema = s
	}
	if cda, err := ParseActions(opts.InlineActions, opts.ActionsPath); err != nil {
		return output.Record{}, err
	} else if cda != nil {
		copts.Actions = cda
	}

	rec, best, routeErr := routeAndBuildWithResult(ctx, r, canonURL, rawURL, fields, copts)
	stampEvasion(&rec, best, jitterMS)
	if opts.Output != nil {
		if werr := opts.Output.Write(rec); werr != nil {
			return rec, fmt.Errorf("write record: %w", werr)
		}
	}
	if routeErr != nil {
		return rec, routeErr
	}
	if rec.Error != "" {
		return rec, errors.New(rec.Error)
	}
	return rec, nil
}
