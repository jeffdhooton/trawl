package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestScrapeCacheRoundTrip verifies the full opt-in cache flow:
// first scrape fetches live, second scrape with the same cache hits
// without touching the server, and the second record carries
// metadata.from_cache=true.
func TestScrapeCacheRoundTrip(t *testing.T) {
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1>cached content</h1>
			<p>` + strings.Repeat("content ", 200) + `</p>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	outA := filepath.Join(trawlHome, "a.jsonl")
	outB := filepath.Join(trawlHome, "b.jsonl")
	cachePath := filepath.Join(trawlHome, "content-cache")

	opts := scrapeOpts{
		outputPath:   outA,
		timeout:      5 * time.Second,
		tiers:        "http",
		cacheEnabled: true,
		cacheTTL:     time.Hour,
		cachePath:    cachePath,
	}

	// First run: live fetch, cache MISS, writes entry.
	if err := runScrape(context.Background(), srv.URL+"/page", opts); err != nil {
		t.Fatalf("first runScrape: %v", err)
	}
	recsA := readJSONL(t, outA)
	if len(recsA) != 1 {
		t.Fatalf("first run: expected 1 record, got %d", len(recsA))
	}
	if recsA[0].Metadata.FromCache {
		t.Error("first run: record marked from_cache — should be live")
	}
	if hits.Load() != 1 {
		t.Errorf("first run: server hits = %d, want 1", hits.Load())
	}

	// Second run: same URL, same cache path. Server must NOT be hit again.
	opts.outputPath = outB
	if err := runScrape(context.Background(), srv.URL+"/page", opts); err != nil {
		t.Fatalf("second runScrape: %v", err)
	}
	recsB := readJSONL(t, outB)
	if len(recsB) != 1 {
		t.Fatalf("second run: expected 1 record, got %d", len(recsB))
	}
	if !recsB[0].Metadata.FromCache {
		t.Error("second run: record not marked from_cache — expected a hit")
	}
	if hits.Load() != 1 {
		t.Errorf("second run: server hits = %d (should still be 1 — cache hit)", hits.Load())
	}
	// Body hash should match so downstream consumers see identical evidence.
	if recsA[0].ContentHash != recsB[0].ContentHash {
		t.Errorf("content hashes diverged: live=%s cached=%s", recsA[0].ContentHash, recsB[0].ContentHash)
	}
}

// TestScrapeCacheTTLExpiry verifies that a too-short TTL forces a
// re-fetch on the second run even with caching enabled.
func TestScrapeCacheTTLExpiry(t *testing.T) {
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1>expiring content</h1>
			<p>` + strings.Repeat("content ", 200) + `</p>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	cachePath := filepath.Join(trawlHome, "content-cache")

	opts := scrapeOpts{
		outputPath:   filepath.Join(trawlHome, "a.jsonl"),
		timeout:      5 * time.Second,
		tiers:        "http",
		cacheEnabled: true,
		cacheTTL:     50 * time.Millisecond,
		cachePath:    cachePath,
	}

	if err := runScrape(context.Background(), srv.URL+"/page", opts); err != nil {
		t.Fatalf("first runScrape: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	opts.outputPath = filepath.Join(trawlHome, "b.jsonl")
	if err := runScrape(context.Background(), srv.URL+"/page", opts); err != nil {
		t.Fatalf("second runScrape: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("expected 2 live hits after TTL expiry, got %d", hits.Load())
	}
	recsB := readJSONL(t, filepath.Join(trawlHome, "b.jsonl"))
	if recsB[0].Metadata.FromCache {
		t.Error("second run after expiry marked from_cache — should have re-fetched")
	}
}

// TestScrapeCacheOffByDefault verifies the opt-in contract: without
// --cache, a second run hits the server again (no caching).
func TestScrapeCacheOffByDefault(t *testing.T) {
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1>no cache</h1>
			<p>` + strings.Repeat("content ", 200) + `</p>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	opts := scrapeOpts{
		outputPath: filepath.Join(trawlHome, "a.jsonl"),
		timeout:    5 * time.Second,
		tiers:      "http",
		// cacheEnabled intentionally false
	}
	if err := runScrape(context.Background(), srv.URL+"/page", opts); err != nil {
		t.Fatalf("first runScrape: %v", err)
	}
	opts.outputPath = filepath.Join(trawlHome, "b.jsonl")
	if err := runScrape(context.Background(), srv.URL+"/page", opts); err != nil {
		t.Fatalf("second runScrape: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("expected 2 live hits when cache is off, got %d", hits.Load())
	}
}
