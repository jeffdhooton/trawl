package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/frontier"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// runJob drains the frontier at jobDir using the given config. It is shared
// by the batch and resume commands.
func runJob(ctx context.Context, jobDir string, cfg *JobConfig) error {
	fields, err := parseFieldSpecs(cfg.Selectors)
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

	sink, err := output.NewJSONLFile(cfg.OutputPath)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer sink.Close()

	httpCfg := engine.DefaultHTTPConfig()
	httpCfg.Timeout = cfg.timeoutDuration()

	r, err := buildRouter(cfg.tierList(), cfg.ForceTier, httpCfg)
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}
	defer r.Close()

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
	gate := politeness.NewGate(gateCfg, nil)

	if cfg.IgnoreRobots {
		log.Warn().Msg("robots.txt is being ignored for this job")
	}

	var wg sync.WaitGroup
	stats := &workerStats{start: time.Now()}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 20
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWorker(ctx, id, f, gate, r, fields, sink, stats)
		}(i)
	}
	wg.Wait()

	s, err := f.Stats()
	if err != nil {
		return err
	}
	log.Info().
		Int("done", s.Done).
		Int("failed", s.Failed).
		Int("queued", s.Queued).
		Int("in_flight", s.InFlight).
		Str("elapsed", time.Since(stats.start).Round(time.Millisecond).String()).
		Msg("job complete")

	if ctxErr := ctx.Err(); ctxErr != nil && s.Queued > 0 {
		stderrf("interrupted — %d urls remain. Resume with: trawl resume %s\n", s.Queued, filepath.Base(jobDir))
	}
	return nil
}

type workerStats struct {
	start time.Time
}

func runWorker(
	ctx context.Context,
	id int,
	f *frontier.Frontier,
	gate *politeness.Gate,
	r *router.Router,
	fields []extract.Field,
	sink output.Sink,
	_ *workerStats,
) {
	for {
		if ctx.Err() != nil {
			return
		}

		rec, err := f.Next()
		if errors.Is(err, frontier.ErrEmpty) {
			// In P1 stage 1, batch mode enqueues everything up-front, so an
			// empty frontier means we're done. (Crawl mode in a later stage
			// will need workers to block until new URLs arrive.)
			return
		}
		if err != nil {
			log.Error().Err(err).Msg("frontier.Next")
			return
		}

		processOne(ctx, rec.URL, f, gate, r, fields, sink)
	}
}

func processOne(
	ctx context.Context,
	canonURL string,
	f *frontier.Frontier,
	gate *politeness.Gate,
	r *router.Router,
	fields []extract.Field,
	sink output.Sink,
) {
	l := log.With().Str("url", canonURL).Logger()
	firstTier := r.Tiers()[0]

	allowed, err := gate.Allowed(ctx, canonURL)
	if err != nil {
		l.Warn().Err(err).Msg("robots check failed, allowing")
	}
	if !allowed {
		_ = f.MarkFailed(canonURL, firstTier, errors.New("blocked by robots.txt"))
		_ = sink.Write(output.Record{
			URL:          canonURL,
			CanonicalURL: canonURL,
			FetchedAt:    time.Now().UTC(),
			Tier:         firstTier,
			Error:        "blocked by robots.txt",
		})
		return
	}

	release, err := gate.Acquire(ctx, canonURL)
	if err != nil {
		// All Acquire errors are transient from the URL's perspective:
		// ctx cancellation, rate limiter predictive refusal ("wait would
		// exceed context deadline"), etc. The URL stays in_flight and
		// Recover on restart will requeue it. The only "real" failure
		// modes of Acquire (URL parse) can't happen here because the URL
		// was already canonicalized before being enqueued.
		l.Debug().Err(err).Msg("acquire transient failure, leaving in-flight for resume")
		return
	}
	defer release()

	record, routeErr := routeAndBuild(ctx, r, canonURL, canonURL, fields)

	// If the caller's context was cancelled, every tier likely failed with a
	// deadline error — not a real failure. Leave the URL in-flight so Recover
	// on restart puts it back into the queue.
	if ctx.Err() != nil {
		return
	}

	if err := sink.Write(record); err != nil {
		l.Error().Err(err).Msg("write record")
	}

	tier := record.Tier
	if tier == "" {
		tier = firstTier
	}
	if routeErr != nil || record.Error != "" {
		msg := record.Error
		if routeErr != nil {
			msg = routeErr.Error()
		}
		_ = f.MarkFailed(canonURL, tier, errors.New(msg))
		l.Debug().Str("tier", tier).Str("err", msg).Msg("fetch failed")
		return
	}
	_ = f.MarkDone(canonURL, tier)
}
