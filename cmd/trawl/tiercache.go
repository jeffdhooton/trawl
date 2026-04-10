package main

import (
	"errors"
	"os"

	"github.com/jeffdhooton/trawl/internal/tierlearn"
	"github.com/rs/zerolog/log"
)

// openTierCache returns a tierlearn.Cache according to the CLI flags and
// defaults. The contract: callers ALWAYS get a non-nil Cache — worst case
// they get NopCache, never an error that aborts the job. This matches the
// design goal that tier learning is best-effort infrastructure that should
// never block a scrape.
//
// Resolution order:
//  1. If disabled is true (--no-tier-learning), return NopCache.
//  2. Resolve path: explicit overridePath, else $TRAWL_HOME/tier-cache.
//  3. Attempt to Open. If another trawl process holds the lock, log an
//     info-level message and fall back to NopCache — parallel runs
//     coexist, only the lock-holder learns.
//  4. Any other Open error also degrades to NopCache with a warn-level
//     log, because an opaque badger error should not kill a batch.
func openTierCache(disabled bool, overridePath string) (tierlearn.Cache, string, error) {
	if disabled {
		return tierlearn.NopCache{}, "", nil
	}
	path := overridePath
	if path == "" {
		p, err := defaultTierCachePath()
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

