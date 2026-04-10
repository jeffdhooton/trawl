package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// TestFollowLinkResolvesHomepageToPricing simulates the benchmark's main
// use case: batch of homepage URLs, --follow-link finds the pricing page,
// extraction runs on the pricing page not the homepage.
func TestFollowLinkResolvesHomepageToPricing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	// Homepage has nav with a pricing link.
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
				<a href="/contact">Contact</a>
			</nav>
			<h1 class="homepage-title">Welcome to Widgets Inc</h1>
			<p>We make widgets.</p>
			<!-- ` + repeatPadding() + ` -->
		</body></html>`))
	})
	// Pricing page with the actual content we want to extract.
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1 class="price-title">Pricing</h1>
			<div class="plan">Starter $19/mo</div>
			<div class="plan">Pro $49/mo</div>
			<!-- padding to get above validity min body size -->
			` + repeatPadding() + `
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	urlFile := writeURLFile(t, trawlHome, []string{srv.URL + "/"})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:   []string{"title=h1.price-title", "plans=.plan[]"},
		outputPath:  outputFile,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		jobID:       "test-follow",
		tiers:       "http",
		followLink:  `a[href*="pricing"]`,
	}

	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]

	// url should be the ORIGINAL input (homepage), canonical_url should be
	// the resolved pricing page.
	if rec.URL != srv.URL+"/" {
		t.Errorf("url = %q, want homepage", rec.URL)
	}
	if rec.CanonicalURL != srv.URL+"/pricing" {
		t.Errorf("canonical_url = %q, want pricing page", rec.CanonicalURL)
	}
	if rec.StatusCode != 200 {
		t.Errorf("status = %d", rec.StatusCode)
	}

	// Extraction should come from the PRICING page, not the homepage.
	title, _ := rec.Extracted["title"].(string)
	if title != "Pricing" {
		t.Errorf("title = %q, want 'Pricing' (from pricing page)", title)
	}
	plans, ok := rec.Extracted["plans"].([]any)
	if !ok {
		t.Fatalf("plans not a slice: %T", rec.Extracted["plans"])
	}
	if len(plans) != 2 {
		t.Errorf("plans = %v, want 2 entries", plans)
	}
	if rec.FailureCategory != "success" {
		t.Errorf("category = %q, want success", rec.FailureCategory)
	}
}

func TestFollowLinkNoMatchIsFollowFailed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	// Homepage has NO pricing link.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<a href="/about">About</a>
			<h1>Homepage with no pricing link</h1>
			<!-- ` + repeatPadding() + ` -->
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	urlFile := writeURLFile(t, trawlHome, []string{srv.URL + "/"})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		outputPath:  outputFile,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		jobID:       "test-follow-miss",
		tiers:       "http",
		followLink:  `a[href*="pricing"]`,
	}

	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].FailureCategory != "follow_failed" {
		t.Errorf("category = %q, want follow_failed", records[0].FailureCategory)
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
