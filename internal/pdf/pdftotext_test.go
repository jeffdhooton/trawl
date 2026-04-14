package pdf

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTier1ExtractsMarkdownAndPages(t *testing.T) {
	stubPdftotext := []byte("Page one body.\n\nSecond paragraph on page one.\n\f" +
		"Page two only has one line.\n\f" +
		"Page three wraps up.\n")
	stubPdfinfo := []byte(`Title:           Sample Doc
Author:          Jane Doe
Pages:           3
CreationDate:    Fri Mar 15 10:00:00 2024 UTC
`)

	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": once(mockResponse{out: stubPdftotext}),
		"pdfinfo":   once(mockResponse{out: stubPdfinfo}),
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	md, info, err := Extract(validPDFBody(), Opts{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	wantMD := "Page one body.\n\nSecond paragraph on page one.\n\n\n" +
		"Page two only has one line.\n\n\n" +
		"Page three wraps up.\n"
	if string(md) != wantMD {
		t.Errorf("markdown mismatch\nwant: %q\ngot:  %q", wantMD, md)
	}

	if info.ExtractorTier != "pdftotext" {
		t.Errorf("ExtractorTier: want pdftotext, got %q", info.ExtractorTier)
	}
	if !info.HasTextLayer {
		t.Error("HasTextLayer: want true")
	}
	if info.UsedOCR {
		t.Error("UsedOCR: want false for Tier 1")
	}
	if info.Title != "Sample Doc" {
		t.Errorf("Title: %q", info.Title)
	}
	if info.Author != "Jane Doe" {
		t.Errorf("Author: %q", info.Author)
	}
	if info.PageCount != 3 {
		t.Errorf("PageCount: want 3, got %d", info.PageCount)
	}
	if info.CreatedAt.Year() != 2024 {
		t.Errorf("CreatedAt year: want 2024, got %d", info.CreatedAt.Year())
	}

	if len(info.Pages) != 3 {
		t.Fatalf("Pages: want 3, got %d", len(info.Pages))
	}
	if info.Pages[0].Number != 1 || !strings.Contains(info.Pages[0].Text, "Page one") {
		t.Errorf("page 1 wrong: %+v", info.Pages[0])
	}
	if info.Pages[1].Number != 2 || !strings.Contains(info.Pages[1].Text, "Page two") {
		t.Errorf("page 2 wrong: %+v", info.Pages[1])
	}
	if info.Pages[2].Number != 3 || !strings.Contains(info.Pages[2].Text, "Page three") {
		t.Errorf("page 3 wrong: %+v", info.Pages[2])
	}
}

func TestTier1FallsBackToPageCountFromPages(t *testing.T) {
	// pdfinfo omits the Pages field; Extract should fall back to
	// counting split pages so PageCount is never zero on success.
	stubPdftotext := []byte("page 1\n\fpage 2\n\fpage 3\n")
	stubPdfinfo := []byte("Title: Untitled\n")

	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": once(mockResponse{out: stubPdftotext}),
		"pdfinfo":   once(mockResponse{out: stubPdfinfo}),
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	_, info, err := Extract(validPDFBody(), Opts{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if info.PageCount != 3 {
		t.Errorf("PageCount fallback: want 3, got %d", info.PageCount)
	}
}

func TestTier1EncryptedPDF(t *testing.T) {
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": once(mockResponse{err: errors.New("pdftotext: exit status 1: Command Line Error: Incorrect password")}),
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	_, _, err := Extract(validPDFBody(), Opts{})
	if !errors.Is(err, ErrEncrypted) {
		t.Errorf("want ErrEncrypted, got %v", err)
	}
}

func TestExtractEmptyAfterBothTiers(t *testing.T) {
	// Scanned PDF with no text layer: pdftotext plain returns stub,
	// pdftotext -layout also returns stub, so Extract terminates with
	// ErrEmptyExtraction (the "try --ocr" hint).
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": {
			{out: []byte("\f\f\f")},         // Tier 1 plain: stub
			{out: []byte("   \n \f \f \n")}, // Tier 2 layout: also stub
		},
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	_, _, err := Extract(validPDFBody(), Opts{})
	if !errors.Is(err, ErrEmptyExtraction) {
		t.Errorf("want ErrEmptyExtraction, got %v", err)
	}
}

func TestTier1DamagedPDFPropagatesError(t *testing.T) {
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": once(mockResponse{err: errors.New("pdftotext: exit status 1: Syntax Error: Couldn't find trailer dictionary")}),
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	_, _, err := Extract(validPDFBody(), Opts{})
	if err == nil {
		t.Fatal("want error for damaged PDF")
	}
	if errors.Is(err, ErrEncrypted) || errors.Is(err, ErrEmptyExtraction) {
		t.Errorf("wrong classification for damaged PDF: %v", err)
	}
	if !strings.Contains(err.Error(), "pdftotext") {
		t.Errorf("error should mention pdftotext: %v", err)
	}
}

func TestTier1SurvivesMissingPdfinfo(t *testing.T) {
	// pdftotext succeeds, pdfinfo absent. Info should have zero metadata
	// but still return a usable result — metadata is best-effort.
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": once(mockResponse{out: []byte("real content here that clearly exceeds stub threshold\n")}),
	})
	defer cleanup()
	// pdftotext present, pdfinfo absent.
	defer overrideLookPath(func(name string) bool {
		return name == "pdftotext"
	})()

	md, info, err := Extract(validPDFBody(), Opts{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(md) == 0 {
		t.Error("want non-empty markdown")
	}
	if info.Title != "" || info.Author != "" || !info.CreatedAt.IsZero() {
		t.Errorf("expected empty metadata when pdfinfo missing, got %+v", info)
	}
	if info.ExtractorTier != "pdftotext" {
		t.Errorf("ExtractorTier: %q", info.ExtractorTier)
	}
}

func TestTier2EscalatesOnStubTier1(t *testing.T) {
	// Tier 1 returns stub output; Tier 2 (-layout) recovers real
	// content. Verify the extractor tier flips to pdftotext-layout
	// and the Tier 2 output is what flows through.
	tier1Stub := []byte("\f\f")
	tier2Real := []byte("Substantive layout-mode output with enough bytes to clear the stub threshold.\n")

	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": {
			{out: tier1Stub},
			{out: tier2Real},
		},
		"pdfinfo": once(mockResponse{out: []byte("Pages: 1\n")}),
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	md, info, err := Extract(validPDFBody(), Opts{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if info.ExtractorTier != "pdftotext-layout" {
		t.Errorf("ExtractorTier: want pdftotext-layout, got %q", info.ExtractorTier)
	}
	if !strings.Contains(string(md), "layout-mode output") {
		t.Errorf("Tier 2 content missing from markdown: %q", md)
	}
	if info.PageCount != 1 {
		t.Errorf("PageCount: %d", info.PageCount)
	}
}

func TestTier2NotInvokedWhenTier1Wins(t *testing.T) {
	// Tier 1 produces substantive output → Tier 2 never runs. Verify
	// by queuing only one pdftotext response; a second invocation
	// would replay the last entry (mock behavior) but we assert on
	// ExtractorTier which distinguishes.
	tier1Good := []byte("Plain-mode output that's clearly above the stub threshold.\n")

	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": once(mockResponse{out: tier1Good}),
		"pdfinfo":   once(mockResponse{out: []byte("Pages: 1\n")}),
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	_, info, err := Extract(validPDFBody(), Opts{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if info.ExtractorTier != "pdftotext" {
		t.Errorf("ExtractorTier: want pdftotext (plain), got %q", info.ExtractorTier)
	}
}

func TestTier2PropagatesLayoutError(t *testing.T) {
	// Tier 1 returns stub; Tier 2 -layout errors out. The layout
	// error is the one that reaches the caller (it's the signal that
	// SOMETHING broke beyond "no text layer").
	cleanup := mockRunCmd(t, map[string][]mockResponse{
		"pdftotext": {
			{out: []byte("\f")},
			{err: errors.New("pdftotext: exit status 1: Incorrect password")},
		},
	})
	defer cleanup()
	defer overrideLookPath(func(string) bool { return true })()

	_, _, err := Extract(validPDFBody(), Opts{})
	if !errors.Is(err, ErrEncrypted) {
		t.Errorf("want ErrEncrypted from Tier 2, got %v", err)
	}
}

// TestTier1Live exercises the real pdftotext binary on a hand-crafted
// minimal PDF. Skipped when pdftotext isn't installed; Phase 7 replaces
// the inline generator with committed fixtures that cover multi-column,
// scanned, and encrypted cases.
func TestTier1Live(t *testing.T) {
	if !HasPdftotext() {
		t.Skip("pdftotext not on PATH")
	}
	body := MinimalTestPDF("Hello from the trawl PDF engine test fixture.")
	md, info, err := Extract(body, Opts{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(string(md), "Hello from the trawl PDF engine") {
		t.Errorf("want expected text in markdown, got %q", md)
	}
	if info.ExtractorTier != "pdftotext" {
		t.Errorf("ExtractorTier: %q", info.ExtractorTier)
	}
	if info.PageCount < 1 {
		t.Errorf("PageCount: want ≥1, got %d", info.PageCount)
	}
	if !info.HasTextLayer {
		t.Error("HasTextLayer: want true")
	}
}

func TestSplitPages(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []Page
	}{
		{
			name: "single page no separator",
			in:   []byte("just one page"),
			want: []Page{{Number: 1, Text: "just one page"}},
		},
		{
			name: "three pages",
			in:   []byte("one\ftwo\fthree"),
			want: []Page{
				{Number: 1, Text: "one"},
				{Number: 2, Text: "two"},
				{Number: 3, Text: "three"},
			},
		},
		{
			name: "trailing form feed stripped",
			in:   []byte("one\ftwo\f"),
			want: []Page{
				{Number: 1, Text: "one"},
				{Number: 2, Text: "two"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitPages(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len: want %d, got %d", len(tc.want), len(got))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("page %d: want %+v, got %+v", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func TestIsStubExtraction(t *testing.T) {
	cases := []struct {
		in   []byte
		stub bool
	}{
		{[]byte{}, true},
		{[]byte("\f\f\f"), true},
		{[]byte("   \n\t  "), true},
		{[]byte("short"), true}, // below 16 bytes
		{[]byte("this is clearly substantive content"), false},
	}
	for _, tc := range cases {
		if got := isStubExtraction(tc.in); got != tc.stub {
			t.Errorf("isStubExtraction(%q): want %v, got %v", tc.in, tc.stub, got)
		}
	}
}

func TestIsEncryptionError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("pdftotext: exit status 1: Incorrect password"), true},
		{errors.New("pdftotext: exit status 1: Command Line Error: Incorrect password"), true},
		{errors.New("pdftotext: pdf file is encrypted"), true},
		{errors.New("pdftotext: exit status 1: Syntax Error"), false},
		{errors.New("pdftotext: exit status 0"), false},
	}
	for i, tc := range cases {
		if got := isEncryptionError(tc.err); got != tc.want {
			t.Errorf("case %d (%v): want %v, got %v", i, tc.err, tc.want, got)
		}
	}
}

// mockResponse is a canned return value for a mocked runCmd call.
type mockResponse struct {
	out []byte
	err error
}

// mockRunCmd overrides runCmd to route calls by binary name to the
// responses map. Each binary maps to a slice of responses consumed
// in order — tests that invoke a binary multiple times (e.g. Tier 1
// then Tier 2 pdftotext) queue one response per call. If a binary
// runs out of queued responses the last one is replayed. Unexpected
// binaries fail the test.
func mockRunCmd(t *testing.T, responses map[string][]mockResponse) func() {
	t.Helper()
	orig := runCmd
	calls := make(map[string]int)
	runCmd = func(_ context.Context, _ time.Duration, name string, _ []byte, _ ...string) ([]byte, error) {
		seq, ok := responses[name]
		if !ok || len(seq) == 0 {
			t.Errorf("unexpected runCmd(%q) in test", name)
			return nil, errors.New("unexpected binary: " + name)
		}
		i := calls[name]
		if i >= len(seq) {
			i = len(seq) - 1
		}
		calls[name]++
		return seq[i].out, seq[i].err
	}
	return func() { runCmd = orig }
}

// once wraps a single response in the slice shape mockRunCmd expects.
// Most tests use once() for readability; tests that need per-call
// variation pass the slice directly.
func once(resp mockResponse) []mockResponse {
	return []mockResponse{resp}
}

