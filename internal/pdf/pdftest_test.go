package pdf

import (
	"strings"
	"testing"
	"time"
)

func TestMinimalTestPDFValidMagic(t *testing.T) {
	body := MinimalTestPDF("hello")
	if !strings.HasPrefix(string(body), "%PDF-1.4") {
		head := string(body[:20])
		t.Errorf("missing PDF magic header; first 20 bytes: %q", head)
	}
	if !strings.Contains(string(body), "%%EOF") {
		t.Error("missing EOF trailer")
	}
}

func TestMinimalTestPDFWithOptsZeroValue(t *testing.T) {
	// Zero value should produce a valid single-page PDF with placeholder.
	body := MinimalTestPDFWithOpts(TestPDFOpts{})
	if !strings.HasPrefix(string(body), "%PDF-1.4") {
		t.Error("zero-value opts should still produce valid PDF")
	}
}

// TestFixtureLiveMetadataRich drives a real pdfinfo invocation against
// a PDF with /Info dict entries. Verifies the full metadata extraction
// path — title/author parsing end-to-end.
func TestFixtureLiveMetadataRich(t *testing.T) {
	if !HasPdftotext() {
		t.Skip("pdftotext not on PATH")
	}
	if !HasPdfinfo() {
		t.Skip("pdfinfo not on PATH")
	}

	body := MinimalTestPDFWithOpts(TestPDFOpts{
		Title:  "Phase 7 Fixture Title",
		Author: "Jeff (test author)",
		Pages:  []string{"Body content that exercises the extraction pipeline end-to-end."},
	})

	_, info, err := Extract(body, Opts{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if info.Title != "Phase 7 Fixture Title" {
		t.Errorf("Title: want 'Phase 7 Fixture Title', got %q", info.Title)
	}
	if info.Author != "Jeff (test author)" {
		t.Errorf("Author: want 'Jeff (test author)', got %q", info.Author)
	}
	if info.PageCount != 1 {
		t.Errorf("PageCount: want 1, got %d", info.PageCount)
	}
}

// TestFixtureLiveMultiPage exercises the page-splitting logic with a
// real multi-page PDF. Verifies pdftotext's form-feed separator
// convention matches what splitPages expects.
func TestFixtureLiveMultiPage(t *testing.T) {
	if !HasPdftotext() {
		t.Skip("pdftotext not on PATH")
	}

	body := MinimalTestPDFWithOpts(TestPDFOpts{
		Pages: []string{
			"First page content marker alpha.",
			"Second page content marker beta.",
			"Third page content marker gamma.",
		},
	})

	md, info, err := Extract(body, Opts{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if info.PageCount != 3 {
		t.Errorf("PageCount: want 3, got %d", info.PageCount)
	}
	if len(info.Pages) != 3 {
		t.Fatalf("Pages: want 3, got %d", len(info.Pages))
	}
	if !strings.Contains(info.Pages[0].Text, "alpha") {
		t.Errorf("page 1 missing alpha: %q", info.Pages[0].Text)
	}
	if !strings.Contains(info.Pages[1].Text, "beta") {
		t.Errorf("page 2 missing beta: %q", info.Pages[1].Text)
	}
	if !strings.Contains(info.Pages[2].Text, "gamma") {
		t.Errorf("page 3 missing gamma: %q", info.Pages[2].Text)
	}

	// All three markers should appear in the markdown body.
	for _, marker := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(string(md), marker) {
			t.Errorf("markdown missing %q", marker)
		}
	}
}

// TestFixtureEscapesParens ensures the PDF-string escaping is correct.
// A naive generator that didn't escape parens would produce an
// invalid PDF pdftotext would refuse to parse.
func TestFixtureEscapesParens(t *testing.T) {
	if !HasPdftotext() {
		t.Skip("pdftotext not on PATH")
	}
	body := MinimalTestPDF("text with (parens) and \\backslashes\\ that must escape")
	_, _, err := Extract(body, Opts{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Extract on paren-rich text: %v", err)
	}
}
