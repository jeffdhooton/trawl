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

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/tierlearn"
	"github.com/jeffdhooton/trawl/internal/validity"
)

// Router routes fetches through a tiered set of engines.
type Router struct {
	engines []engine.Engine
	checker validity.Checker
	cache   tierlearn.Cache
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
	return &Router{engines: engines, checker: checker, cache: tierlearn.NopCache{}}, nil
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
		attempt := Attempt{Tier: e.Name()}

		res, err := e.Fetch(ctx, req)
		if err != nil {
			attempt.Err = err
			outcome.Attempts = append(outcome.Attempts, attempt)
			if ctx.Err() != nil {
				return outcome, ctx.Err()
			}
			continue // try next tier on transient fetch failure
		}
		attempt.Result = res
		outcome.LastResult = res

		vr := r.checker.Check(validity.Page{
			URL:         req.URL,
			StatusCode:  res.StatusCode,
			ContentType: res.ContentType,
			Body:        res.Body,
		})
		attempt.Valid = vr.Valid
		attempt.Reason = vr.Reason
		outcome.Attempts = append(outcome.Attempts, attempt)

		if vr.Valid {
			outcome.Tier = e.Name()
			outcome.Result = res
			if host != "" {
				r.cache.Observe(host, e.Name())
			}
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
}

// Attempt is a single engine's outcome during routing.
type Attempt struct {
	Tier   string
	Result *engine.Result
	Valid  bool
	Reason string
	Err    error
}
