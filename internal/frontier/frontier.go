// Package frontier is the persistent URL queue.
//
// It owns three concerns: what URLs to fetch next (FIFO ordering), what URLs
// have already been seen (exact-match dedup), and what state each URL is in
// (queued, in_flight, done, failed). It is backed by BadgerDB so state
// survives crashes and can be resumed.
//
// The frontier is safe for concurrent use by multiple workers. Enqueue and
// Next are synchronized under a mutex; the expensive work (the actual fetch)
// happens outside the lock.
package frontier

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/jeffdhooton/trawl/internal/canonical"
)

// State is the lifecycle of a URL in the frontier.
type State string

const (
	StateQueued   State = "queued"
	StateInFlight State = "in_flight"
	StateDone     State = "done"
	StateFailed   State = "failed"
)

// Record is what the frontier stores per URL.
type Record struct {
	URL        string    `json:"url"`
	State      State     `json:"state"`
	Attempts   int       `json:"attempts"`
	LastTier   string    `json:"last_tier,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Seq is the FIFO position. Only meaningful while State == StateQueued.
	Seq uint64 `json:"seq"`
	// Fallback is an optional sidecar URL used by hybrid discovery: if the
	// primary URL returns an unreachable category (http_4xx, dns_failure)
	// the worker can re-route through Fallback + --fallback-selector to
	// recover the pricing page from a live homepage. Stored verbatim from
	// the seed row — not canonicalized, not deduped.
	Fallback string `json:"fallback,omitempty"`
}

// Stats is a lightweight snapshot of frontier counts.
type Stats struct {
	Queued   int `json:"queued"`
	InFlight int `json:"in_flight"`
	Done     int `json:"done"`
	Failed   int `json:"failed"`
	Total    int `json:"total"`
}

// ErrEmpty is returned from Next when no work remains.
var ErrEmpty = errors.New("frontier: empty")

const (
	// Key layout:
	//   url:<canonical>        -> JSON Record
	//   queue:<seq>            -> canonical URL (pointer into url:)
	//   meta:next_seq          -> uint64 (8 bytes BE)
	prefixURL   = "url:"
	prefixQueue = "queue:"
	keyNextSeq  = "meta:next_seq"
)

// Frontier is the persistent URL queue.
type Frontier struct {
	db *badger.DB

	mu      sync.Mutex
	nextSeq uint64
}

// Open opens (or creates) a frontier at the given directory.
func Open(dir string) (*Frontier, error) {
	opts := badger.DefaultOptions(dir).
		WithLogger(badgerNoopLogger{}).
		WithSyncWrites(false)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("badger open: %w", err)
	}

	f := &Frontier{db: db}
	if err := f.loadNextSeq(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load seq: %w", err)
	}
	return f, nil
}

// Close flushes and closes the underlying database.
func (f *Frontier) Close() error {
	return f.db.Close()
}

// Enqueue adds a URL to the frontier if it has not been seen before.
// It canonicalizes the URL first and returns (canonicalURL, added, error).
// A URL that already exists in any state returns added=false.
func (f *Frontier) Enqueue(rawURL string) (canonURL string, added bool, err error) {
	return f.EnqueueWithFallback(rawURL, "")
}

// EnqueueWithFallback adds a URL to the frontier with an optional fallback
// URL stored as sidecar metadata. The fallback is NOT canonicalized or
// deduplicated — it's a per-row hint that the worker uses to retry on
// specific unreachable categories. Dedup is still driven by the canonical
// form of rawURL.
func (f *Frontier) EnqueueWithFallback(rawURL, fallback string) (canonURL string, added bool, err error) {
	canonURL, err = canonical.Canonicalize(rawURL, canonical.Options{})
	if err != nil {
		return "", false, fmt.Errorf("canonicalize: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	err = f.db.Update(func(txn *badger.Txn) error {
		urlKey := []byte(prefixURL + canonURL)
		if _, gerr := txn.Get(urlKey); gerr == nil {
			return nil // already exists
		} else if !errors.Is(gerr, badger.ErrKeyNotFound) {
			return gerr
		}

		seq := f.nextSeq
		f.nextSeq++

		rec := Record{
			URL:        canonURL,
			State:      StateQueued,
			EnqueuedAt: time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
			Seq:        seq,
			Fallback:   fallback,
		}
		if err := putJSON(txn, urlKey, rec); err != nil {
			return err
		}
		if err := txn.Set(queueKey(seq), []byte(canonURL)); err != nil {
			return err
		}
		if err := putNextSeq(txn, f.nextSeq); err != nil {
			return err
		}
		added = true
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return canonURL, added, nil
}

// Next atomically claims the next queued URL, transitioning it to in_flight.
// Returns ErrEmpty if nothing is queued.
func (f *Frontier) Next() (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out Record
	err := f.db.Update(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := []byte(prefixQueue)
		it.Seek(prefix)
		if !it.ValidForPrefix(prefix) {
			return ErrEmpty
		}

		queueK := it.Item().KeyCopy(nil)
		var canonURL string
		if err := it.Item().Value(func(v []byte) error {
			canonURL = string(v)
			return nil
		}); err != nil {
			return err
		}

		urlKey := []byte(prefixURL + canonURL)
		item, err := txn.Get(urlKey)
		if err != nil {
			// queue pointer without url record — drop and continue not worth it for P0; fail loud.
			return fmt.Errorf("dangling queue pointer for %q: %w", canonURL, err)
		}
		var rec Record
		if err := readJSON(item, &rec); err != nil {
			return err
		}

		rec.State = StateInFlight
		rec.Attempts++
		rec.UpdatedAt = time.Now().UTC()

		if err := putJSON(txn, urlKey, rec); err != nil {
			return err
		}
		if err := txn.Delete(queueK); err != nil {
			return err
		}
		out = rec
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return out, nil
}

// MarkDone transitions a URL to the done state.
func (f *Frontier) MarkDone(canonURL, tier string) error {
	return f.updateRecord(canonURL, func(r *Record) {
		r.State = StateDone
		r.LastTier = tier
		r.LastError = ""
		r.UpdatedAt = time.Now().UTC()
	})
}

// MarkFailed transitions a URL to the failed state with an error message.
func (f *Frontier) MarkFailed(canonURL, tier string, fetchErr error) error {
	return f.updateRecord(canonURL, func(r *Record) {
		r.State = StateFailed
		r.LastTier = tier
		if fetchErr != nil {
			r.LastError = fetchErr.Error()
		}
		r.UpdatedAt = time.Now().UTC()
	})
}

// Recover re-queues any URLs that were in_flight when the process died.
// Call this once on startup before spawning workers.
func (f *Frontier) Recover() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var recovered int
	err := f.db.Update(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := []byte(prefixURL)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var rec Record
			if err := readJSON(it.Item(), &rec); err != nil {
				return err
			}
			if rec.State != StateInFlight {
				continue
			}

			rec.State = StateQueued
			rec.Seq = f.nextSeq
			rec.UpdatedAt = time.Now().UTC()
			f.nextSeq++

			urlKey := []byte(prefixURL + rec.URL)
			if err := putJSON(txn, urlKey, rec); err != nil {
				return err
			}
			if err := txn.Set(queueKey(rec.Seq), []byte(rec.URL)); err != nil {
				return err
			}
			recovered++
		}

		if recovered > 0 {
			if err := putNextSeq(txn, f.nextSeq); err != nil {
				return err
			}
		}
		return nil
	})
	return recovered, err
}

// Stats returns a snapshot of the current counts by state.
func (f *Frontier) Stats() (Stats, error) {
	var s Stats
	err := f.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := []byte(prefixURL)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var rec Record
			if err := readJSON(it.Item(), &rec); err != nil {
				return err
			}
			s.Total++
			switch rec.State {
			case StateQueued:
				s.Queued++
			case StateInFlight:
				s.InFlight++
			case StateDone:
				s.Done++
			case StateFailed:
				s.Failed++
			}
		}
		return nil
	})
	return s, err
}

// Get returns the record for a canonicalized URL, or ErrKeyNotFound.
func (f *Frontier) Get(canonURL string) (Record, error) {
	var rec Record
	err := f.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixURL + canonURL))
		if err != nil {
			return err
		}
		return readJSON(item, &rec)
	})
	return rec, err
}

func (f *Frontier) updateRecord(canonURL string, mutate func(*Record)) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.db.Update(func(txn *badger.Txn) error {
		urlKey := []byte(prefixURL + canonURL)
		item, err := txn.Get(urlKey)
		if err != nil {
			return err
		}
		var rec Record
		if err := readJSON(item, &rec); err != nil {
			return err
		}
		mutate(&rec)
		return putJSON(txn, urlKey, rec)
	})
}

func (f *Frontier) loadNextSeq() error {
	return f.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(keyNextSeq))
		if errors.Is(err, badger.ErrKeyNotFound) {
			f.nextSeq = 0
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			if len(v) != 8 {
				return fmt.Errorf("next_seq: expected 8 bytes, got %d", len(v))
			}
			f.nextSeq = binary.BigEndian.Uint64(v)
			return nil
		})
	})
}

func queueKey(seq uint64) []byte {
	buf := make([]byte, len(prefixQueue)+8)
	copy(buf, prefixQueue)
	binary.BigEndian.PutUint64(buf[len(prefixQueue):], seq)
	return buf
}

func putNextSeq(txn *badger.Txn, seq uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, seq)
	return txn.Set([]byte(keyNextSeq), buf)
}

func putJSON(txn *badger.Txn, key []byte, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return txn.Set(key, data)
}

func readJSON(item *badger.Item, v any) error {
	return item.Value(func(data []byte) error {
		return json.Unmarshal(data, v)
	})
}

// badgerNoopLogger silences badger's chatty INFO logs.
type badgerNoopLogger struct{}

func (badgerNoopLogger) Errorf(string, ...any)   {}
func (badgerNoopLogger) Warningf(string, ...any) {}
func (badgerNoopLogger) Infof(string, ...any)    {}
func (badgerNoopLogger) Debugf(string, ...any)   {}

