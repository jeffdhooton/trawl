package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newCSVFixtureServer returns a small server serving /product/N pages
// with a title and a price that selector rules can extract. Body is
// padded above the HTTP engine's 512-byte validity threshold.
func newCSVFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/product/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/product/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body><p>%s</p><h1 class="title">Product %s</h1><span class="price">$%s.99</span></body></html>`, pad, id, id)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func readCSVRows(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	return rows
}

// columnIndex returns the position of name in a CSV header row, or -1.
func columnIndex(header []string, name string) int {
	for i, h := range header {
		if h == name {
			return i
		}
	}
	return -1
}

func TestBatchCSVAutoDiscover(t *testing.T) {
	srv := newCSVFixtureServer(t)
	trawlHome := withTrawlHome(t)
	urlFile := writeURLFile(t, trawlHome, []string{
		srv.URL + "/product/1",
		srv.URL + "/product/2",
	})
	outputFile := filepath.Join(trawlHome, "out.csv")

	opts := batchOpts{
		selectors:   []string{"title=h1.title", "price=.price"},
		outputPath:  outputFile,
		timeout:     5 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		jobID:       "csv-auto",
	}
	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	rows := readCSVRows(t, outputFile)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (header+2), got %d", len(rows))
	}
	header := rows[0]
	// Auto-discovered extracted columns must land.
	mustHave := []string{"url", "canonical_url", "status_code", "extracted.title", "extracted.price"}
	for _, col := range mustHave {
		if columnIndex(header, col) < 0 {
			t.Errorf("header missing %q: %v", col, header)
		}
	}
	// Validate a data row has recognizable values.
	titleCol := columnIndex(header, "extracted.title")
	found := map[string]bool{}
	for _, row := range rows[1:] {
		found[row[titleCol]] = true
	}
	if !found["Product 1"] || !found["Product 2"] {
		t.Errorf("extracted titles = %v, want Product 1 and Product 2", found)
	}
}

func TestBatchCSVExplicitColumns(t *testing.T) {
	srv := newCSVFixtureServer(t)
	trawlHome := withTrawlHome(t)
	urlFile := writeURLFile(t, trawlHome, []string{srv.URL + "/product/42"})
	outputFile := filepath.Join(trawlHome, "out.csv")

	opts := batchOpts{
		selectors:   []string{"title=h1.title", "price=.price"},
		outputPath:  outputFile,
		timeout:     5 * time.Second,
		concurrency: 1,
		ratePerSec:  50,
		jobID:       "csv-explicit",
		csvColumns:  []string{"url", "extracted.title"},
	}
	if err := runBatch(context.Background(), urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	rows := readCSVRows(t, outputFile)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0][0] != "url" || rows[0][1] != "extracted.title" {
		t.Errorf("header = %v, want [url extracted.title]", rows[0])
	}
	// Only 2 columns per row — explicit list is authoritative.
	if len(rows[1]) != 2 {
		t.Errorf("data row has %d columns, want 2", len(rows[1]))
	}
	if rows[1][1] != "Product 42" {
		t.Errorf("title = %q", rows[1][1])
	}
}

func TestBatchCSVColumnsRequireCSVOutput(t *testing.T) {
	trawlHome := withTrawlHome(t)
	urlFile := writeURLFile(t, trawlHome, []string{"https://example.com/"})

	opts := batchOpts{
		outputPath: filepath.Join(trawlHome, "out.jsonl"), // NOT a csv path
		timeout:    5 * time.Second,
		jobID:      "csv-mismatch",
		csvColumns: []string{"url"},
	}
	err := runBatch(context.Background(), urlFile, opts)
	if err == nil {
		t.Fatal("expected error for --csv-columns with .jsonl output")
	}
	if !strings.Contains(err.Error(), "csv-columns") {
		t.Errorf("error = %q, should mention csv-columns", err)
	}
}

func TestScrapeTSVOutput(t *testing.T) {
	srv := newCSVFixtureServer(t)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.tsv")

	opts := scrapeOpts{
		selectors:  []string{"title=h1.title"},
		outputPath: outputFile,
		timeout:    5 * time.Second,
		tiers:      "http",
	}
	if err := runScrape(context.Background(), srv.URL+"/product/7", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	// Tab-separated: header row must contain tabs, not commas.
	if !strings.Contains(string(data), "\t") {
		t.Errorf("expected tab-separated output, got: %s", data)
	}
}
