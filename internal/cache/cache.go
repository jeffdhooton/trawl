// Package cache is the persistent content cache that stores full
// engine.Result bodies keyed by (canonical URL, tier name). Users opt in
// via `--cache` on scrape/batch/crawl; the router then consults the
// cache before attempting each tier and serves hits under the configured
// TTL without touching the live site.
//
// Key design:
//   - Keyed on (URL, tier) so a chromium-rendered body and an HTTP-fetched
//     body for the same URL live in separate slots. This matches the
//     router's semantics: when the user forces a specific tier, only that
//     tier's cache entry is eligible.
//   - TTL applied on Get — entries older than cfg.TTL count as a miss and
//     are re-fetched + re-cached on the next Put. No background GC; stale
//     entries just sit on disk until overwritten.
//   - Nil/NopCache fallback when disabled or on open failure so the router
//     never has to special-case the disabled path.
//
// Like tierlearn, the cache is persistent across jobs (one run's work
// warms the next) and safe for concurrent use by many workers within a
// single process. It is NOT safe for concurrent use by multiple trawl
// processes at once — Badger holds an exclusive lock on its directory.
// If Open fails because another process holds the lock, callers should
// fall back to NopCache rather than aborting.
package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/jeffdhooton/trawl/internal/engine"
)

// Cache is the minimal interface the router depends on. Implementations
// must be safe for concurrent use.
type Cache interface {
	// Get returns a cached engine.Result for (url, tier) if one exists and
	// is fresher than the configured TTL. A miss (not found OR expired)
	// returns nil, false. Get MUST NOT return an error for transient
	// lookup failures — the cache is advisory and must never block the
	// router.
	Get(url, tier string) (*engine.Result, bool)
	// Put stores a fresh successful fetch. Failed fetches (every tier
	// exhausted, non-escalatable errors, etc.) MUST NOT be cached — the
	// whole point of the cache is to reuse known-good content.
	Put(url, tier string, res *engine.Result)
	// Close releases any underlying resources. Safe to call on NopCache.
	Close() error
}

// Config tunes a BadgerCache. Zero values are valid: TTL=0 means entries
// never expire (cache forever).
type Config struct {
	TTL time.Duration
}

// Entry is the on-disk value shape. Exposed so tests and observability
// tooling can decode cache files.
type Entry struct {
	URL         string              `json:"url"`
	FinalURL    string              `json:"final_url,omitempty"`
	Tier        string              `json:"tier"`
	StatusCode  int                 `json:"status_code"`
	Header      map[string][]string `json:"header,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	Body        []byte              `json:"body,omitempty"`
	DurationMS  int64               `json:"duration_ms,omitempty"`
	Redirects   []string            `json:"redirects,omitempty"`
	StoredAt    time.Time           `json:"stored_at"`
}

// NopCache is a Cache that stores nothing and never hits. Used as the
// fallback when caching is disabled and when Open fails.
type NopCache struct{}

func (NopCache) Get(string, string) (*engine.Result, bool) { return nil, false }
func (NopCache) Put(string, string, *engine.Result)        {}
func (NopCache) Close() error                              { return nil }

// BadgerCache is a persistent Cache backed by BadgerDB.
type BadgerCache struct {
	db  *badger.DB
	cfg Config

	mu sync.RWMutex
}

// ErrLocked is returned by Open when Badger reports the directory is
// already in use by another process. Callers should fall back to
// NopCache rather than aborting.
var ErrLocked = errors.New("cache: directory is locked by another process")

// Open opens (or creates) a cache at the given directory.
func Open(dir string, cfg Config) (*BadgerCache, error) {
	opts := badger.DefaultOptions(dir).
		WithLogger(badgerNoopLogger{}).
		WithSyncWrites(false)

	db, err := badger.Open(opts)
	if err != nil {
		if isLockError(err) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("badger open: %w", err)
	}
	return &BadgerCache{db: db, cfg: cfg}, nil
}

// Close flushes and closes the underlying database.
func (c *BadgerCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Get returns a cached engine.Result for (url, tier) if one exists and is
// fresher than the configured TTL.
func (c *BadgerCache) Get(url, tier string) (*engine.Result, bool) {
	if url == "" || tier == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	var entry Entry
	var found bool
	_ = c.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(entryKey(url, tier))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			if jerr := json.Unmarshal(v, &entry); jerr != nil {
				return jerr
			}
			found = true
			return nil
		})
	})
	if !found {
		return nil, false
	}
	if c.cfg.TTL > 0 && time.Since(entry.StoredAt) > c.cfg.TTL {
		return nil, false
	}
	return entryToResult(&entry), true
}

// Put stores a fresh successful fetch. Errors are silently swallowed —
// the cache is advisory and must never block the router on I/O trouble.
func (c *BadgerCache) Put(url, tier string, res *engine.Result) {
	if url == "" || tier == "" || res == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := Entry{
		URL:         url,
		FinalURL:    res.FinalURL,
		Tier:        tier,
		StatusCode:  res.StatusCode,
		Header:      headerToMap(res.Header),
		ContentType: res.ContentType,
		Body:        res.Body,
		DurationMS:  res.Duration.Milliseconds(),
		Redirects:   res.Redirects,
		StoredAt:    time.Now().UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_ = c.db.Update(func(txn *badger.Txn) error {
		return txn.Set(entryKey(url, tier), data)
	})
}

// entryToResult reconstructs an engine.Result from a stored Entry. The
// duration is recovered as int64 ms → time.Duration so downstream metrics
// stay consistent with a live fetch.
func entryToResult(e *Entry) *engine.Result {
	return &engine.Result{
		URL:         e.URL,
		FinalURL:    e.FinalURL,
		StatusCode:  e.StatusCode,
		Header:      mapToHeader(e.Header),
		ContentType: e.ContentType,
		Body:        e.Body,
		Duration:    time.Duration(e.DurationMS) * time.Millisecond,
		Redirects:   e.Redirects,
	}
}

func headerToMap(h http.Header) map[string][]string {
	if h == nil {
		return nil
	}
	return map[string][]string(h)
}

func mapToHeader(m map[string][]string) http.Header {
	if m == nil {
		return nil
	}
	return http.Header(m)
}

const entryPrefix = "entry:"

// entryKey serializes (url, tier) into a byte key. The tier goes last so
// that a future "list every tier stored for this URL" scan can use the
// URL as a prefix.
func entryKey(url, tier string) []byte {
	return []byte(entryPrefix + url + "\x00" + tier)
}

func isLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
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

// badgerNoopLogger silences Badger's chatty INFO logs.
type badgerNoopLogger struct{}

func (badgerNoopLogger) Errorf(string, ...any)   {}
func (badgerNoopLogger) Warningf(string, ...any) {}
func (badgerNoopLogger) Infof(string, ...any)    {}
func (badgerNoopLogger) Debugf(string, ...any)   {}
