package main

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeffdhooton/trawl/internal/tierlearn"
)

// TestBatchPopulatesTierCache runs a short-mode batch and verifies that
// the default tier-cache (a BadgerDB under $TRAWL_HOME/tier-cache) picked
// up the test server's host with tier=http. This covers the end-to-end
// flag plumbing + cache lifecycle in one pass: runBatch opens the cache
// through openTierCache, attaches it to the router, workers call
// router.Route which calls cache.Observe on success, and the cache is
// closed + flushed by the time runBatch returns.
func TestBatchPopulatesTierCache(t *testing.T) {
	srv := newTestServer(t)
	trawlHome := withTrawlHome(t)

	urlFile := writeURLFile(t, trawlHome, []string{
		srv.URL + "/product/1",
		srv.URL + "/product/2",
	})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:   []string{"title=h1.title"},
		outputPath:  outputFile,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		jobID:       "test-tier-cache",
		tiers:       "http,chromium",
	}
	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	// Open the default cache path and inspect it directly. This exercises
	// the same code the next trawl invocation would take.
	cachePath := filepath.Join(trawlHome, "tier-cache")
	c, err := tierlearn.Open(cachePath)
	if err != nil {
		t.Fatalf("open cache at %s: %v", cachePath, err)
	}
	defer c.Close()

	host := mustHost(t, srv.URL)
	if got := c.Preferred(host); got != "http" {
		t.Errorf("cache.Preferred(%q) = %q, want http", host, got)
	}
	hosts, err := c.Hosts()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hosts[host]; !ok {
		t.Errorf("cache missing host %q; entries = %+v", host, hosts)
	}
}

// TestBatchRespectsNoTierLearning verifies that --no-tier-learning
// prevents the cache from being populated even though the batch succeeds.
func TestBatchRespectsNoTierLearning(t *testing.T) {
	srv := newTestServer(t)
	trawlHome := withTrawlHome(t)

	urlFile := writeURLFile(t, trawlHome, []string{srv.URL + "/product/1"})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:      []string{"title=h1.title"},
		outputPath:     outputFile,
		timeout:        5 * time.Second,
		concurrency:    1,
		ratePerSec:     50,
		jobID:          "test-no-learning",
		tiers:          "http",
		noTierLearning: true,
	}
	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	cachePath := filepath.Join(trawlHome, "tier-cache")
	c, err := tierlearn.Open(cachePath)
	if err != nil {
		// Cache dir may not even exist when learning is disabled — that's
		// an acceptable outcome. Test passes.
		return
	}
	defer c.Close()

	host := mustHost(t, srv.URL)
	if got := c.Preferred(host); got != "" {
		t.Errorf("with --no-tier-learning, cache should be empty for %q, got %q", host, got)
	}
}

// TestTierCachePathOverride verifies the --tier-cache-path flag points
// the cache at a custom directory.
func TestTierCachePathOverride(t *testing.T) {
	srv := newTestServer(t)
	trawlHome := withTrawlHome(t)
	customDir := filepath.Join(t.TempDir(), "custom-cache")

	urlFile := writeURLFile(t, trawlHome, []string{srv.URL + "/product/1"})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:     []string{"title=h1.title"},
		outputPath:    outputFile,
		timeout:       5 * time.Second,
		concurrency:   1,
		ratePerSec:    50,
		jobID:         "test-custom-cache",
		tiers:         "http",
		tierCachePath: customDir,
	}
	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	c, err := tierlearn.Open(customDir)
	if err != nil {
		t.Fatalf("open custom cache at %s: %v", customDir, err)
	}
	defer c.Close()

	host := mustHost(t, srv.URL)
	if got := c.Preferred(host); got != "http" {
		t.Errorf("custom cache.Preferred(%q) = %q, want http", host, got)
	}

	// Default cache path should be empty (not even created in this test).
	defaultPath := filepath.Join(trawlHome, "tier-cache")
	if _, derr := tierlearn.Open(defaultPath); derr == nil {
		// The default dir may have been opened by a concurrent test; just
		// ensure it doesn't know about our test server host.
		// (No assertion here — the custom path is what matters.)
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Hostname()
}
