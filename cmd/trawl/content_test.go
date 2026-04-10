package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newContentTestServer serves a rich article page with metadata, a
// JSON-LD block, and nav/footer boilerplate. Shared by the content
// extraction tests below.
func newContentTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
  <title>Raw Page Title</title>
  <meta name="description" content="A test article about widgets.">
  <meta property="og:title" content="Widgets — Social Card Title">
  <meta property="og:description" content="Short social snippet.">
  <meta property="og:image" content="/images/widget.png">
  <meta property="article:published_time" content="2026-02-14T09:30:00Z">
  <link rel="canonical" href="/article">
  <script type="application/ld+json">
  {
    "@context": "https://schema.org",
    "@type": "NewsArticle",
    "headline": "Widgets Today",
    "datePublished": "2026-02-14"
  }
  </script>
</head>
<body>
  <nav>
    <a href="/home">Home</a>
    <a href="/pricing">Pricing</a>
    <a href="/about">About</a>
  </nav>
  <article>
    <h1>The real article headline</h1>
    <p>` + strings.Repeat("This is substantive article content that should survive readability. ", 25) + `</p>
    <p>` + strings.Repeat("A second meaningful paragraph follows with more detail. ", 25) + `</p>
  </article>
  <footer>Copyright boilerplate. Cookie notice. Privacy policy link.</footer>
</body>
</html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestScrapeEmitsMetadataByDefault: running scrape with NO content flags
// should still populate metadata.page because it's on by default.
func TestScrapeEmitsMetadataByDefault(t *testing.T) {
	srv := newContentTestServer(t)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    5 * time.Second,
		tiers:      "http",
	}
	if err := runScrape(context.Background(), srv.URL+"/article", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]

	if rec.Metadata.Page == nil {
		t.Fatal("metadata.page is nil (should default on)")
	}
	page := rec.Metadata.Page

	// og:title wins over <title>
	if page.Title != "Widgets — Social Card Title" {
		t.Errorf("title = %q, want og:title to win", page.Title)
	}
	if page.Description != "Short social snippet." {
		t.Errorf("description = %q, want og:description", page.Description)
	}
	if !strings.HasSuffix(page.Canonical, "/article") {
		t.Errorf("canonical = %q", page.Canonical)
	}
	if page.Language != "en" {
		t.Errorf("language = %q", page.Language)
	}
	if page.PublishedAt == nil {
		t.Error("published_at should be parsed from article:published_time")
	}
	if len(page.JSONLD) != 1 {
		t.Errorf("json_ld len = %d, want 1", len(page.JSONLD))
	}
	if page.OpenGraph["image"] != "/images/widget.png" {
		t.Errorf("og:image = %q", page.OpenGraph["image"])
	}

	// Body field should NOT be populated because --format was not set.
	if rec.Body != "" {
		t.Errorf("body should be empty when --format is unset, got %d bytes", len(rec.Body))
	}
	if rec.BodyFormat != "" {
		t.Errorf("body_format should be empty, got %q", rec.BodyFormat)
	}
}

// TestScrapeNoMetadataFlagOmitsPage: --no-metadata should skip the page
// field entirely.
func TestScrapeNoMetadataFlagOmitsPage(t *testing.T) {
	srv := newContentTestServer(t)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    5 * time.Second,
		tiers:      "http",
		noMetadata: true,
	}
	if err := runScrape(context.Background(), srv.URL+"/article", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	rec := readJSONL(t, outputFile)[0]
	if rec.Metadata.Page != nil {
		t.Errorf("metadata.page should be nil with --no-metadata, got %+v", rec.Metadata.Page)
	}
}

// TestScrapeFormatMarkdown: --format markdown should populate body as
// markdown, with headings and links converted.
func TestScrapeFormatMarkdown(t *testing.T) {
	srv := newContentTestServer(t)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    5 * time.Second,
		tiers:      "http",
		format:     "markdown",
	}
	if err := runScrape(context.Background(), srv.URL+"/article", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	rec := readJSONL(t, outputFile)[0]
	if rec.BodyFormat != "markdown" {
		t.Errorf("body_format = %q, want markdown", rec.BodyFormat)
	}
	if !strings.Contains(rec.Body, "# The real article headline") {
		t.Errorf("markdown body missing # heading conversion:\n%s", rec.Body)
	}
	if strings.Contains(rec.Body, "<h1>") {
		t.Error("markdown body still contains raw <h1> tags")
	}
}

// TestScrapeFormatHTMLPassesThrough: --format html should emit the raw
// HTML body verbatim.
func TestScrapeFormatHTMLPassesThrough(t *testing.T) {
	srv := newContentTestServer(t)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    5 * time.Second,
		tiers:      "http",
		format:     "html",
	}
	if err := runScrape(context.Background(), srv.URL+"/article", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	rec := readJSONL(t, outputFile)[0]
	if rec.BodyFormat != "html" {
		t.Errorf("body_format = %q, want html", rec.BodyFormat)
	}
	if !strings.Contains(rec.Body, "<h1>The real article headline</h1>") {
		t.Error("html body should contain raw heading tag")
	}
}

// TestScrapeReadabilityStripsBoilerplate: --readability + --format markdown
// should strip the nav/footer while keeping the article content.
func TestScrapeReadabilityStripsBoilerplate(t *testing.T) {
	srv := newContentTestServer(t)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath:  outputFile,
		timeout:     5 * time.Second,
		tiers:       "http",
		format:      "markdown",
		readability: true,
	}
	if err := runScrape(context.Background(), srv.URL+"/article", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	rec := readJSONL(t, outputFile)[0]
	body := rec.Body

	if !strings.Contains(body, "article content that should survive") {
		t.Errorf("article content missing from readability output:\n%s", body)
	}
	if strings.Contains(body, "Privacy policy") {
		t.Error("footer boilerplate survived readability pass")
	}

	// Metadata should still be populated because it's extracted from the
	// ORIGINAL body (before readability strips head).
	if rec.Metadata.Page == nil || rec.Metadata.Page.Title == "" {
		t.Error("metadata.page should survive even when readability is on")
	}
}

// TestScrapeInvalidFormatErrors: --format pdf is not a real option, runScrape
// should fail fast before issuing any network request.
func TestScrapeInvalidFormatErrors(t *testing.T) {
	trawlHome := withTrawlHome(t)
	opts := scrapeOpts{
		outputPath: filepath.Join(trawlHome, "out.jsonl"),
		timeout:    time.Second,
		tiers:      "http",
		format:     "pdf",
	}
	if err := runScrape(context.Background(), "https://example.com/", opts); err == nil {
		t.Fatal("expected error for --format pdf")
	}
}
