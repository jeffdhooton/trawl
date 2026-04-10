// Package stats aggregates a job's results in memory so a runtime-agnostic
// stats.json artifact can be produced at shutdown.
//
// The aggregator is thread-safe and intended to be called once per record
// from the worker loop. Derived numbers (reachable count, chromium
// escalation rate) are computed at snapshot time from the raw counters.
package stats

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/jeffdhooton/trawl/internal/failure"
)

// Collector accumulates per-record statistics during a run.
type Collector struct {
	mu      sync.Mutex
	startAt time.Time

	total            int
	byTier           map[string]int
	byCategory       map[failure.Category]int
	durationMSByTier map[string]durStats

	// Hybrid-discovery counters. Only incremented when the worker actually
	// attempts a fallback (i.e. primary failed with a trigger category AND
	// the seed row had a fallback URL). Used to measure whether the hybrid
	// path is paying its cost.
	fallbackAttempted   int
	fallbackSucceeded   int
	fallbackNoLink      int
	fallbackUnreachable int
}

type durStats struct {
	Count int
	Sum   int64 // milliseconds
	Min   int64
	Max   int64
}

// New creates a Collector marked as started "now."
func New() *Collector {
	return &Collector{
		startAt:          time.Now().UTC(),
		byTier:           make(map[string]int),
		byCategory:       make(map[failure.Category]int),
		durationMSByTier: make(map[string]durStats),
	}
}

// Record adds one observation. category is the classified failure bucket
// (use failure.CatSuccess for successes). tier is the engine that served
// the final response ("" is tolerated for terminal failures). durationMS
// is the fetch duration; pass 0 if not applicable.
func (c *Collector) Record(category failure.Category, tier string, durationMS int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.total++
	c.byCategory[category]++

	if tier != "" {
		c.byTier[tier]++
	}
	if category == failure.CatSuccess && tier != "" && durationMS > 0 {
		ds := c.durationMSByTier[tier]
		ds.Count++
		ds.Sum += durationMS
		if ds.Min == 0 || durationMS < ds.Min {
			ds.Min = durationMS
		}
		if durationMS > ds.Max {
			ds.Max = durationMS
		}
		c.durationMSByTier[tier] = ds
	}
}

// RecordFallback records the outcome of a fallback attempt. Call once per
// attempt, after the primary has been classified unreachable and the
// hybrid path has decided to try the fallback URL.
//
// Outcomes:
//   - "succeeded"    fallback resolved + fetched into a reachable record
//   - "no_link"      prefetch succeeded but selector matched nothing
//   - "unreachable"  fallback fetch itself failed (any category)
func (c *Collector) RecordFallback(outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.fallbackAttempted++
	switch outcome {
	case "succeeded":
		c.fallbackSucceeded++
	case "no_link":
		c.fallbackNoLink++
	case "unreachable":
		c.fallbackUnreachable++
	}
}

// Snapshot produces a stable JSON-friendly view. Safe to call mid-run.
func (c *Collector) Snapshot(jobID string) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	reachable := 0
	for cat, n := range c.byCategory {
		if cat.IsReachable() {
			reachable += n
		}
	}

	// byTier counts every record that touched a tier, INCLUDING failures.
	// Useful for a "routing attempts" view but NOT for the decision rule.
	tiersAttempted := map[string]int{}
	for k, v := range c.byTier {
		tiersAttempted[k] = v
	}

	// Successful fetches per tier — this is what the decision rule uses.
	// Pulled from durationMSByTier which only increments on success.
	tiersSucceeded := map[string]int{}
	for tier, ds := range c.durationMSByTier {
		tiersSucceeded[tier] = ds.Count
	}

	cats := map[string]int{}
	for _, cat := range failure.AllCategories() {
		if n, ok := c.byCategory[cat]; ok {
			cats[string(cat)] = n
		}
	}
	// Also surface any categories we observed that aren't in AllCategories()
	// (empty strings, future additions) so nothing silently vanishes.
	for cat, n := range c.byCategory {
		if _, known := cats[string(cat)]; !known {
			key := string(cat)
			if key == "" {
				key = "uncategorized"
			}
			cats[key] = n
		}
	}

	avgByTier := map[string]TierLatency{}
	for tier, ds := range c.durationMSByTier {
		if ds.Count == 0 {
			continue
		}
		avgByTier[tier] = TierLatency{
			Count: ds.Count,
			AvgMS: ds.Sum / int64(ds.Count),
			MinMS: ds.Min,
			MaxMS: ds.Max,
		}
	}

	wallClock := time.Since(c.startAt)

	// Chromium escalation rate: of URLs that actually loaded successfully,
	// what fraction needed Chromium? Numerator is SUCCESSFUL chromium
	// fetches (not attempts), denominator is successful fetches total.
	var chromiumRate float64
	if reachable > 0 {
		chromiumRate = float64(tiersSucceeded["chromium"]) / float64(reachable)
	}

	return Snapshot{
		JobID:                  jobID,
		StartedAt:              c.startAt,
		FinishedAt:             time.Now().UTC(),
		WallClockMS:            wallClock.Milliseconds(),
		Total:                  c.total,
		Reachable:              reachable,
		Unreachable:            c.total - reachable,
		TierSucceeded:          tiersSucceeded,
		TierAttempted:          tiersAttempted,
		FailuresByCategory:     cats,
		TierLatency:            avgByTier,
		ChromiumEscalationRate: chromiumRate,
		Fallback: FallbackStats{
			Attempted:   c.fallbackAttempted,
			Succeeded:   c.fallbackSucceeded,
			NoLink:      c.fallbackNoLink,
			Unreachable: c.fallbackUnreachable,
		},
	}
}

// Snapshot is the JSON shape written to stats.json.
type Snapshot struct {
	JobID       string    `json:"job_id"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	WallClockMS int64     `json:"wall_clock_ms"`
	Total       int       `json:"total"`
	Reachable   int       `json:"reachable"`
	Unreachable int       `json:"unreachable"`
	// TierSucceeded counts successful fetches per engine. This is the
	// numerator source for the Lightpanda decision rule in BENCHMARK.md.
	TierSucceeded map[string]int `json:"tier_succeeded"`
	// TierAttempted counts every record that touched a tier, including
	// failures. Useful for "how much work did each engine do" but NOT
	// for the escalation rate calculation.
	TierAttempted          map[string]int         `json:"tier_attempted"`
	FailuresByCategory     map[string]int         `json:"failures_by_category"`
	TierLatency            map[string]TierLatency `json:"tier_latency_ms"`
	ChromiumEscalationRate float64                `json:"chromium_escalation_rate"`
	// Fallback records how hybrid discovery performed. Zero values when
	// no seed row had a fallback URL configured.
	Fallback FallbackStats `json:"fallback"`
}

// TierLatency holds aggregate timings for a single engine tier.
type TierLatency struct {
	Count int   `json:"count"`
	AvgMS int64 `json:"avg_ms"`
	MinMS int64 `json:"min_ms"`
	MaxMS int64 `json:"max_ms"`
}

// FallbackStats summarizes the hybrid-discovery path in a run.
//
//   - Attempted:   rows whose primary failed with a trigger category AND
//     carried a fallback URL from the seed
//   - Succeeded:   rows where the fallback fetch produced a reachable record
//   - NoLink:      fallback prefetch succeeded but --fallback-selector matched
//     nothing
//   - Unreachable: fallback fetch itself failed (any category)
//
// Attempted == Succeeded + NoLink + Unreachable.
type FallbackStats struct {
	Attempted   int `json:"attempted"`
	Succeeded   int `json:"succeeded"`
	NoLink      int `json:"no_link"`
	Unreachable int `json:"unreachable"`
}

// WriteJSON serializes a snapshot to a file path.
func WriteJSON(path string, s Snapshot) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
