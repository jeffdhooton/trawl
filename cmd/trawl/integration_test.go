package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeffdhooton/trawl/internal/frontier"
	"github.com/jeffdhooton/trawl/internal/output"
)

// testServer serves a handful of canned pages used across the integration
// tests. The /slow endpoint is configurable so we can trigger SIGINT-like
// cancellation mid-run.
type testServer struct {
	*httptest.Server
	reqCount atomic.Int64
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})

	mux.HandleFunc("/product/", func(w http.ResponseWriter, r *http.Request) {
		ts.reqCount.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/product/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body>
		<h1 class="title">Product %s</h1>
		<span class="price">$%s.99</span>
		<div class="desc">%s</div>
		%s
		</body></html>`, id, id, strings.Repeat("pad ", 200), strings.Repeat("<br>", 100))
	})

	mux.HandleFunc("/404", func(w http.ResponseWriter, r *http.Request) {
		ts.reqCount.Add(1)
		http.NotFound(w, r)
	})

	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		ts.reqCount.Add(1)
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
	})

	ts.Server = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func writeURLFile(t *testing.T, dir string, urls []string) string {
	t.Helper()
	path := filepath.Join(dir, "urls.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, u := range urls {
		fmt.Fprintln(f, u)
	}
	return path
}

func readJSONL(t *testing.T, path string) []output.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []output.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r output.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("parse jsonl: %v\nline: %s", err, sc.Text())
		}
		out = append(out, r)
	}
	return out
}

// withTrawlHome sets TRAWL_HOME to a temp dir so tests are hermetic.
func withTrawlHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TRAWL_HOME", dir)
	return dir
}

func TestBatchEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	trawlHome := withTrawlHome(t)

	urls := []string{
		srv.URL + "/product/1",
		srv.URL + "/product/2",
		srv.URL + "/product/3",
	}
	urlFile := writeURLFile(t, trawlHome, urls)
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:    []string{"title=h1.title", "price=.price"},
		outputPath:   outputFile,
		ignoreRobots: false,
		timeout:      5 * time.Second,
		concurrency:  5,
		ratePerSec:   50,
		jobID:        "test-job-1",
	}

	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	seen := map[string]bool{}
	for _, r := range records {
		if r.StatusCode != 200 {
			t.Errorf("record %s: status = %d", r.URL, r.StatusCode)
		}
		title, _ := r.Extracted["title"].(string)
		if !strings.HasPrefix(title, "Product ") {
			t.Errorf("record %s: title = %q", r.URL, title)
		}
		seen[r.CanonicalURL] = true
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 unique URLs, got %d", len(seen))
	}

	// Frontier should show 3 done, 0 queued, 0 failed.
	f, err := frontier.Open(filepath.Join(trawlHome, "jobs", "test-job-1", "frontier"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stats, _ := f.Stats()
	if stats.Done != 3 || stats.Queued != 0 || stats.Failed != 0 {
		t.Errorf("frontier stats: %+v", stats)
	}
}

func TestBatchHandlesFailures(t *testing.T) {
	srv := newTestServer(t)
	trawlHome := withTrawlHome(t)

	urls := []string{
		srv.URL + "/product/1",
		srv.URL + "/404",
	}
	urlFile := writeURLFile(t, trawlHome, urls)
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:   []string{"title=h1.title"},
		outputPath:  outputFile,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		jobID:       "test-job-fail",
	}

	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	f, _ := frontier.Open(filepath.Join(trawlHome, "jobs", "test-job-fail", "frontier"))
	defer f.Close()
	stats, _ := f.Stats()
	if stats.Done != 1 || stats.Failed != 1 {
		t.Errorf("frontier stats: %+v", stats)
	}
}

func TestResumeAfterInterrupt(t *testing.T) {
	srv := newTestServer(t)
	trawlHome := withTrawlHome(t)

	// 20 product URLs, batch should take at least ~200ms at rate=100
	var urls []string
	for i := 0; i < 20; i++ {
		urls = append(urls, fmt.Sprintf("%s/product/%d", srv.URL, i))
	}
	urlFile := writeURLFile(t, trawlHome, urls)
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:    []string{"title=h1.title"},
		outputPath:   outputFile,
		timeout:      5 * time.Second,
		concurrency:  2,
		ratePerSec:   50,
		ignoreRobots: false,
		jobID:        "test-resume",
	}

	// First run with a short deadline so it gets interrupted mid-stream.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	err := runBatch(ctx, urlFile, opts)
	cancel()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first run: %v", err)
	}

	// Verify we did NOT finish all 20.
	f1, _ := frontier.Open(filepath.Join(trawlHome, "jobs", "test-resume", "frontier"))
	s1, _ := f1.Stats()
	f1.Close()
	if s1.Done+s1.Failed >= 20 {
		t.Skipf("first run finished everything (%+v); timing too fast to test resume", s1)
	}
	if s1.Queued == 0 && s1.InFlight == 0 {
		t.Fatalf("nothing left to resume: %+v", s1)
	}

	// Now resume and let it finish.
	if err := runResume(context.Background(), "test-resume"); err != nil {
		t.Fatalf("resume: %v", err)
	}

	f2, _ := frontier.Open(filepath.Join(trawlHome, "jobs", "test-resume", "frontier"))
	defer f2.Close()
	s2, _ := f2.Stats()
	if s2.Done+s2.Failed != 20 {
		t.Errorf("after resume stats: %+v (want done+failed=20)", s2)
	}
	if s2.Queued != 0 || s2.InFlight != 0 {
		t.Errorf("leftover work after resume: %+v", s2)
	}

	// Results file should have at least 20 records (possibly a few dupes
	// if one was written before interrupt and retried — P0 accepts this).
	records := readJSONL(t, outputFile)
	uniq := map[string]bool{}
	for _, r := range records {
		uniq[r.CanonicalURL] = true
	}
	if len(uniq) != 20 {
		t.Errorf("unique urls in output = %d, want 20", len(uniq))
	}
}

func TestScrapeSingleURL(t *testing.T) {
	srv := newTestServer(t)
	trawlHome := withTrawlHome(t)

	outputFile := filepath.Join(trawlHome, "out.jsonl")
	opts := scrapeOpts{
		selectors:  []string{"title=h1.title", "price=.price"},
		outputPath: outputFile,
		timeout:    5 * time.Second,
	}

	if err := runScrape(context.Background(), srv.URL+"/product/42", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}
	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	r := records[0]
	if r.StatusCode != 200 {
		t.Errorf("status = %d", r.StatusCode)
	}
	if title, _ := r.Extracted["title"].(string); title != "Product 42" {
		t.Errorf("title = %q", title)
	}
	if price, _ := r.Extracted["price"].(string); price != "$42.99" {
		t.Errorf("price = %q", price)
	}
}
