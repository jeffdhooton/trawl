package job

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// crawlState is the per-job bundle for BFS crawl mode. Nil in batch
// mode. The atomic counter tracks how many URLs the frontier
// currently holds (seed + children) so workers can stop enqueueing
// once the --limit cap is hit. It's an approximate bound — a few
// over is fine, the frontier is already deduping via canonical URL.
type crawlState struct {
	maxDepth   int
	sameDomain bool
	limit      int // 0 = unlimited
	enqueued   atomic.Int64
}

// tryReserve attempts to reserve an enqueue slot. Returns false if
// the limit has already been hit. The counter is advisory — the
// frontier's canonical-URL dedup is still the source of truth for
// "did this actually get added." We decrement on dedup-rejected
// enqueues below.
func (c *crawlState) tryReserve() bool {
	if c == nil || c.limit <= 0 {
		if c != nil {
			c.enqueued.Add(1)
		}
		return true
	}
	for {
		cur := c.enqueued.Load()
		if cur >= int64(c.limit) {
			return false
		}
		if c.enqueued.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// release returns a previously-reserved slot. Called when the
// frontier rejects an enqueue (URL already seen) so duplicate children
// don't eat into the limit budget.
func (c *crawlState) release() {
	if c == nil {
		return
	}
	c.enqueued.Add(-1)
}

type workerStats struct {
	start time.Time
}

// errNoMatchingLink is returned by resolveFollowLink when the
// prefetch succeeded but no <a> matched the selector. Callers use
// errors.Is to distinguish this from underlying fetch failures.
var errNoMatchingLink = errors.New("no matching link")

// shouldTryFallback is the hybrid-discovery trigger rule: the worker
// only re-routes through the fallback URL when the primary failed
// with one of a small set of categories. Kept narrow (http_4xx,
// dns_failure) to avoid wasting budget on domains where the whole
// host is dead.
func shouldTryFallback(cat failure.Category) bool {
	return cat == failure.CatHTTP4xx || cat == failure.CatDNS
}

// resolveFollowLink routes the input URL through the tiered router,
// then returns the first <a> whose href matches the CSS selector,
// resolved to an absolute URL and canonicalized.
//
// The prefetch uses the FULL router (not HTTP-only) so SPA homepages
// can escalate to chromium to discover nav links that only exist in
// the hydrated DOM.
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
	copts ContentOpts,
	cstate *crawlState,
) {
	for {
		if ctx.Err() != nil {
			return
		}

		var rec frontier.Record
		var err error
		if cstate != nil {
			// Crawl mode: block until new work arrives or the crawl
			// is quiescent (queue empty AND no worker in flight).
			rec, err = f.BlockingNext(ctx)
		} else {
			// Batch mode: the frontier is pre-filled, so ErrEmpty
			// means done.
			rec, err = f.Next()
		}
		if errors.Is(err, frontier.ErrEmpty) {
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if err != nil {
			log.Error().Err(err).Msg("frontier.Next")
			return
		}

		processOne(ctx, rec, f, gate, r, fields, sink, deadLetter, collector,
			fallbackSelector, copts, cstate)
		_ = id
	}
}

func processOne(
	ctx context.Context,
	frec frontier.Record,
	f *frontier.Frontier,
	gate *politeness.Gate,
	r *router.Router,
	fields []extract.Field,
	sink output.Sink,
	deadLetter output.Sink,
	collector *stats.Collector,
	fallbackSelector string,
	copts ContentOpts,
	cstate *crawlState,
) {
	canonURL := frec.URL
	fallbackURL := frec.Fallback
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

	release, jitterMS, err := gate.Acquire(ctx, canonURL)
	if err != nil {
		// All Acquire errors are transient from the URL's perspective.
		// The URL stays in_flight and Recover on restart will requeue
		// it.
		l.Debug().Err(err).Msg("acquire transient failure, leaving in-flight for resume")
		return
	}
	defer release()

	record, best, routeErr := routeAndBuildWithResult(ctx, r, canonURL, canonURL, fields, copts)
	stampEvasion(&record, best, jitterMS)

	if ctx.Err() != nil {
		return
	}

	primaryCat := failure.Category(record.FailureCategory)
	wantFallback := fallbackSelector != "" && fallbackURL != "" && shouldTryFallback(primaryCat)
	if wantFallback {
		fallbackRec, cat, ok := tryFallback(ctx, r, canonURL, fallbackURL, fallbackSelector, fields, collector, copts)
		if ctx.Err() != nil {
			return
		}
		if ok {
			record = fallbackRec
			routeErr = nil
			primaryCat = cat
			l.Debug().
				Str("fallback_url", fallbackURL).
				Str("resolved", record.CanonicalURL).
				Str("tier", record.Tier).
				Msg("hybrid fallback succeeded")
		} else {
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

	// Crawl mode: discover links on success. Failed fetches have no
	// body to parse, so we skip discovery there. MarkDone happens
	// AFTER discovery so BlockingNext's quiescence check doesn't
	// race — children are enqueued while this URL still counts as
	// in-flight, meaning a sibling worker that wakes on the enqueue
	// broadcast can't mistakenly conclude the crawl is done.
	if cstate != nil && best != nil {
		discoverAndEnqueue(l, f, best, frec.Depth, cstate)
	}
	_ = f.MarkDone(canonURL, tier)
}

// discoverAndEnqueue parses links from the fetched body and enqueues
// every unseen child at parentDepth+1, subject to the --depth,
// --limit, and --same-domain caps in cstate.
func discoverAndEnqueue(l zlog, f *frontier.Frontier, best *engine.Result, parentDepth int, cstate *crawlState) {
	childDepth := parentDepth + 1
	if childDepth > cstate.maxDepth {
		return
	}
	if !isHTML(best.ContentType) {
		return
	}
	base := best.FinalURL
	if base == "" {
		base = best.URL
	}
	links, err := extract.AllLinks(best.Body, base, extract.LinkOptions{SameDomain: cstate.sameDomain})
	if err != nil {
		l.Debug().Err(err).Msg("crawl: link extraction failed")
		return
	}
	added := 0
	for _, link := range links {
		if !cstate.tryReserve() {
			l.Debug().Int("limit", cstate.limit).Msg("crawl: limit reached, stop enqueueing children")
			return
		}
		_, wasAdded, err := f.EnqueueWithDepth(link, childDepth)
		if err != nil {
			cstate.release()
			l.Debug().Str("child", link).Err(err).Msg("crawl: skip invalid child")
			continue
		}
		if !wasAdded {
			cstate.release()
			continue
		}
		added++
	}
	if added > 0 {
		l.Debug().Int("children", added).Int("depth", childDepth).Msg("crawl: enqueued children")
	}
}

// zlog aliases zerolog.Logger so discoverAndEnqueue's signature reads
// naturally without a second import in every caller.
type zlog = zerolog.Logger

// tryFallback runs the hybrid-discovery fallback path for a single
// row. On success returns a fully-populated output.Record whose URL
// is the original primary (for seed-join), whose CanonicalURL is the
// resolved link, and whose metadata.Discovery captures the hybrid
// path.
func tryFallback(
	ctx context.Context,
	r *router.Router,
	primaryURL, fallbackURL, selector string,
	fields []extract.Field,
	collector *stats.Collector,
	copts ContentOpts,
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

	rec, routeErr := routeAndBuild(ctx, r, resolved, primaryURL, fields, copts)
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
