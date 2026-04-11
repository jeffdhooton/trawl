package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jeffdhooton/trawl/internal/cache"
	"github.com/rs/zerolog/log"
)

// defaultContentCachePath returns <trawl-root>/content-cache, the
// default location for the cross-job content cache.
func defaultContentCachePath() (string, error) {
	root, err := trawlRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "content-cache"), nil
}

// openContentCache mirrors openTierCache's error-tolerant contract:
// callers always get a non-nil cache.Cache, worst case NopCache. A nil
// ttl or <=0 means "cache forever" for that run. The second return is
// the resolved path (empty if the cache is disabled or fell back).
//
// Resolution order:
//  1. If enabled is false (--cache not set), return NopCache.
//  2. Resolve path: explicit overridePath, else $TRAWL_HOME/content-cache.
//  3. Attempt to Open. If another trawl process holds the lock, log an
//     info-level message and fall back to NopCache — parallel runs
//     coexist, only the lock-holder writes.
//  4. Any other Open error also degrades to NopCache with a warn-level
//     log, because an opaque badger error should not kill a scrape.
func openContentCache(enabled bool, overridePath string, ttl time.Duration) (cache.Cache, string, error) {
	if !enabled {
		return cache.NopCache{}, "", nil
	}
	path := overridePath
	if path == "" {
		p, err := defaultContentCachePath()
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
