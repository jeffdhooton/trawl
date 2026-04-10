// Package router implements the tiered engine router. It escalates each URL
// through a configured list of engines (cheap → expensive) until one returns
// a result that the validity checker accepts.
//
// P1 stage 1 is stateless: every URL starts at the cheapest tier. Stage 4
// will add per-domain learned tier preferences stored in BadgerDB so a known
// SPA domain can skip the HTTP tier on its second page.
package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/validity"
)

// Router routes fetches through a tiered set of engines.
type Router struct {
	engines []engine.Engine
	checker validity.Checker
}

// New constructs a Router. Engines should be ordered cheap → expensive.
func New(engines []engine.Engine, checker validity.Checker) (*Router, error) {
	if len(engines) == 0 {
		return nil, errors.New("router: at least one engine required")
	}
	if checker == nil {
		checker = validity.NewChecker(validity.Default())
	}
	return &Router{engines: engines, checker: checker}, nil
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
//   - If an engine's fetch fails (network error, context cancel), the router
//     captures the error and tries the next tier.
//   - If an engine's fetch succeeds but validity says Valid=false and
//     Escalate=true, the router tries the next tier.
//   - If validity says Escalate=false (e.g. 404, unsupported content-type),
//     the router stops immediately — no tier will help.
//   - If every tier is exhausted, the last attempted Result is returned so
//     callers can still persist the evidence, along with a non-nil error.
func (r *Router) Route(ctx context.Context, req engine.Request) (*Outcome, error) {
	outcome := &Outcome{URL: req.URL}

	for _, e := range r.engines {
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
}

// Attempt is a single engine's outcome during routing.
type Attempt struct {
	Tier   string
	Result *engine.Result
	Valid  bool
	Reason string
	Err    error
}
