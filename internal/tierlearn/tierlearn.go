// Package tierlearn provides a persistent host → preferred-tier cache that
// the router consults before escalation and updates after a successful
// fetch.
//
// Learning model: single-observation, most-recent-successful-tier wins.
// Every successful fetch overwrites the host's preference with whichever
// tier served it. This is responsive (one successful HTTP fetch on a
// previously-chromium host immediately flips the preference) but has a
// limitation — if the router's learned preference steers all future
// fetches to chromium, HTTP is never tried on that host again and the
// cache can't self-correct if the site migrates to server-side rendering.
// In practice SaaS sites don't migrate SPA→SSR often enough for this to
// matter; users who hit the edge case can delete the cache file.
//
// The cache is persistent across jobs (one trawl batch's learning
// benefits the next) and safe for concurrent use by many workers within
// a single process. It is NOT safe for concurrent use by multiple trawl
// processes at once — Badger holds an exclusive lock on its directory.
// If Open fails because another process holds the lock, callers should
// fall back to NopCache rather than aborting, so parallel runs still
// make progress (only one of them learns).
package tierlearn

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// Cache is the minimal interface the router depends on. Implementations
// must be safe for concurrent use.
type Cache interface {
	// Preferred returns the tier name to try first for host, or "" if the
	// cache has no opinion (caller uses the router's default ladder).
	Preferred(host string) string
	// Observe records a successful fetch: host was served by tier. Failed
	// fetches should not be passed to Observe — they don't teach us
	// anything about which tier is best. Idempotent.
	Observe(host, tier string)
	// Close releases any underlying resources. Safe to call on NopCache.
	Close() error
}

// Entry is the on-disk value shape. Exposed so tests and observability
// tooling can decode cache files.
type Entry struct {
	Tier      string    `json:"tier"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NopCache is a Cache that never learns anything. Useful when tier
// learning is explicitly disabled via --no-tier-learning and as the
// fallback when a real cache fails to open.
type NopCache struct{}

func (NopCache) Preferred(string) string    { return "" }
func (NopCache) Observe(string, string)     {}
func (NopCache) Close() error               { return nil }

// BadgerCache is a persistent Cache backed by BadgerDB.
type BadgerCache struct {
	db *badger.DB

	// writeCh serializes Observe calls so each host update is a single
	// write transaction without callers blocking on Badger mutexes. In
	// practice Badger is fast enough that this channel barely fills, but
	// it decouples the router from fsync latency.
	mu sync.RWMutex
}

// ErrLocked is returned by Open when Badger reports the directory is
// already in use by another process. Callers should treat this as a
// signal to fall back to NopCache rather than aborting.
var ErrLocked = errors.New("tierlearn: cache directory is locked by another process")

// Open opens (or creates) a cache at the given directory. If the
// directory is locked by another process, Open returns ErrLocked and
// the caller should fall back to NopCache.
func Open(dir string) (*BadgerCache, error) {
	opts := badger.DefaultOptions(dir).
		WithLogger(badgerNoopLogger{}).
		WithSyncWrites(false)

	db, err := badger.Open(opts)
	if err != nil {
		// Badger reports "Another process is using this Badger database"
		// via errors.Is on a sentinel exported in recent versions; older
		// versions only surface the text. Match on the substring to stay
		// compatible across minor revs.
		if isLockError(err) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("badger open: %w", err)
	}
	return &BadgerCache{db: db}, nil
}

// Close flushes and closes the underlying database.
func (c *BadgerCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Preferred returns the tier name to try first for host, or "" if unknown.
// Host lookups that fail (not found, read error) silently return "" — the
// cache is advisory and must never block the router on transient errors.
func (c *BadgerCache) Preferred(host string) string {
	if host == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	var tier string
	_ = c.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(hostKey(host))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			var e Entry
			if jerr := json.Unmarshal(v, &e); jerr != nil {
				return jerr
			}
			tier = e.Tier
			return nil
		})
	})
	return tier
}

// Observe records that host was successfully served by tier. Pass tier=""
// is a no-op (failed fetches do not teach). Concurrent calls from many
// workers are safe.
func (c *BadgerCache) Observe(host, tier string) {
	if host == "" || tier == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := Entry{Tier: tier, UpdatedAt: time.Now().UTC()}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_ = c.db.Update(func(txn *badger.Txn) error {
		return txn.Set(hostKey(host), data)
	})
}

// Hosts returns every host currently in the cache with its recorded
// preference. Intended for diagnostics (e.g. a future "trawl cache dump"
// subcommand) — the router itself never calls this.
func (c *BadgerCache) Hosts() (map[string]Entry, error) {
	out := map[string]Entry{}
	err := c.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(hostPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			host := string(it.Item().Key()[len(hostPrefix):])
			err := it.Item().Value(func(v []byte) error {
				var e Entry
				if jerr := json.Unmarshal(v, &e); jerr != nil {
					return jerr
				}
				out[host] = e
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

const hostPrefix = "host:"

func hostKey(host string) []byte {
	return []byte(hostPrefix + host)
}

func isLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Badger emits variants like "Another process is using this Badger
	// database" and "cannot acquire directory lock". Match both.
	for _, needle := range []string{
		"Another process is using",
		"cannot acquire directory lock",
		"resource temporarily unavailable",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// badgerNoopLogger silences Badger's chatty INFO logs. Same helper as the
// frontier package — duplicated because exporting it would be a weird
// cross-package dependency for three empty methods.
type badgerNoopLogger struct{}

func (badgerNoopLogger) Errorf(string, ...any)   {}
func (badgerNoopLogger) Warningf(string, ...any) {}
func (badgerNoopLogger) Infof(string, ...any)    {}
func (badgerNoopLogger) Debugf(string, ...any)   {}
