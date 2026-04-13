// Package router implements the tiered engine router. It escalates each URL
// through a configured list of engines (cheap → expensive) until one returns
// a result that the validity checker accepts.
//
// A persistent tier-learning Cache can be attached via WithCache. The cache
// remembers which tier last succeeded for each host; on subsequent fetches
// Route reorders the ladder to try the preferred tier first. Failed fetches
// never teach — only a successful, validity-passing response updates the
// cache. See internal/tierlearn for the storage model.
package router

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jeffdhooton/trawl/internal/cache"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/tierlearn"
	"github.com/jeffdhooton/trawl/internal/validity"
	"github.com/rs/zerolog/log"
)

// ProxyRotator allows the router to force a proxy rotation for a
// target domain when a fetch returns a rotate-worthy status code.
// Implementations live in cmd/trawl (domainStickyRotator).
type ProxyRotator interface {
	// Rotate forces a new proxy assignment for the given domain.
	// Returns false if rotation is not possible (single proxy, pool
	// exhausted, or no pool configured).
	Rotate(domain string) bool
}

// Router routes fetches through a tiered set of engines.
type Router struct {
	engines      []engine.Engine
	checker      validity.Checker
	cache        tierlearn.Cache
	contentCache cache.Cache

	// Proxy rotation on specific status codes. When rotator is non-nil
	// and a live fetch returns a status code in rotateCodes, the router
	// calls rotator.Rotate(host) and retries the SAME tier up to
	// rotateRetries times before proceeding with normal escalation.
	rotator      ProxyRotator
	rotateCodes  map[int]bool
	rotateMax    int // max proxy-rotation retries per tier (0 = disabled)
}

// New constructs a Router. Engines should be ordered cheap → expensive.
// The cache defaults to NopCache (no learning). Use WithCache to attach a
// persistent tierlearn.Cache.
func New(engines []engine.Engine, checker validity.Checker) (*Router, error) {
	if len(engines) == 0 {
		return nil, errors.New("router: at least one engine required")
	}
	if checker == nil {
		checker = validity.NewChecker(validity.Default())
	}
	return &Router{
		engines:      engines,
		checker:      checker,
		cache:        tierlearn.NopCache{},
		contentCache: cache.NopCache{},
	}, nil
}

// WithCache attaches a tier-learning cache to the router. Passing nil is
// equivalent to NopCache (learning disabled). Safe to call before Route is
// invoked for the first time; not safe to call concurrently with Route.
func (r *Router) WithCache(c tierlearn.Cache) *Router {
	if c == nil {
		r.cache = tierlearn.NopCache{}
	} else {
		r.cache = c
	}
	return r
}

// WithProxyRotation configures the router to retry a tier through a
// different proxy when a fetch returns one of the given status codes.
// maxRetries caps how many proxy rotations are attempted per tier
// (0 disables rotation). Only effective when rotator is non-nil and
// the proxy pool has more than one entry.
func (r *Router) WithProxyRotation(rotator ProxyRotator, codes []int, maxRetries int) *Router {
	if rotator == nil || len(codes) == 0 || maxRetries <= 0 {
		return r
	}
	r.rotator = rotator
	r.rotateCodes = make(map[int]bool, len(codes))
	for _, c := range codes {
		r.rotateCodes[c] = true
	}
	r.rotateMax = maxRetries
	return r
}

// WithContentCache attaches a persistent content cache to the router.
// Cache entries are keyed by (canonical URL, tier name) and short-circuit
// the tier loop on hit. Passing nil is equivalent to cache.NopCache
// (caching disabled). Safe to call before Route is invoked for the
// first time; not safe to call concurrently with Route.
func (r *Router) WithContentCache(c cache.Cache) *Router {
	if c == nil {
		r.contentCache = cache.NopCache{}
	} else {
		r.contentCache = c
	}
	return r
}

// Close releases all owned engines.
func (r *Router) Close() error {
	var firstErr error
	for _, e := range r.engines {
		if err := e.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Tiers returns the engine names in order. Useful for logging and CLI help.
func (r *Router) Tiers() []string {
	names := make([]string, len(r.engines))
	for i, e := range r.engines {
		names[i] = e.Name()
	}
	return names
}

// Route fetches req by escalating through the configured tiers. It returns
// the first result whose validity check passes.
//
// Behavior:
//   - If the attached Cache has a learned tier preference for the request's
//     host, the engine ladder is reordered to try that tier first. Other
//     engines keep their relative order behind the preferred one, so the
//     fallback path on a stale preference is still sensible.
//   - If an engine's fetch fails (network error, context cancel), the router
//     captures the error and tries the next tier.
//   - If an engine's fetch succeeds but validity says Valid=false and
//     Escalate=true, the router tries the next tier.
//   - If validity says Escalate=false (e.g. 404, unsupported content-type),
//     the router stops immediately — no tier will help.
//   - If every tier is exhausted, the last attempted Result is returned so
//     callers can still persist the evidence, along with a non-nil error.
//   - On success, Cache.Observe(host, tier) records which tier served the
//     URL so the next fetch from the same host starts there.
func (r *Router) Route(ctx context.Context, req engine.Request) (*Outcome, error) {
	outcome := &Outcome{URL: req.URL}

	host := hostOf(req.URL)
	ladder := r.engines
	if host != "" {
		if preferred := r.cache.Preferred(host); preferred != "" {
			ladder = reorderLadder(r.engines, preferred)
			outcome.PreferredTier = preferred
		}
	}

	for _, e := range ladder {
		if ctx.Err() != nil {
			return outcome, ctx.Err()
		}
		attempt := Attempt{Tier: e.Name()}

		// Content cache lookup: if this (URL, tier) is cached and fresh,
		// skip the live fetch entirely. Still run validity so a cached
		// stub doesn't get served — if it fails validity we escalate
		// past the cache entry to the next tier exactly as if the live
		// fetch had returned that stub. The cached result is NOT re-put
		// on hit; put only happens after live fetches.
		if cached, hit := r.contentCache.Get(req.URL, e.Name()); hit {
			vr := r.checker.Check(validity.Page{
				URL:         req.URL,
				StatusCode:  cached.StatusCode,
				ContentType: cached.ContentType,
				Body:        cached.Body,
			})
			attempt.Result = cached
			attempt.Valid = vr.Valid
			attempt.Reason = vr.Reason
			attempt.FromCache = true
			outcome.LastResult = cached
			outcome.Attempts = append(outcome.Attempts, attempt)
			if vr.Valid {
				outcome.Tier = e.Name()
				outcome.Result = cached
				outcome.FromCache = true
				// Do NOT touch the tier-learning cache on a content-cache
				// hit — the learning signal is "what served this host
				// LIVE," and a cache replay isn't new information.
				return outcome, nil
			}
			if !vr.Escalate {
				return outcome, fmt.Errorf("%s (cached): %s", e.Name(), vr.Reason)
			}
			// Validity said escalate on a cached entry — fall through to
			// the next tier, same as a failed live fetch.
			continue
		}

		res, vr, fetchErr := r.fetchWithProxyRotation(ctx, e, req, host)
		if fetchErr != nil {
			attempt.Err = fetchErr
			outcome.Attempts = append(outcome.Attempts, attempt)
			if ctx.Err() != nil {
				return outcome, ctx.Err()
			}
			continue // try next tier on transient fetch failure
		}
		attempt.Result = res
		attempt.Valid = vr.Valid
		attempt.Reason = vr.Reason
		outcome.LastResult = res
		outcome.Attempts = append(outcome.Attempts, attempt)

		if vr.Valid {
			outcome.Tier = e.Name()
			outcome.Result = res
			if host != "" {
				r.cache.Observe(host, e.Name())
			}
			// Store successful live fetch for future replay. Failed
			// fetches and cached-replay successes are not re-put.
			r.contentCache.Put(req.URL, e.Name(), res)
			return outcome, nil
		}
		if !vr.Escalate {
			return outcome, fmt.Errorf("%s: %s", e.Name(), vr.Reason)
		}
	}

	reasons := make([]string, 0, len(outcome.Attempts))
	for _, a := range outcome.Attempts {
		if a.Err != nil {
			reasons = append(reasons, fmt.Sprintf("%s:%v", a.Tier, a.Err))
		} else {
			reasons = append(reasons, fmt.Sprintf("%s:%s", a.Tier, a.Reason))
		}
	}
	return outcome, fmt.Errorf("all tiers exhausted: %v", reasons)
}

// reorderLadder returns a new engine slice with the preferred tier moved
// to the front. Other engines keep their relative order. If the preferred
// tier isn't in the list, the original ladder is returned unchanged —
// stale cache entries pointing at removed engines silently degrade to
// the default behavior.
func reorderLadder(engines []engine.Engine, preferred string) []engine.Engine {
	idx := -1
	for i, e := range engines {
		if e.Name() == preferred {
			idx = i
			break
		}
	}
	if idx <= 0 {
		// Either not found (-1) or already at front (0). Either way, no
		// reorder needed — returning the original slice saves an alloc.
		return engines
	}
	out := make([]engine.Engine, 0, len(engines))
	out = append(out, engines[idx])
	out = append(out, engines[:idx]...)
	out = append(out, engines[idx+1:]...)
	return out
}

// hostOf extracts the host component from a URL for cache keying. Returns
// "" if the URL is unparseable — callers should treat that as "no host,
// skip the cache."
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Outcome is the full record of a routed fetch, including every tier tried.
type Outcome struct {
	URL string
	// Tier is the engine that finally succeeded. Empty if none did.
	Tier string
	// Result is the successful fetch result. Nil if every tier failed.
	Result *engine.Result
	// LastResult is the most recent non-nil result, used as a fallback for
	// persistence when Result is nil (so failed routing still produces a row).
	LastResult *engine.Result
	// Attempts records every tier that was tried, in order.
	Attempts []Attempt
	// PreferredTier is the cache-suggested tier that reordered the ladder
	// for this fetch, or "" if the cache had no opinion. Surface-only —
	// callers can use this to measure how often learning is firing.
	PreferredTier string
	// FromCache is true when the Result was reconstructed from the content
	// cache rather than a live engine fetch. Set only when a content-cache
	// is attached and returns a hit. Surface-only; downstream consumers use
	// it to distinguish cache-served rows from live ones.
	FromCache bool
}

// fetchWithProxyRotation fetches through the given engine, retrying with
// rotated proxies when the response status matches rotateCodes. Returns the
// final Result and validity check. On network-level fetch failure (no Result),
// returns a nil Result with the error — the caller handles escalation.
func (r *Router) fetchWithProxyRotation(
	ctx context.Context,
	e engine.Engine,
	req engine.Request,
	host string,
) (*engine.Result, validity.Result, error) {
	maxAttempts := 1 + r.rotateMax // 1 initial + N rotations
	if r.rotator == nil || len(r.rotateCodes) == 0 {
		maxAttempts = 1
	}

	var lastRes *engine.Result
	var lastVR validity.Result

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return lastRes, lastVR, ctx.Err()
		}

		res, err := e.Fetch(ctx, req)
		if err != nil {
			// Network-level failure — no status code to check for rotation.
			return nil, validity.Result{}, err
		}

		vr := r.checker.Check(validity.Page{
			URL:         req.URL,
			StatusCode:  res.StatusCode,
			ContentType: res.ContentType,
			Body:        res.Body,
		})

		lastRes = res
		lastVR = vr

		if vr.Valid {
			return res, vr, nil
		}

		// Check if this status code warrants a proxy rotation retry.
		if r.rotateCodes[res.StatusCode] {
			if attempt < maxAttempts-1 && r.rotator.Rotate(host) {
				log.Debug().
					Str("tier", e.Name()).
					Str("host", host).
					Int("status", res.StatusCode).
					Int("attempt", attempt+1).
					Int("max", maxAttempts).
					Msg("rotating proxy and retrying")
				continue
			}
			// All rotation retries exhausted (or pool can't rotate).
			// Force Escalate=true so the router tries the next tier —
			// a different tier may use a different proxy or no proxy at
			// all. Without this, a 403 (normally non-escalatable) would
			// kill the row even though chromium might succeed.
			lastVR.Escalate = true
		}
		break
	}
	return lastRes, lastVR, nil
}

// Attempt is a single engine's outcome during routing.
type Attempt struct {
	Tier   string
	Result *engine.Result
	Valid  bool
	Reason string
	Err    error
	// FromCache is true when this attempt was served from the content
	// cache instead of a live engine fetch. Useful for per-tier stats
	// aggregation to distinguish cache-served rows from live ones.
	FromCache bool
}
