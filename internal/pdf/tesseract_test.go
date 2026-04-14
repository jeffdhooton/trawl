package pdf

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTier3EscalatesWhenTier1And2BothStub(t *testing.T) {
	// Both pdftotext invocations return stub; --ocr is on; OCR returns
	// substantive text. Verify Extract picks Tier 3.
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": {
			{out: []byte("\f")},
			{out: []byte("\f")},
		},
		"pdfinfo": once(mockResponse{out: []byte("Pages: 2\n")}),
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()
	defer overrideRunOCR(func(_ context.Context, _ []byte, opts Opts) ([]Page, error) {
		if opts.OCRLang != "eng" {
			t.Errorf("OCRLang passed through: want eng, got %q", opts.OCRLang)
		}
		return []Page{
			{Number: 1, Text: "OCR'd page one with enough text to clear the stub threshold."},
			{Number: 2, Text: "OCR'd page two text."},
		}, nil
	})()

	md, info, err := Extract(validPDFBody(), Opts{UseOCR: true, OCRLang: "eng"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if info.ExtractorTier != "tesseract" {
		t.Errorf("ExtractorTier: want tesseract, got %q", info.ExtractorTier)
	}
	if !info.UsedOCR {
		t.Error("UsedOCR: want true")
	}
	if info.HasTextLayer {
		t.Error("HasTextLayer: want false for OCR-served docs")
	}
	if !strings.Contains(string(md), "page one") || !strings.Contains(string(md), "page two") {
		t.Errorf("markdown missing page text: %q", md)
	}
	if len(info.Pages) != 2 {
		t.Errorf("Pages: want 2, got %d", len(info.Pages))
	}
}

func TestTier3NotInvokedWhenOCRDisabled(t *testing.T) {
	// Both pdftotext invocations return stub, --ocr is OFF → terminal
	// ErrEmptyExtraction. Verify runOCR was never called.
	ocrCalls := 0
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": {
			{out: []byte("\f")},
			{out: []byte("\f")},
		},
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()
	defer overrideRunOCR(func(context.Context, []byte, Opts) ([]Page, error) {
		ocrCalls++
		return nil, errors.New("should not be called")
	})()

	_, _, err := Extract(validPDFBody(), Opts{UseOCR: false})
	if !errors.Is(err, ErrEmptyExtraction) {
		t.Errorf("want ErrEmptyExtraction, got %v", err)
	}
	if ocrCalls != 0 {
		t.Errorf("runOCR should not be invoked when UseOCR=false; got %d calls", ocrCalls)
	}
}

func TestTier3FailsWithOCRError(t *testing.T) {
	// pdftoppm / tesseract error propagates up as the Extract return.
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": {
			{out: []byte("\f")},
			{out: []byte("\f")},
		},
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()
	defer overrideRunOCR(func(context.Context, []byte, Opts) ([]Page, error) {
		return nil, errors.New("pdftoppm: exit status 1: damaged")
	})()

	_, _, err := Extract(validPDFBody(), Opts{UseOCR: true})
	if err == nil {
		t.Fatal("want error from Tier 3 failure")
	}
	if !strings.Contains(err.Error(), "pdftoppm") {
		t.Errorf("error should mention pdftoppm, got %v", err)
	}
}

func TestTier3OCRProducesEmpty(t *testing.T) {
	// OCR returned pages but all page text is stub (whitespace). Should
	// terminate with ErrEmptyExtraction rather than falsely "succeed."
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": {
			{out: []byte("\f")},
			{out: []byte("\f")},
		},
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()
	defer overrideRunOCR(func(context.Context, []byte, Opts) ([]Page, error) {
		return []Page{
			{Number: 1, Text: "  "},
			{Number: 2, Text: "\n\n"},
		}, nil
	})()

	_, _, err := Extract(validPDFBody(), Opts{UseOCR: true})
	if !errors.Is(err, ErrEmptyExtraction) {
		t.Errorf("want ErrEmptyExtraction, got %v", err)
	}
}

func TestStitchOCRPages(t *testing.T) {
	pages := []Page{
		{Number: 1, Text: "page one"},
		{Number: 2, Text: "page two"},
		{Number: 3, Text: "page three"},
	}
	got := stitchOCRPages(pages)
	want := "page one\fpage two\fpage three"
	if string(got) != want {
		t.Errorf("stitch: want %q, got %q", want, got)
	}
}

func TestStitchOCRPagesEmpty(t *testing.T) {
	if stitchOCRPages(nil) != nil {
		t.Error("stitch(nil) should return nil")
	}
}

func TestSortFilesByPageNumber(t *testing.T) {
	files := []string{
		"/tmp/foo/page-10.png",
		"/tmp/foo/page-2.png",
		"/tmp/foo/page-1.png",
		"/tmp/foo/page-20.png",
	}
	sortFilesByPageNumber(files)
	want := []string{
		"/tmp/foo/page-1.png",
		"/tmp/foo/page-2.png",
		"/tmp/foo/page-10.png",
		"/tmp/foo/page-20.png",
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("index %d: want %s, got %s", i, want[i], files[i])
		}
	}
}

func TestSortFilesPaddedAndUnpadded(t *testing.T) {
	// Mixed zero-padded and non-padded shouldn't happen in practice
	// (pdftoppm is consistent within one invocation) but verify
	// numeric sort handles it.
	files := []string{
		"/tmp/foo/page-003.png",
		"/tmp/foo/page-1.png",
		"/tmp/foo/page-002.png",
	}
	sortFilesByPageNumber(files)
	if pageNumberFrom(files[0]) != 1 || pageNumberFrom(files[1]) != 2 || pageNumberFrom(files[2]) != 3 {
		t.Errorf("numeric sort failed: %v", files)
	}
}

// TestTier3Live exercises pdftoppm + tesseract end-to-end. Skipped
// when either binary is missing. Uses the Phase 2 minimal-PDF generator
// which produces a text-layer doc; OCR should still work by
// rasterizing and reading the text back, producing the same content
// (albeit with some OCR noise).
func TestTier3Live(t *testing.T) {
	if !HasPdftotext() {
		t.Skip("pdftotext not on PATH")
	}
	if !OCRAvailable() {
		t.Skip("OCR toolchain (pdftoppm + tesseract) not on PATH")
	}

	// Force Tier 1+2 to "fail" by giving text that's too short, or we
	// construct a harder test: substitute runPdftotext to return stub,
	// forcing Tier 3 on a live PDF.
	cleanup := mockRunPdftotextStub(t)
	defer cleanup()

	body := MinimalTestPDF("Phase 6 live OCR smoke test marker.")
	md, info, err := Extract(body, Opts{UseOCR: true, OCRLang: "eng", Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if info.ExtractorTier != "tesseract" {
		t.Errorf("ExtractorTier: want tesseract, got %q", info.ExtractorTier)
	}
	if !info.UsedOCR {
		t.Error("UsedOCR: want true")
	}
	if info.HasTextLayer {
		t.Error("HasTextLayer: want false")
	}
	// OCR isn't exact — match on a recognizable substring rather than
	// the full marker. Helvetica 12pt at 300 DPI is near-perfect for
	// clean text but case and punctuation drift are still possible.
	lower := strings.ToLower(string(md))
	if !strings.Contains(lower, "phase") || !strings.Contains(lower, "ocr") {
		t.Errorf("OCR output missing expected keywords: %q", md)
	}
}

// overrideRunOCR swaps the package-level runOCR var for the duration of
// a test and returns a restore function to defer.
func overrideRunOCR(fn func(context.Context, []byte, Opts) ([]Page, error)) func() {
	orig := runOCR
	runOCR = fn
	return func() { runOCR = orig }
}

// mockRunPdftotextStub forces pdftotext calls to return stub output so
// the live OCR test can observe the Tier 3 escalation path without
// changing the real PDF bytes. Leaves other binaries (pdftoppm,
// tesseract) alone so they run for real.
func mockRunPdftotextStub(t *testing.T) func() {
	t.Helper()
	orig := runCmd
	runCmd = func(ctx context.Context, timeout time.Duration, name string, stdin []byte, args ...string) ([]byte, error) {
		if name == "pdftotext" {
			return []byte("\f"), nil
		}
		return orig(ctx, timeout, name, stdin, args...)
	}
	return func() { runCmd = orig }
}
