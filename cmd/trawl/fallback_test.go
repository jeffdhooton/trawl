package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSeedCSV writes a two-column CSV with `url` (primary) and
// `homepage` (fallback) for hybrid-discovery tests.
func writeSeedCSV(t *testing.T, dir string, rows [][2]string) string {
	t.Helper()
	path := filepath.Join(dir, "seed.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintln(f, "url,homepage")
	for _, r := range rows {
		fmt.Fprintf(f, "%s,%s\n", r[0], r[1])
	}
	return path
}

// TestHybridFallbackOn4xxRecoversViaHomepage is the happy path: the primary
// pricing URL returns 404, the fallback homepage has a pricing link in its
// nav, and the resolved page is what lands in the output record.
func TestHybridFallbackOn4xxRecoversViaHomepage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/dead-pricing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<nav>
				<a href="/about">About</a>
				<a href="/pricing">Pricing</a>
			</nav>
			<h1>Widgets Inc</h1>
			<!-- ` + repeatPadding() + ` -->
		</body></html>`))
	})
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1 class="price-title">Pricing</h1>
			<div class="plan">Starter $19/mo</div>
			<div class="plan">Pro $49/mo</div>
			` + repeatPadding() + `
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	urlFile := writeSeedCSV(t, trawlHome, [][2]string{
		{srv.URL + "/dead-pricing", srv.URL + "/"},
	})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:        []string{"title=h1.price-title", "plans=.plan[]"},
		outputPath:       outputFile,
		timeout:          5 * time.Second,
		concurrency:      2,
		ratePerSec:       50,
		jobID:            "test-hybrid-4xx",
		tiers:            "http",
		fallbackColumn:   "homepage",
		fallbackSelector: `a[href*="pricing"]`,
	}

	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]

	if rec.URL != srv.URL+"/dead-pricing" {
		t.Errorf("url = %q, want primary (dead-pricing)", rec.URL)
	}
	if rec.CanonicalURL != srv.URL+"/pricing" {
		t.Errorf("canonical_url = %q, want resolved /pricing", rec.CanonicalURL)
	}
	if rec.FailureCategory != "success" {
		t.Errorf("category = %q, want success", rec.FailureCategory)
	}
	if rec.Metadata.Discovery == nil {
		t.Fatal("metadata.discovery missing")
	}
	if rec.Metadata.Discovery.Path != "fallback" {
		t.Errorf("discovery.path = %q, want fallback", rec.Metadata.Discovery.Path)
	}
	if rec.Metadata.Discovery.PrimaryURL != srv.URL+"/dead-pricing" {
		t.Errorf("discovery.primary_url = %q", rec.Metadata.Discovery.PrimaryURL)
	}
	if rec.Metadata.Discovery.FallbackURL != srv.URL+"/" {
		t.Errorf("discovery.fallback_url = %q", rec.Metadata.Discovery.FallbackURL)
	}
	title, _ := rec.Extracted["title"].(string)
	if title != "Pricing" {
		t.Errorf("title = %q, want 'Pricing'", title)
	}
}

// TestHybridFallbackSkippedWhenPrimarySucceeds verifies that a live primary
// is returned as-is without touching the fallback URL. Discovery metadata
// should still be attached (path=primary) so downstream can count the row.
func TestHybridFallbackSkippedWhenPrimarySucceeds(t *testing.T) {
	var homepageHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1 class="price-title">Live Pricing</h1>
			` + repeatPadding() + `
		</body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		homepageHits++
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>home</h1></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	urlFile := writeSeedCSV(t, trawlHome, [][2]string{
		{srv.URL + "/pricing", srv.URL + "/"},
	})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:        []string{"title=h1.price-title"},
		outputPath:       outputFile,
		timeout:          5 * time.Second,
		concurrency:      2,
		ratePerSec:       50,
		jobID:            "test-hybrid-primary-ok",
		tiers:            "http",
		fallbackColumn:   "homepage",
		fallbackSelector: `a[href*="pricing"]`,
	}

	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	if homepageHits > 0 {
		t.Errorf("fallback homepage was fetched %d times; expected 0", homepageHits)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.CanonicalURL != srv.URL+"/pricing" {
		t.Errorf("canonical_url = %q, want primary", rec.CanonicalURL)
	}
	title, _ := rec.Extracted["title"].(string)
	if title != "Live Pricing" {
		t.Errorf("title = %q, want 'Live Pricing'", title)
	}
	if rec.Metadata.Discovery == nil {
		t.Fatal("metadata.discovery missing")
	}
	if rec.Metadata.Discovery.Path != "primary" {
		t.Errorf("discovery.path = %q, want primary", rec.Metadata.Discovery.Path)
	}
}

// TestHybridFallbackNoLinkMarksFollowFailed: primary 4xx's, fallback homepage
// is reachable but has no <a> matching the selector. The row should land
// with the primary's category preserved and the fallback counter should
// show a no_link attempt.
func TestHybridFallbackNoLinkPreservesPrimaryCategory(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/dead-pricing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<a href="/about">About</a>
			<h1>No pricing link anywhere</h1>
			<!-- ` + repeatPadding() + ` -->
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	urlFile := writeSeedCSV(t, trawlHome, [][2]string{
		{srv.URL + "/dead-pricing", srv.URL + "/"},
	})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		outputPath:       outputFile,
		timeout:          5 * time.Second,
		concurrency:      2,
		ratePerSec:       50,
		jobID:            "test-hybrid-no-link",
		tiers:            "http",
		fallbackColumn:   "homepage",
		fallbackSelector: `a[href*="pricing"]`,
	}

	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.FailureCategory != "http_4xx" {
		t.Errorf("category = %q, want http_4xx (primary preserved)", rec.FailureCategory)
	}
	if rec.Metadata.Discovery == nil || rec.Metadata.Discovery.Path != "primary" {
		t.Errorf("discovery = %+v, want path=primary with annotation", rec.Metadata.Discovery)
	}
}

// TestHybridFlagsRequireBoth rejects a config where only one of the two
// hybrid flags is set.
func TestHybridFlagsRequireBoth(t *testing.T) {
	trawlHome := withTrawlHome(t)
	urlFile := writeSeedCSV(t, trawlHome, [][2]string{{"https://example.com", ""}})

	opts := batchOpts{
		outputPath:       filepath.Join(trawlHome, "out.jsonl"),
		timeout:          time.Second,
		concurrency:      1,
		ratePerSec:       10,
		jobID:            "test-hybrid-partial",
		tiers:            "http",
		fallbackSelector: `a[href*="pricing"]`,
	}
	if err := runBatch(context.Background(), urlFile, opts); err == nil {
		t.Fatal("expected error when --fallback-selector is set without --fallback-column")
	}
}

// repeatPadding returns enough filler to pass validity.MinBodyBytes.
func repeatPadding() string {
	const padding = "abcdefghij"
	out := ""
	for i := 0; i < 100; i++ {
		out += padding
	}
	return out
}
