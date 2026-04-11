package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newMapSite builds a tiny site with:
//   - /robots.txt allowing everything and declaring a sitemap
//   - /sitemap.xml with two explicit URLs
//   - /, /a, /b, /c, /d linked in a small graph for the crawl source
//
// The crawl source should discover at least /a and /b from /, plus /c
// from /a (depth 2 reach). The sitemap declares /from-sitemap-1 and
// /from-sitemap-2 which are NOT in the crawl graph, so they exercise
// the "sitemap-only" URLs path.
func newMapSite(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()

	// Every HTML page shares this filler so the body clears the HTTP
	// engine's 512-byte validity threshold. Same trick used by the
	// crawl tests.
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	htmlPage := func(w http.ResponseWriter, links ...string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString("<html><body><p>")
		b.WriteString(pad)
		b.WriteString("</p>")
		for _, l := range links {
			fmt.Fprintf(&b, `<a href="%s">link</a>`, l)
		}
		b.WriteString("</body></html>")
		_, _ = w.Write([]byte(b.String()))
	}

	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "User-agent: *\nAllow: /\nSitemap: %s/sitemap.xml\n", srv.URL)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		// Include one URL that ALSO appears in the crawl graph (/a) so
		// the dedup path is exercised, plus two URLs that only the
		// sitemap declares.
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/a</loc></url>
  <url><loc>%s/from-sitemap-1</loc></url>
  <url><loc>%s/from-sitemap-2</loc></url>
</urlset>`, srv.URL, srv.URL, srv.URL)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		htmlPage(w, "/a", "/b", "https://external.invalid/x")
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		htmlPage(w, "/c", "/")
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		htmlPage(w, "/d")
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) { htmlPage(w) })
	mux.HandleFunc("/d", func(w http.ResponseWriter, r *http.Request) { htmlPage(w) })
	// Pages the sitemap declares but nothing links to. They have to
	// serve 200 so the crawl source wouldn't reach them, but a sitemap
	// source emits them without fetching.
	mux.HandleFunc("/from-sitemap-1", func(w http.ResponseWriter, r *http.Request) { htmlPage(w) })
	mux.HandleFunc("/from-sitemap-2", func(w http.ResponseWriter, r *http.Request) { htmlPage(w) })

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// readLines parses a plain-text URL list into a slice for comparison.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// hasURL is a tolerant substring match so we don't have to reconstruct
// the canonical form (trailing slash, port) in each assertion.
func hasURL(lines []string, suffix string) bool {
	for _, l := range lines {
		if strings.HasSuffix(l, suffix) || strings.HasSuffix(l, suffix+"/") {
			return true
		}
	}
	return false
}

func TestMapBothSources(t *testing.T) {
	srv := newMapSite(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "urls.txt")

	opts := mapOpts{
		sources:     "both",
		depth:       2,
		sameDomain:  true,
		limit:       1000,
		timeout:     5 * time.Second,
		outputPath:  out,
		concurrency: 4,
		ratePerSec:  50,
		sitemapMax:  100,
	}
	if err := runMap(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runMap: %v", err)
	}

	lines := readLines(t, out)
	if len(lines) == 0 {
		t.Fatal("no URLs emitted")
	}

	// Sitemap-only URLs must appear.
	if !hasURL(lines, "/from-sitemap-1") {
		t.Errorf("missing sitemap-only URL /from-sitemap-1, got %v", lines)
	}
	if !hasURL(lines, "/from-sitemap-2") {
		t.Errorf("missing sitemap-only URL /from-sitemap-2, got %v", lines)
	}
	// Crawl-discovered URLs at depth <= 2 must appear.
	for _, p := range []string{"/a", "/b", "/c", "/d"} {
		if !hasURL(lines, p) {
			t.Errorf("missing crawl URL %s, got %v", p, lines)
		}
	}
	// Dedup: /a appears in BOTH sitemap and crawl. Must appear exactly
	// once in the output.
	count := 0
	for _, l := range lines {
		if strings.HasSuffix(l, "/a") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("/a should appear exactly once, got %d times (%v)", count, lines)
	}
	// Same-domain filter: no external URLs.
	for _, l := range lines {
		if strings.Contains(l, "external.invalid") {
			t.Errorf("same-domain violated: %s", l)
		}
	}
}

func TestMapSitemapOnly(t *testing.T) {
	srv := newMapSite(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "urls.txt")

	opts := mapOpts{
		sources:    "sitemap",
		limit:      1000,
		timeout:    5 * time.Second,
		outputPath: out,
		sitemapMax: 100,
	}
	if err := runMap(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runMap: %v", err)
	}

	lines := readLines(t, out)
	// Must contain sitemap URLs.
	if !hasURL(lines, "/from-sitemap-1") {
		t.Errorf("missing /from-sitemap-1, got %v", lines)
	}
	// Must NOT contain crawl-only URLs (/b, /c, /d, seed).
	for _, p := range []string{"/b", "/c", "/d"} {
		if hasURL(lines, p) {
			t.Errorf("sitemap-only mode leaked crawl URL %s: %v", p, lines)
		}
	}
}

func TestMapCrawlOnly(t *testing.T) {
	srv := newMapSite(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "urls.txt")

	opts := mapOpts{
		sources:     "crawl",
		depth:       2,
		sameDomain:  true,
		limit:       1000,
		timeout:     5 * time.Second,
		outputPath:  out,
		concurrency: 4,
		ratePerSec:  50,
	}
	if err := runMap(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runMap: %v", err)
	}

	lines := readLines(t, out)
	// Seed + crawl children.
	for _, p := range []string{"/a", "/b", "/c", "/d"} {
		if !hasURL(lines, p) {
			t.Errorf("missing crawl URL %s, got %v", p, lines)
		}
	}
	// Sitemap-only URLs MUST NOT appear.
	for _, p := range []string{"/from-sitemap-1", "/from-sitemap-2"} {
		if hasURL(lines, p) {
			t.Errorf("crawl-only mode leaked sitemap URL %s: %v", p, lines)
		}
	}
}

func TestMapDepthCap(t *testing.T) {
	srv := newMapSite(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "urls.txt")

	opts := mapOpts{
		sources:     "crawl",
		depth:       1,
		sameDomain:  true,
		limit:       1000,
		timeout:     5 * time.Second,
		outputPath:  out,
		concurrency: 4,
		ratePerSec:  50,
	}
	if err := runMap(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runMap: %v", err)
	}

	lines := readLines(t, out)
	// depth=1: seed fetches /, discovers /a and /b. /c and /d are depth 2.
	for _, p := range []string{"/a", "/b"} {
		if !hasURL(lines, p) {
			t.Errorf("missing depth-1 URL %s, got %v", p, lines)
		}
	}
	for _, p := range []string{"/c", "/d"} {
		if hasURL(lines, p) {
			t.Errorf("depth cap violated: %s should not have been crawled", p)
		}
	}
}

func TestMapLimitCap(t *testing.T) {
	srv := newMapSite(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "urls.txt")

	opts := mapOpts{
		sources:     "crawl",
		depth:       10,
		sameDomain:  true,
		limit:       2,
		timeout:     5 * time.Second,
		outputPath:  out,
		concurrency: 4,
		ratePerSec:  50,
	}
	if err := runMap(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runMap: %v", err)
	}

	lines := readLines(t, out)
	if len(lines) > 2 {
		t.Errorf("limit cap violated: got %d lines, want <= 2: %v", len(lines), lines)
	}
	if len(lines) < 1 {
		t.Errorf("expected at least the seed, got %v", lines)
	}
}

func TestMapInvalidSources(t *testing.T) {
	srv := newMapSite(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "urls.txt")

	opts := mapOpts{
		sources:    "frobnicate",
		limit:      100,
		timeout:    5 * time.Second,
		outputPath: out,
	}
	if err := runMap(context.Background(), srv.URL+"/", opts); err == nil {
		t.Fatal("expected error on invalid --sources, got nil")
	}
}
