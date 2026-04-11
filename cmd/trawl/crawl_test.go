package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/trawl/internal/frontier"
)

// newLinkGraphServer serves a tiny, controlled HTML link graph for BFS tests.
//
//	/           → links to /a, /b, https://external.invalid/ignored
//	/a          → links to /c, /
//	/b          → links to /d
//	/c          → leaf
//	/d          → links to /e (depth-2 frontier)
//	/e          → leaf (depth 3 — only reached if --depth >= 3)
//	/loop       → links to /          (cycle — dedup test)
func newLinkGraphServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})

	// The HTTP engine rejects bodies under ~512 bytes as "stub" pages, so
	// pad each leaf with enough filler to clear the threshold. The filler
	// is inert text — the link graph is what the tests actually exercise.
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	pageFn := func(links ...string) string {
		var b strings.Builder
		b.WriteString(`<html><body><p>`)
		b.WriteString(pad)
		b.WriteString(`</p>`)
		for _, l := range links {
			fmt.Fprintf(&b, `<a href="%s">link</a>`, l)
		}
		b.WriteString(`</body></html>`)
		return b.String()
	}
	pages := map[string]string{
		"/":     pageFn("/a", "/b", "https://external.invalid/ignored"),
		"/a":    pageFn("/c", "/"),
		"/b":    pageFn("/d"),
		"/c":    pageFn(),
		"/d":    pageFn("/e"),
		"/e":    pageFn(),
		"/loop": pageFn("/"),
	}
	for path, body := range pages {
		p, b := path, body
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != p {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(b))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCrawlDepthCap(t *testing.T) {
	srv := newLinkGraphServer(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "crawl.jsonl")

	opts := crawlOpts{
		outputPath:  out,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		tiers:       "http",
		jobID:       "crawl-depth",
		depth:       1,
		sameDomain:  true,
		limit:       100,
	}
	if err := runCrawl(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runCrawl: %v", err)
	}

	records := readJSONL(t, out)
	seen := map[string]bool{}
	for _, r := range records {
		seen[r.CanonicalURL] = true
	}

	// Depth 0: seed "/"
	// Depth 1: /a, /b
	// Depth 2 would be /c, /d — must NOT appear with depth=1.
	wantPresent := []string{"/", "/a", "/b"}
	wantAbsent := []string{"/c", "/d", "/e"}

	for _, p := range wantPresent {
		if !seen[srv.URL+p] && !(p == "/" && seen[srv.URL+"/"]) {
			t.Errorf("missing expected URL %s%s in results (seen=%v)", srv.URL, p, seen)
		}
	}
	for _, p := range wantAbsent {
		if seen[srv.URL+p] {
			t.Errorf("depth cap violated: %s%s should not have been crawled", srv.URL, p)
		}
	}

	// Frontier should be fully drained (done+failed for every enqueued URL).
	f, _ := frontier.Open(filepath.Join(trawlHome, "jobs", "crawl-depth", "frontier"))
	defer f.Close()
	s, _ := f.Stats()
	if s.Queued != 0 || s.InFlight != 0 {
		t.Errorf("frontier not drained: %+v", s)
	}
	if s.Done < 3 {
		t.Errorf("expected at least 3 done records, got %+v", s)
	}
}

func TestCrawlLimitCap(t *testing.T) {
	srv := newLinkGraphServer(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "crawl.jsonl")

	opts := crawlOpts{
		outputPath:  out,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		tiers:       "http",
		jobID:       "crawl-limit",
		depth:       10, // unbounded by depth
		sameDomain:  true,
		limit:       3, // bounded by enqueue budget
	}
	if err := runCrawl(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runCrawl: %v", err)
	}

	f, _ := frontier.Open(filepath.Join(trawlHome, "jobs", "crawl-limit", "frontier"))
	defer f.Close()
	s, _ := f.Stats()

	// Seed + children enqueued. Limit is 3, so total enqueued must be <= 3.
	// (seed=1 counts toward the budget, so limit=3 caps at ~3 URLs.)
	if s.Total > 3 {
		t.Errorf("limit cap violated: Total = %d, want <= 3", s.Total)
	}
	if s.Total < 1 {
		t.Errorf("expected at least the seed, got Total = %d", s.Total)
	}
}

func TestCrawlSameDomainRejectsExternal(t *testing.T) {
	srv := newLinkGraphServer(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "crawl.jsonl")

	opts := crawlOpts{
		outputPath:  out,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		tiers:       "http",
		jobID:       "crawl-sd",
		depth:       2,
		sameDomain:  true,
		limit:       100,
	}
	if err := runCrawl(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runCrawl: %v", err)
	}

	records := readJSONL(t, out)
	for _, r := range records {
		if strings.Contains(r.CanonicalURL, "external.invalid") {
			t.Errorf("same-domain crawl leaked external URL: %s", r.CanonicalURL)
		}
	}
}

func TestCrawlDedupsCycles(t *testing.T) {
	srv := newLinkGraphServer(t)
	trawlHome := withTrawlHome(t)
	out := filepath.Join(trawlHome, "crawl.jsonl")

	// /a links back to "/", which is already in the frontier. Must not
	// cause duplicate records or infinite looping.
	opts := crawlOpts{
		outputPath:  out,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		tiers:       "http",
		jobID:       "crawl-cycle",
		depth:       3,
		sameDomain:  true,
		limit:       100,
	}
	if err := runCrawl(context.Background(), srv.URL+"/", opts); err != nil {
		t.Fatalf("runCrawl: %v", err)
	}

	records := readJSONL(t, out)
	counts := map[string]int{}
	for _, r := range records {
		counts[r.CanonicalURL]++
	}
	for u, c := range counts {
		if c > 1 {
			t.Errorf("URL %s appears %d times in results — cycle not deduped", u, c)
		}
	}
	// At depth 3 the whole graph (/, /a, /b, /c, /d, /e) should be reached.
	for _, p := range []string{"/", "/a", "/b", "/c", "/d", "/e"} {
		found := false
		for u := range counts {
			if strings.HasSuffix(u, p) && strings.HasPrefix(u, srv.URL) {
				// avoid matching "/a" vs "/": require exact path suffix
				if u == srv.URL+p || (p == "/" && u == srv.URL+"/") {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("expected %s%s in results at depth 3, got %v", srv.URL, p, keys(counts))
		}
	}
}

// keys is a tiny helper so test failures print a sorted-looking slice
// instead of the full Record map.
func keys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

