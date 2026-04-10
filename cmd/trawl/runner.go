package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/extract"
	"github.com/jeffdhooton/trawl/internal/failure"
	"github.com/jeffdhooton/trawl/internal/frontier"
	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/jeffdhooton/trawl/internal/stats"
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

	// Dead-letter queue: a strict subset of results.jsonl containing only
	// unreachable records (DNS / TLS / 4xx / 5xx / etc). Makes it cheap for
	// benchmark scripts to exclude dead rows from denominators without
	// re-filtering the full results file.
	deadLetterPath := filepath.Join(jobDir, "dead_letter.jsonl")
	deadLetter, err := output.NewJSONLFile(deadLetterPath)
	if err != nil {
		return fmt.Errorf("open dead letter: %w", err)
	}
	defer deadLetter.Close()

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
	wstats := &workerStats{start: time.Now()}
	collector := stats.New()

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 20
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWorker(ctx, id, f, gate, r, fields, sink, deadLetter, collector, wstats,
				cfg.FallbackSelector)
		}(i)
	}
	wg.Wait()

	// Write stats.json before we return so even interrupted runs produce
	// a partial snapshot. Benchmark scripts depend on this artifact.
	statsPath := filepath.Join(jobDir, "stats.json")
	snap := collector.Snapshot(cfg.ID)
	if err := stats.WriteJSON(statsPath, snap); err != nil {
		log.Warn().Err(err).Msg("failed to write stats.json")
	}

	s, err := f.Stats()
	if err != nil {
		return err
	}
	log.Info().
		Int("done", s.Done).
		Int("failed", s.Failed).
		Int("queued", s.Queued).
		Int("in_flight", s.InFlight).
		Str("elapsed", time.Since(wstats.start).Round(time.Millisecond).String()).
		Msg("job complete")

	if ctxErr := ctx.Err(); ctxErr != nil && s.Queued > 0 {
		stderrf("interrupted — %d urls remain. Resume with: trawl resume %s\n", s.Queued, filepath.Base(jobDir))
	}
	return nil
}

type workerStats struct {
	start time.Time
}

// errNoMatchingLink is returned by resolveFollowLink when the prefetch
// succeeded but no <a> matched the selector. Callers use errors.Is to
// distinguish this from underlying fetch failures.
var errNoMatchingLink = errors.New("no matching link")

// resolveFollowLink routes the input URL through the tiered router, then
// returns the first <a> whose href matches the CSS selector, resolved to
// an absolute URL and canonicalized.
//
// The prefetch uses the FULL router (not HTTP-only) so SPA homepages can
// escalate to chromium to discover nav links that only exist in the
// hydrated DOM. This has a measurable cost (chromium startup per page)
// but gives us real data on SPA nav prevalence.
//
// The returned tier string is the tier that served the prefetch — useful
// for stats aggregation to understand per-tier cost of discovery.
//
// Errors:
//   - errNoMatchingLink    — prefetch succeeded, selector matched nothing
//   - anything else        — underlying fetch/canonicalize failure, wrapped
func resolveFollowLink(
	ctx context.Context,
	r *router.Router,
	inputURL string,
	selector string,
) (resolved, tier string, err error) {
	outcome, routeErr := r.Route(ctx, engine.Request{URL: inputURL})
	if routeErr != nil {
		return "", "", routeErr
	}
	if outcome.Result == nil {
		return "", outcome.Tier, fmt.Errorf("prefetch: no result")
	}
	res := outcome.Result

	base := res.FinalURL
	if base == "" {
		base = inputURL
	}
	raw, err := extract.FirstLink(res.Body, base, selector, extract.LinkOptions{SameDomain: true})
	if err != nil {
		return "", outcome.Tier, fmt.Errorf("extract: %w", err)
	}
	if raw == "" {
		return "", outcome.Tier, errNoMatchingLink
	}
	canon, err := canonical.Canonicalize(raw, canonical.Options{})
	if err != nil {
		return "", outcome.Tier, fmt.Errorf("canon: %w", err)
	}
	return canon, outcome.Tier, nil
}

func runWorker(
	ctx context.Context,
	id int,
	f *frontier.Frontier,
	gate *politeness.Gate,
	r *router.Router,
	fields []extract.Field,
	sink output.Sink,
	deadLetter output.Sink,
	collector *stats.Collector,
	_ *workerStats,
	fallbackSelector string,
) {
	for {
		if ctx.Err() != nil {
			return
		}

		rec, err := f.Next()
		if errors.Is(err, frontier.ErrEmpty) {
			// Batch mode enqueues everything up-front, so an empty frontier
			// means we're done. Full BFS crawl (later) will need workers
			// to block until new URLs arrive.
			return
		}
		if err != nil {
			log.Error().Err(err).Msg("frontier.Next")
			return
		}

		processOne(ctx, rec.URL, rec.Fallback, f, gate, r, fields, sink, deadLetter, collector,
			fallbackSelector)
	}
}

// shouldTryFallback is the hybrid-discovery trigger rule: the worker only
// re-routes through the fallback URL when the primary failed with one of a
// small set of categories. Kept narrow (http_4xx, dns_failure) to avoid
// wasting budget on domains where the whole host is dead.
func shouldTryFallback(cat failure.Category) bool {
	return cat == failure.CatHTTP4xx || cat == failure.CatDNS
}

func processOne(
	ctx context.Context,
	canonURL string,
	fallbackURL string,
	f *frontier.Frontier,
	gate *politeness.Gate,
	r *router.Router,
	fields []extract.Field,
	sink output.Sink,
	deadLetter output.Sink,
	collector *stats.Collector,
	fallbackSelector string,
) {
	l := log.With().Str("url", canonURL).Logger()
	firstTier := r.Tiers()[0]

	allowed, err := gate.Allowed(ctx, canonURL)
	if err != nil {
		l.Warn().Err(err).Msg("robots check failed, allowing")
	}
	if !allowed {
		_ = f.MarkFailed(canonURL, firstTier, errors.New("blocked by robots.txt"))
		rejected := output.Record{
			URL:             canonURL,
			CanonicalURL:    canonURL,
			FetchedAt:       time.Now().UTC(),
			Tier:            firstTier,
			Error:           "blocked by robots.txt",
			FailureCategory: string(failure.CatRobotsBlocked),
		}
		_ = sink.Write(rejected)
		_ = deadLetter.Write(rejected)
		collector.Record(failure.CatRobotsBlocked, firstTier, 0)
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

	// First attempt: route the primary URL through the full tier ladder.
	record, routeErr := routeAndBuild(ctx, r, canonURL, canonURL, fields)

	if ctx.Err() != nil {
		// Caller context cancelled — every tier likely failed with a deadline
		// error, not a real failure. Leave the URL in-flight so Recover on
		// restart puts it back into the queue.
		return
	}

	// Hybrid discovery: if the primary failed with a trigger category AND the
	// seed row carried a fallback URL AND the operator asked for fallback,
	// try resolving the target through the fallback URL's homepage.
	//
	// The fallback path completely replaces the primary record — we don't
	// emit two rows per seed. The output record's `url` field stays as the
	// original primary (for jq joins back to the seed), `canonical_url` is
	// the resolved link the fallback path produced, and metadata.discovery
	// captures what happened. Stats.json increments fallback.{attempted,
	// succeeded, no_link, unreachable}.
	primaryCat := failure.Category(record.FailureCategory)
	wantFallback := fallbackSelector != "" && fallbackURL != "" && shouldTryFallback(primaryCat)
	if wantFallback {
		fallbackRec, cat, ok := tryFallback(ctx, r, canonURL, fallbackURL, fallbackSelector, fields, collector)
		if ctx.Err() != nil {
			return
		}
		if ok {
			// Successful hybrid discovery. Replace the primary record outright.
			record = fallbackRec
			routeErr = nil
			primaryCat = cat
			l.Debug().
				Str("fallback_url", fallbackURL).
				Str("resolved", record.CanonicalURL).
				Str("tier", record.Tier).
				Msg("hybrid fallback succeeded")
		} else {
			// Fallback attempt failed. Annotate the primary record with
			// discovery metadata so downstream can see a fallback was tried,
			// then persist the primary as-is. The primary's failure_category
			// stays intact.
			if record.Metadata.Discovery == nil {
				record.Metadata.Discovery = &output.DiscoveryStats{
					Path:        "primary",
					PrimaryURL:  canonURL,
					FallbackURL: fallbackURL,
				}
			}
			l.Debug().
				Str("fallback_url", fallbackURL).
				Str("primary_category", string(primaryCat)).
				Msg("hybrid fallback did not recover the row")
		}
	} else if fallbackSelector != "" && fallbackURL != "" {
		// Fallback was configured for this row, primary succeeded (or failed
		// in a non-trigger category) — tag discovery.path=primary so a later
		// comparison run can count "fallback would not have been tried."
		if record.Metadata.Discovery == nil {
			record.Metadata.Discovery = &output.DiscoveryStats{
				Path:        "primary",
				PrimaryURL:  canonURL,
				FallbackURL: fallbackURL,
			}
		}
	}

	if err := sink.Write(record); err != nil {
		l.Error().Err(err).Msg("write record")
	}
	// Dead-letter queue mirrors any unreachable record so benchmark scripts
	// can cheaply isolate "dead data" from "real tier decisions."
	if !primaryCat.IsReachable() {
		if err := deadLetter.Write(record); err != nil {
			l.Error().Err(err).Msg("write dead letter")
		}
	}

	tier := record.Tier
	if tier == "" {
		tier = firstTier
	}
	collector.Record(primaryCat, tier, record.DurationMS)
	if routeErr != nil || record.Error != "" {
		msg := record.Error
		if routeErr != nil {
			msg = routeErr.Error()
		}
		_ = f.MarkFailed(canonURL, tier, errors.New(msg))
		l.Debug().Str("tier", tier).Str("category", record.FailureCategory).Str("err", msg).Msg("fetch failed")
		return
	}
	_ = f.MarkDone(canonURL, tier)
}

// tryFallback runs the hybrid-discovery fallback path for a single row.
// On success, it returns a fully-populated output.Record whose URL is the
// original primary (for seed-join), whose CanonicalURL is the resolved link,
// and whose metadata.Discovery captures the hybrid path.
//
// The returned ok is false when: the fallback prefetch failed, the selector
// matched nothing, or the resolved-link fetch returned an unreachable
// category. In all failure modes the collector's fallback counters are
// updated before returning.
func tryFallback(
	ctx context.Context,
	r *router.Router,
	primaryURL, fallbackURL, selector string,
	fields []extract.Field,
	collector *stats.Collector,
) (output.Record, failure.Category, bool) {
	resolved, _, err := resolveFollowLink(ctx, r, fallbackURL, selector)
	if err != nil {
		if errors.Is(err, errNoMatchingLink) {
			collector.RecordFallback("no_link")
		} else {
			collector.RecordFallback("unreachable")
		}
		return output.Record{}, "", false
	}

	rec, routeErr := routeAndBuild(ctx, r, resolved, primaryURL, fields)
	cat := failure.Category(rec.FailureCategory)
	if rec.Metadata.Discovery == nil {
		rec.Metadata.Discovery = &output.DiscoveryStats{
			Path:        "fallback",
			PrimaryURL:  primaryURL,
			FallbackURL: fallbackURL,
		}
	}
	if routeErr != nil || !cat.IsReachable() {
		collector.RecordFallback("unreachable")
		return rec, cat, false
	}
	collector.RecordFallback("succeeded")
	return rec, cat, true
}
