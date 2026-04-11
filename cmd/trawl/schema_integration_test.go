package main

import (
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

// newSEPFixtureServer serves a minimal HTML page shaped like a Stanford
// Encyclopedia of Philosophy entry so the --schema end-to-end test can
// run hermetically without touching the live site.
func newSEPFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	// Padding keeps the body above the HTTP engine's 512-byte validity
	// threshold. Same trick used by crawl/map fixtures.
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	html := fmt.Sprintf(`<html><body>
<h1>Test Entry</h1>
<div id="pubinfo"><em>First published Thu Jan 1, 2020; substantive revision Wed Jul 31, 2024</em></div>
<div id="preamble"><p>%s</p></div>
<div id="toc">
  <ul>
    <li><a href="#sec1">1. First Section</a></li>
    <li><a href="#sec2">2. Second Section</a></li>
  </ul>
</div>
<div id="main-text"><h2><a name="sec1">1. First Section</a></h2><p>body</p></div>
<div id="related-entries">
  <h2>Related Entries</h2>
  <p>
    <a href="../aesthetics/">aesthetics</a> |
    <a href="../logic/">logic</a>
  </p>
</div>
<div id="article-copyright">
  <p>
    <a href="../info.html#c">Copyright &copy; 2024</a> by
    <a href="http://example.com/author">Jane Doe</a>
  </p>
</div>
</body></html>`, pad)

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/entries/test/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeSchemaFile dumps a minimal SEP-shaped schema to disk so the test
// doesn't depend on the internal/schema testdata (which should remain
// package-local) and exercises the real --schema code path end to end.
func writeSchemaFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sep.yaml")
	body := `version: 1
fields:
  title:
    selector: "h1"
  pubinfo:
    selector: "#pubinfo em"
  toc_entries:
    selector: "#toc > ul > li > a"
    multiple: true
    fields:
      text:
        selector: ""
      anchor:
        selector: ""
        attr: "href"
  related_entries:
    selector: "#related-entries p a"
    multiple: true
    fields:
      title:
        selector: ""
      href:
        selector: ""
        attr: "href"
  author:
    selector: "#article-copyright a[href^='http']"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScrapeWithSchema(t *testing.T) {
	srv := newSEPFixtureServer(t)
	trawlHome := withTrawlHome(t)
	schemaPath := writeSchemaFile(t, trawlHome)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    5 * time.Second,
		tiers:      "http",
		schemaPath: schemaPath,
	}

	if err := runScrape(context.Background(), srv.URL+"/entries/test/", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.StatusCode != 200 {
		t.Errorf("status = %d, want 200", rec.StatusCode)
	}
	if rec.Extracted == nil {
		t.Fatal("record.extracted is nil — schema did not run")
	}

	if title, _ := rec.Extracted["title"].(string); title != "Test Entry" {
		t.Errorf("extracted.title = %q, want Test Entry", title)
	}
	if author, _ := rec.Extracted["author"].(string); author != "Jane Doe" {
		t.Errorf("extracted.author = %q, want Jane Doe", author)
	}

	toc, ok := rec.Extracted["toc_entries"].([]any)
	if !ok {
		t.Fatalf("toc_entries type = %T, want []any", rec.Extracted["toc_entries"])
	}
	if len(toc) != 2 {
		t.Fatalf("toc_entries len = %d, want 2", len(toc))
	}
	first := toc[0].(map[string]any)
	if first["text"] != "1. First Section" {
		t.Errorf("toc_entries[0].text = %v", first["text"])
	}
	if first["anchor"] != "#sec1" {
		t.Errorf("toc_entries[0].anchor = %v", first["anchor"])
	}

	rel, _ := rec.Extracted["related_entries"].([]any)
	if len(rel) != 2 {
		t.Fatalf("related_entries len = %d, want 2", len(rel))
	}
}

// TestScrapeSchemaAndSelectorMerge verifies the contract that --schema
// and --selector coexist, with schema keys winning on collision.
func TestScrapeSchemaAndSelectorMerge(t *testing.T) {
	srv := newSEPFixtureServer(t)
	trawlHome := withTrawlHome(t)
	schemaPath := writeSchemaFile(t, trawlHome)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    5 * time.Second,
		tiers:      "http",
		schemaPath: schemaPath,
		// Flat selector: both overlaps with schema (title) AND adds a new
		// field (pre_snippet) that the schema doesn't know about.
		selectors: []string{"title=title", "pre_snippet=#preamble p"},
	}

	if err := runScrape(context.Background(), srv.URL+"/entries/test/", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	records := readJSONL(t, outputFile)
	rec := records[0]

	// Schema should have won on 'title' — its value is the h1 text, not
	// the <title>-tag text.
	if rec.Extracted["title"] != "Test Entry" {
		t.Errorf("title = %v, want schema-extracted 'Test Entry'", rec.Extracted["title"])
	}
	// Flat --selector value survives for the non-colliding key.
	if _, ok := rec.Extracted["pre_snippet"]; !ok {
		t.Error("flat selector key pre_snippet missing from merged output")
	}
}

// TestScrapeSchemaLoadFails verifies that a bad schema path fails
// loudly at command start rather than silently producing empty output.
func TestScrapeSchemaLoadFails(t *testing.T) {
	srv := newSEPFixtureServer(t)
	trawlHome := withTrawlHome(t)

	opts := scrapeOpts{
		outputPath: filepath.Join(trawlHome, "out.jsonl"),
		timeout:    5 * time.Second,
		tiers:      "http",
		schemaPath: filepath.Join(trawlHome, "does-not-exist.yaml"),
	}

	err := runScrape(context.Background(), srv.URL+"/entries/test/", opts)
	if err == nil {
		t.Fatal("expected error for missing schema file")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error %q should mention schema", err)
	}
}
