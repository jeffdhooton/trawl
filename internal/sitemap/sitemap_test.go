package sitemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// newFakeSite builds an httptest.Server serving a map of path → handler.
// Any unlisted path returns 404.
func newFakeSite(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range handlers {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeXML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(body))
}

func writeText(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(body))
}

// TestDiscoverViaRobotsTxt is the happy path: robots.txt declares a
// sitemap, the sitemap lists three URLs, they come back in order.
func TestDiscoverViaRobotsTxt(t *testing.T) {
	var srv *httptest.Server
	srv = newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "User-agent: *\nAllow: /\nSitemap: "+srv.URL+"/custom-sitemap.xml\n")
		},
		"/custom-sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/a</loc></url>
  <url><loc>https://example.com/b</loc></url>
  <url><loc>https://example.com/c</loc></url>
</urlset>`)
		},
	})

	urls, trace, err := Discover(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("urls = %v, want %v", urls, want)
	}
	if !trace.RobotsFetched {
		t.Error("expected RobotsFetched=true")
	}
	if len(trace.SitemapsFromRobots) != 1 {
		t.Errorf("SitemapsFromRobots = %v", trace.SitemapsFromRobots)
	}
	if len(trace.Sources) != 1 || trace.Sources[0].URLSetCount != 3 {
		t.Errorf("sources = %+v", trace.Sources)
	}
}

// TestDiscoverFallsBackToWellKnownPath: robots.txt exists but declares no
// Sitemap: directive, so Discover probes /sitemap.xml.
func TestDiscoverFallsBackToWellKnownPath(t *testing.T) {
	srv := newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "User-agent: *\nAllow: /\n")
		},
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/one</loc></url>
</urlset>`)
		},
	})

	urls, trace, err := Discover(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != "https://example.com/one" {
		t.Errorf("urls = %v", urls)
	}
	if len(trace.SitemapsFromRobots) != 0 {
		t.Errorf("expected zero robots-declared sitemaps, got %v", trace.SitemapsFromRobots)
	}
	// Should have tried /sitemap.xml (success) and possibly the other
	// well-known paths (404).
	found := false
	for _, s := range trace.Sources {
		if strings.HasSuffix(s.URL, "/sitemap.xml") && s.URLSetCount == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /sitemap.xml source with URLSetCount=1, got %+v", trace.Sources)
	}
}

// TestDiscoverSitemapIndexRecursion: top-level sitemapindex points to two
// child urlsets, each with its own URLs. Discover walks them all.
func TestDiscoverSitemapIndexRecursion(t *testing.T) {
	var srv *httptest.Server
	srv = newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "Sitemap: "+srv.URL+"/sitemap-index.xml\n")
		},
		"/sitemap-index.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>`+srv.URL+`/sitemap-products.xml</loc></sitemap>
  <sitemap><loc>`+srv.URL+`/sitemap-blog.xml</loc></sitemap>
</sitemapindex>`)
		},
		"/sitemap-products.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/product/1</loc></url>
  <url><loc>https://example.com/product/2</loc></url>
</urlset>`)
		},
		"/sitemap-blog.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/blog/post-1</loc></url>
</urlset>`)
		},
	})

	urls, trace, err := Discover(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantURLs := map[string]bool{
		"https://example.com/product/1": false,
		"https://example.com/product/2": false,
		"https://example.com/blog/post-1": false,
	}
	for _, u := range urls {
		if _, ok := wantURLs[u]; ok {
			wantURLs[u] = true
		}
	}
	for u, seen := range wantURLs {
		if !seen {
			t.Errorf("missing URL %q", u)
		}
	}
	// Trace should have three sources (one index + two children).
	if len(trace.Sources) != 3 {
		t.Errorf("expected 3 sources (1 index + 2 children), got %d: %+v", len(trace.Sources), trace.Sources)
	}
	// The index should be at depth 0, children at depth 1.
	for _, s := range trace.Sources {
		if strings.HasSuffix(s.URL, "/sitemap-index.xml") && s.Depth != 0 {
			t.Errorf("index depth = %d, want 0", s.Depth)
		}
		if strings.HasSuffix(s.URL, "/sitemap-products.xml") && s.Depth != 1 {
			t.Errorf("products depth = %d, want 1", s.Depth)
		}
	}
}

// TestDiscoverGzipByContentEncoding: a server serves a plain URL but with
// Content-Encoding: gzip and a gzipped body. Discover should transparently
// decompress.
func TestDiscoverGzipByContentEncoding(t *testing.T) {
	plainBody := `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/gz</loc></url>
</urlset>`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(plainBody))
	_ = gz.Close()
	gzBytes := buf.Bytes()

	srv := newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "User-agent: *\n")
		},
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(gzBytes)
		},
	})

	urls, _, err := Discover(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != "https://example.com/gz" {
		t.Errorf("urls = %v", urls)
	}
}

// TestDiscoverGzipByFilenameSuffix: sitemap URL ends in .gz, body is
// gzipped, server doesn't set Content-Encoding. Discover should still
// decompress.
func TestDiscoverGzipByFilenameSuffix(t *testing.T) {
	plainBody := `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/compressed</loc></url>
</urlset>`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(plainBody))
	_ = gz.Close()
	gzBytes := buf.Bytes()

	var srv *httptest.Server
	srv = newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "Sitemap: "+srv.URL+"/sitemap.xml.gz\n")
		},
		"/sitemap.xml.gz": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(gzBytes)
		},
	})

	urls, _, err := Discover(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != "https://example.com/compressed" {
		t.Errorf("urls = %v", urls)
	}
}

// TestDiscoverMaxURLsCap ensures the MaxURLs cap short-circuits walking
// and sets Truncated=true.
func TestDiscoverMaxURLsCap(t *testing.T) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
`)
	for i := 0; i < 20; i++ {
		body.WriteString("<url><loc>https://example.com/p/")
		body.WriteString(string(rune('a' + i)))
		body.WriteString("</loc></url>\n")
	}
	body.WriteString(`</urlset>`)

	srv := newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "User-agent: *\n")
		},
		"/sitemap.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, body.String())
		},
	})

	urls, trace, err := Discover(context.Background(), srv.URL, Options{MaxURLs: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 5 {
		t.Errorf("urls length = %d, want 5", len(urls))
	}
	if !trace.Truncated {
		t.Error("expected Truncated=true")
	}
}

// TestDiscoverMaxDepthCap: a chain of three sitemap-indexes, each pointing
// to the next. With MaxDepth=1, Discover should stop after walking index
// level 0 and level 1, ignoring level 2 and beyond.
func TestDiscoverMaxDepthCap(t *testing.T) {
	var srv *httptest.Server
	srv = newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "Sitemap: "+srv.URL+"/index-0.xml\n")
		},
		"/index-0.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>`+srv.URL+`/index-1.xml</loc></sitemap>
</sitemapindex>`)
		},
		"/index-1.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>`+srv.URL+`/index-2.xml</loc></sitemap>
</sitemapindex>`)
		},
		"/index-2.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/deep</loc></url>
</urlset>`)
		},
	})

	// MaxDepth=1: we walk depth 0 (index-0) and depth 1 (index-1). index-2
	// is queued at depth 2 but skipped by the cap, so we never see the
	// deep URL.
	urls, trace, err := Discover(context.Background(), srv.URL, Options{MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 0 {
		t.Errorf("urls = %v, want empty (MaxDepth=1 should cut off before deep urlset)", urls)
	}
	// Sources should include index-0 (depth 0) and index-1 (depth 1) but
	// not index-2.
	for _, s := range trace.Sources {
		if strings.HasSuffix(s.URL, "/index-2.xml") {
			t.Errorf("should not have walked index-2 at MaxDepth=1")
		}
	}
}

// TestDiscoverCyclicIndexTerminates: a sitemap-index that references
// itself should terminate via the seen-set, not loop forever. Correctness
// here means the call returns at all — if the seen-set is broken the go
// test harness timeout will flag it.
func TestDiscoverCyclicIndexTerminates(t *testing.T) {
	var srv *httptest.Server
	srv = newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "Sitemap: "+srv.URL+"/loop.xml\n")
		},
		"/loop.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>`+srv.URL+`/loop.xml</loc></sitemap>
</sitemapindex>`)
		},
	})

	_, trace, err := Discover(context.Background(), srv.URL, Options{MaxDepth: 5})
	if err != nil {
		t.Fatal(err)
	}
	// loop.xml should appear exactly once in Sources (seen-set dedup).
	count := 0
	for _, s := range trace.Sources {
		if strings.HasSuffix(s.URL, "/loop.xml") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("loop.xml fetched %d times, want 1 (seen-set dedup)", count)
	}
}

// TestDiscoverDedupsURLs: the same URL listed twice across two sitemaps
// appears only once in the output.
func TestDiscoverDedupsURLs(t *testing.T) {
	var srv *httptest.Server
	srv = newFakeSite(t, map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, r *http.Request) {
			writeText(w, "Sitemap: "+srv.URL+"/index.xml\n")
		},
		"/index.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>`+srv.URL+`/a.xml</loc></sitemap>
  <sitemap><loc>`+srv.URL+`/b.xml</loc></sitemap>
</sitemapindex>`)
		},
		"/a.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/shared</loc></url>
  <url><loc>https://example.com/a-only</loc></url>
</urlset>`)
		},
		"/b.xml": func(w http.ResponseWriter, r *http.Request) {
			writeXML(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/shared</loc></url>
  <url><loc>https://example.com/b-only</loc></url>
</urlset>`)
		},
	})

	urls, _, err := Discover(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 3 {
		t.Errorf("urls = %v, want 3 unique", urls)
	}
	seen := map[string]int{}
	for _, u := range urls {
		seen[u]++
	}
	if seen["https://example.com/shared"] != 1 {
		t.Errorf("shared URL appeared %d times, want 1", seen["https://example.com/shared"])
	}
}

// TestDiscoverBadBaseURL rejects non-http schemes and unparseable URLs.
func TestDiscoverBadBaseURL(t *testing.T) {
	_, _, err := Discover(context.Background(), "ftp://example.com", Options{})
	if err == nil {
		t.Error("expected error for ftp:// base")
	}
	_, _, err = Discover(context.Background(), "://not-a-url", Options{})
	if err == nil {
		t.Error("expected error for malformed base")
	}
}

// TestDiscoverAllSourcesMissing: robots.txt is missing, no well-known path
// responds. Discover returns empty URLs, no fatal error, and every source
// has an error populated.
func TestDiscoverAllSourcesMissing(t *testing.T) {
	srv := newFakeSite(t, map[string]http.HandlerFunc{}) // no handlers → everything 404

	urls, trace, err := Discover(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 0 {
		t.Errorf("urls = %v, want empty", urls)
	}
	// Should have attempted each well-known path.
	if len(trace.Sources) == 0 {
		t.Error("expected at least one source attempt")
	}
	for _, s := range trace.Sources {
		if s.Error == "" {
			t.Errorf("source %s has no error but nothing was served", s.URL)
		}
	}
}

