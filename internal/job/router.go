package job

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jeffdhooton/trawl/internal/cache"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/jeffdhooton/trawl/internal/tierlearn"
	"github.com/jeffdhooton/trawl/internal/validity"
	"github.com/rs/zerolog/log"
)

// buildRouter constructs a router from tier names. Supported names:
//
//	http       — always available
//	chromium   — requires a local Chrome/Chromium binary on PATH
//
// If forceTier is non-empty, only that engine is used (no escalation).
// chromiumCfg seeds the chromium engine when that tier is in the
// list; the http UA from httpCfg is copied in unless chromiumCfg
// already overrides it.
func buildRouter(tiers []string, forceTier string, httpCfg engine.HTTPConfig, chromiumCfg engine.ChromiumConfig) (*router.Router, error) {
	var names []string
	if forceTier != "" {
		names = []string{forceTier}
	} else {
		names = tiers
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no tiers configured")
	}

	var engines []engine.Engine
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch name {
		case "http":
			engines = append(engines, engine.NewHTTP(httpCfg))
		case "chromium":
			ccfg := chromiumCfg
			if ccfg.NavigationTimeout == 0 {
				defaults := engine.DefaultChromiumConfig()
				if ccfg.NavigationTimeout == 0 {
					ccfg.NavigationTimeout = defaults.NavigationTimeout
				}
				if ccfg.WaitAfterLoad == 0 {
					ccfg.WaitAfterLoad = defaults.WaitAfterLoad
				}
				if !ccfg.Headless {
					ccfg.Headless = defaults.Headless
				}
			}
			if ccfg.UserAgent == "" {
				ccfg.UserAgent = httpCfg.UserAgent
			}
			engines = append(engines, engine.NewChromium(ccfg))
		default:
			return nil, fmt.Errorf("unknown tier %q (supported: http, chromium)", name)
		}
	}

	return router.New(engines, validity.NewChecker(validity.Default()))
}

// parseTierList splits a comma-separated tier string like "http,chromium".
func parseTierList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// openTierCache returns a tierlearn.Cache according to the CLI flags
// and defaults. The contract: callers ALWAYS get a non-nil Cache —
// worst case they get NopCache, never an error that aborts the job.
// Tier learning is best-effort infrastructure that should never block
// a scrape.
//
// Resolution order:
//  1. If disabled is true (--no-tier-learning), return NopCache.
//  2. Resolve path: explicit overridePath, else $TRAWL_HOME/tier-cache.
//  3. Attempt to Open. ErrLocked → another process holds the lock →
//     fall back to NopCache (parallel runs coexist, only the
//     lock-holder learns).
//  4. Any other Open error also degrades to NopCache with a warn-
//     level log — an opaque badger error must not kill a batch.
func openTierCache(disabled bool, overridePath string) (tierlearn.Cache, string, error) {
	if disabled {
		return tierlearn.NopCache{}, "", nil
	}
	path := overridePath
	if path == "" {
		p, err := DefaultTierCachePath()
		if err != nil {
			log.Warn().Err(err).Msg("could not resolve default tier-cache path, disabling learning")
			return tierlearn.NopCache{}, "", nil
		}
		path = p
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("could not create tier-cache dir, disabling learning")
		return tierlearn.NopCache{}, "", nil
	}
	c, err := tierlearn.Open(path)
	if err != nil {
		if errors.Is(err, tierlearn.ErrLocked) {
			log.Info().
				Str("path", path).
				Msg("tier-cache locked by another trawl process, this run will not learn")
			return tierlearn.NopCache{}, path, nil
		}
		log.Warn().Err(err).Str("path", path).Msg("could not open tier-cache, disabling learning")
		return tierlearn.NopCache{}, path, nil
	}
	return c, path, nil
}

// openContentCache mirrors openTierCache's error-tolerant contract.
// A nil ttl or <=0 means "cache forever" for that run.
func openContentCache(enabled bool, overridePath string, ttl time.Duration) (cache.Cache, string, error) {
	if !enabled {
		return cache.NopCache{}, "", nil
	}
	path := overridePath
	if path == "" {
		p, err := DefaultContentCachePath()
		if err != nil {
			log.Warn().Err(err).Msg("could not resolve default content-cache path, disabling cache")
			return cache.NopCache{}, "", nil
		}
		path = p
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("could not create content-cache dir, disabling cache")
		return cache.NopCache{}, "", nil
	}
	c, err := cache.Open(path, cache.Config{TTL: ttl})
	if err != nil {
		if errors.Is(err, cache.ErrLocked) {
			log.Info().
				Str("path", path).
				Msg("content-cache locked by another trawl process, this run will not cache")
			return cache.NopCache{}, path, nil
		}
		log.Warn().Err(err).Str("path", path).Msg("could not open content-cache, disabling cache")
		return cache.NopCache{}, path, nil
	}
	return c, path, nil
}

// parseCacheTTL translates the config string ("24h", "0", "") into a
// time.Duration. "0" or empty means cache-forever (zero Duration). Any
// parse error falls back to the default so a typo doesn't kill the run.
func parseCacheTTL(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Warn().Str("ttl", s).Err(err).Msg("invalid --cache-ttl, using default")
		return fallback
	}
	return d
}
