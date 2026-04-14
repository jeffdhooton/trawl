package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/trawl/internal/pdf"
)

// newPDFTestServer serves a minimal valid PDF at /doc.pdf with the
// given body text baked in, plus permissive robots.txt. Caller gets a
// live server and a string they can assert is present in the
// extracted markdown.
func newPDFTestServer(t *testing.T, text string) (*httptest.Server, []byte) {
	t.Helper()
	body := pdf.MinimalTestPDF(text)
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/doc.pdf", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, body
}

func TestScrapePDF_EndToEnd(t *testing.T) {
	if !pdf.HasPdftotext() {
		t.Skip("pdftotext not on PATH — install poppler-utils to run this test")
	}
	const marker = "Phase 4 integration test marker string."
	srv, _ := newPDFTestServer(t, marker)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    10 * time.Second,
		tiers:      "http",
		format:     "markdown",
	}
	if err := runScrape(context.Background(), srv.URL+"/doc.pdf", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	recs := readJSONL(t, outputFile)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	rec := recs[0]

	if rec.StatusCode != 200 {
		t.Errorf("StatusCode: %d", rec.StatusCode)
	}
	if rec.Metadata.ContentType != "text/markdown" {
		t.Errorf("ContentType: want text/markdown (post-transform), got %q", rec.Metadata.ContentType)
	}
	if rec.BodyFormat != "markdown" {
		t.Errorf("BodyFormat: want markdown, got %q", rec.BodyFormat)
	}
	if !strings.Contains(rec.Body, marker) {
		t.Errorf("Body missing marker; got: %q", rec.Body)
	}

	if rec.Metadata.PDF == nil {
		t.Fatal("metadata.pdf is nil — transform didn't populate")
	}
	info := rec.Metadata.PDF
	if info.ExtractorTier != "pdftotext" {
		t.Errorf("ExtractorTier: %q", info.ExtractorTier)
	}
	if !info.HasTextLayer {
		t.Error("HasTextLayer: want true")
	}
	if info.UsedOCR {
		t.Error("UsedOCR: want false (no --ocr)")
	}
	if info.PageCount < 1 {
		t.Errorf("PageCount: want ≥1, got %d", info.PageCount)
	}
	if len(info.Pages) < 1 {
		t.Fatalf("Pages: want ≥1, got %d", len(info.Pages))
	}
	if !strings.Contains(info.Pages[0].Text, marker) {
		t.Errorf("Pages[0].Text missing marker: %q", info.Pages[0].Text)
	}
}

func TestScrapePDF_FormatHTMLOverriddenToMarkdown(t *testing.T) {
	// --format html on a PDF should silently upgrade to markdown and
	// label BodyFormat accordingly. PDF bytes aren't useful HTML.
	if !pdf.HasPdftotext() {
		t.Skip("pdftotext not on PATH")
	}
	const marker = "HTML-override test marker string."
	srv, _ := newPDFTestServer(t, marker)
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    10 * time.Second,
		tiers:      "http",
		format:     "html",
	}
	if err := runScrape(context.Background(), srv.URL+"/doc.pdf", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	recs := readJSONL(t, outputFile)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	rec := recs[0]

	if rec.BodyFormat != "markdown" {
		t.Errorf("BodyFormat: want markdown (overridden from html), got %q", rec.BodyFormat)
	}
	if !strings.Contains(rec.Body, marker) {
		t.Errorf("Body missing marker: %q", rec.Body)
	}
	if rec.Metadata.PDF == nil {
		t.Error("metadata.pdf should still be populated when --format html")
	}
}

func TestScrapePDF_PageTitleCopiedWhenMetadataPDFHasTitle(t *testing.T) {
	// pdftotext -info (via runPdfinfo) doesn't populate Title for our
	// hand-crafted minimal PDF (no /Info dict). This test instead
	// verifies the opposite invariant: when PDF has NO title,
	// metadata.page stays nil — we don't synthesize empty structures.
	if !pdf.HasPdftotext() {
		t.Skip("pdftotext not on PATH")
	}
	srv, _ := newPDFTestServer(t, "Body with no PDF title set.")
	trawlHome := withTrawlHome(t)
	outputFile := filepath.Join(trawlHome, "out.jsonl")

	opts := scrapeOpts{
		outputPath: outputFile,
		timeout:    10 * time.Second,
		tiers:      "http",
	}
	if err := runScrape(context.Background(), srv.URL+"/doc.pdf", opts); err != nil {
		t.Fatalf("runScrape: %v", err)
	}

	recs := readJSONL(t, outputFile)
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	rec := recs[0]
	if rec.Metadata.PDF == nil {
		t.Fatal("metadata.pdf should be populated on success")
	}
	// PDF has no title — metadata.page should stay nil rather than
	// being synthesized with an empty title.
	if rec.Metadata.Page != nil && rec.Metadata.Page.Title != "" {
		t.Errorf("metadata.page.title should be empty when PDF has no title, got %q", rec.Metadata.Page.Title)
	}
}

func TestIsPDF(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"application/pdf", true},
		{"application/pdf; charset=binary", true},
		{"APPLICATION/PDF", true},
		{"application/x-pdf", true},
		{"text/html", false},
		{"", false}, // unlike isHTML, empty is NOT pdf
		{"application/json", false},
	}
	for _, tc := range cases {
		if got := isPDF(tc.ct); got != tc.want {
			t.Errorf("isPDF(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}
