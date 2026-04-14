// Package pdf transforms PDF bytes into extracted markdown and structured
// metadata by shelling out to user-installed binaries (pdftotext, tesseract,
// pdftoppm, pdfinfo). It is NOT an engine.Engine — the HTTP engine already
// fetched the PDF; this package is a post-fetch body transformer, parallel
// to internal/extract.
//
// See docs/PDF.md for the full design rationale, including why shell-out
// preserves the no-CGO rule from SPEC §7 and why a standalone Rust PDF
// tool was considered and declined.
//
// Public entry point is Extract(body, opts). Binary detection lives in
// detect.go; low-level exec helpers live in tools.go. Tier implementations
// land in later phases — Phase 1 is plumbing only.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"time"
)

// Opts configures an Extract call. Zero value is a valid "default" that
// requires pdftotext to be on PATH and disables OCR.
type Opts struct {
	// UseOCR enables Tier 3 (tesseract) when Tiers 1+2 return empty. Off
	// by default; set by --ocr on the command line.
	UseOCR bool
	// OCRLang is the tesseract language pack code (e.g. "eng", "eng+deu").
	// Only meaningful when UseOCR is true. Extract substitutes "eng" when
	// empty.
	OCRLang string
	// MaxPages caps the number of pages that get OCR'd. Tiers 1+2 always
	// process the whole document; Tier 3 respects this cap because each
	// OCR page costs seconds. Zero means no cap — use the CLI's
	// --pdf-max-pages flag (default 50) to bound long scanned docs.
	MaxPages int
	// Timeout is the wall-clock deadline for a single Extract call. Zero
	// means "no timeout" — the caller's context bounds the work. Recommended:
	// set a value that leaves headroom below the caller's own deadline so a
	// slow pdftotext doesn't monopolize the budget.
	Timeout time.Duration
}


// Info is the structured metadata extracted alongside the text. Mirrors
// output.PDFInfo (cmd/trawl populates that field from this struct) but
// lives here so the package has no dependency on output.
type Info struct {
	PageCount     int
	Title         string
	Author        string
	CreatedAt     time.Time
	HasTextLayer  bool
	UsedOCR       bool
	ExtractorTier string // "pdftotext" | "pdftotext-layout" | "tesseract"
	Pages         []Page
}

// Page is a single extracted page with its 1-based page number.
type Page struct {
	Number int
	Text   string
}

// Error sentinels. cmd/trawl inspects these to pick the right failure
// category and log message.
var (
	// ErrPdftotextMissing is returned when pdftotext isn't on PATH.
	// Callers classify the row as failure.CatPDFToolingMissing and log
	// InstallHint("pdftotext") once per run.
	ErrPdftotextMissing = errors.New("pdf: pdftotext binary not found on PATH")

	// ErrTesseractMissing is returned when Opts.UseOCR is set but
	// tesseract (or pdftoppm) is missing. Should ideally never reach
	// users — the cmd layer checks presence at command start and fails
	// early so --ocr jobs don't run 100 URLs in before failing.
	ErrTesseractMissing = errors.New("pdf: tesseract binary not found on PATH")

	// ErrPdftoppmMissing is returned when --ocr is set but pdftoppm is
	// missing (needed to rasterize pages before tesseract ingests them).
	// Like ErrTesseractMissing, the cmd layer prevents this reaching
	// users in normal flow.
	ErrPdftoppmMissing = errors.New("pdf: pdftoppm binary not found on PATH")

	// ErrEncrypted is returned for password-protected PDFs. User-supplied
	// passwords are a v2 feature per docs/PDF.md §3.
	ErrEncrypted = errors.New("pdf: document is password-protected")

	// ErrInvalidPDF is returned when the body doesn't start with a PDF
	// magic header. Guards against misclassified content types — servers
	// sometimes label HTML error pages as application/pdf.
	ErrInvalidPDF = errors.New("pdf: body is not a valid PDF")

	// ErrEmptyExtraction is returned when every tier returned empty text.
	// On a scanned PDF without --ocr this is expected; callers should
	// surface the "try --ocr" hint.
	ErrEmptyExtraction = errors.New("pdf: no text extracted (may be scanned; try --ocr)")
)

// InstallHint returns a platform-agnostic one-line install string for a
// given binary name. Returns "" for unknown names so callers can branch
// without hard-coding the known set.
func InstallHint(binary string) string {
	switch binary {
	case "pdftotext", "pdftoppm", "pdfinfo":
		return "install poppler-utils: 'brew install poppler' (macOS) or 'apt install poppler-utils' (Debian/Ubuntu)"
	case "tesseract":
		return "install tesseract: 'brew install tesseract' (macOS) or 'apt install tesseract-ocr' (Debian/Ubuntu)"
	default:
		return ""
	}
}

// pdfMagic is the byte prefix of every conforming PDF file. RFC 3778 §2.1
// formalizes "%PDF-<version>" as the required header; servers occasionally
// label HTML error pages as application/pdf, so we guard.
var pdfMagic = []byte("%PDF-")

// Extract transforms PDF bytes into markdown text plus structured Info.
//
// Tier ladder (Phases 2, 3, 6):
//
//	Tier 1: pdftotext (plain)         — fast, always tried first
//	Tier 2: pdftotext -layout         — escalated on stub Tier 1 output
//	Tier 3: pdftoppm + tesseract OCR  — only when Opts.UseOCR is set
//
// When every tier yields stub output, ErrEmptyExtraction is returned
// with the "try --ocr" hint. When --ocr is already on and Tier 3 also
// fails, the same error is returned (callers distinguish by the
// UseOCR flag they passed in).
func Extract(body []byte, opts Opts) ([]byte, *Info, error) {
	if !bytes.HasPrefix(body, pdfMagic) {
		return nil, nil, ErrInvalidPDF
	}
	if !HasPdftotext() {
		return nil, nil, ErrPdftotextMissing
	}
	if opts.UseOCR {
		if !HasPdftoppm() {
			return nil, nil, ErrPdftoppmMissing
		}
		if !HasTesseract() {
			return nil, nil, ErrTesseractMissing
		}
	}

	ctx := context.Background()
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// Tier 1: plain pdftotext.
	out, err := runPdftotext(ctx, body, modePlain)
	if err != nil {
		return nil, nil, err
	}
	tier := "pdftotext"

	// Tier 2: escalate to -layout when Tier 1 output is stub. Layout
	// mode preserves bounding-box positioning, which recovers text in
	// PDFs where plain mode misinterprets the content stream (common
	// for multi-column papers with non-standard Tj ordering).
	if isStubExtraction(out) {
		layoutOut, layoutErr := runPdftotext(ctx, body, modeLayout)
		if layoutErr != nil {
			// A layout-specific error (e.g. ErrEncrypted surfaced now
			// when Tier 1 somehow didn't) propagates. If both tiers
			// returned unsalvageable errors, Tier 1's is the
			// authoritative one — surface that.
			return nil, nil, layoutErr
		}
		if !isStubExtraction(layoutOut) {
			out = layoutOut
			tier = "pdftotext-layout"
		} else if opts.UseOCR {
			// Tier 3: OCR. Rasterize pages with pdftoppm, run
			// tesseract on each image, stitch the per-page text back
			// into a form-feed-separated byte stream matching
			// pdftotext's output shape.
			ocrPages, ocrErr := runOCR(ctx, body, opts)
			if ocrErr != nil {
				return nil, nil, ocrErr
			}
			if len(ocrPages) == 0 {
				return nil, nil, ErrEmptyExtraction
			}
			out = stitchOCRPages(ocrPages)
			if isStubExtraction(out) {
				return nil, nil, ErrEmptyExtraction
			}
			tier = "tesseract"
		} else {
			// No text layer, no --ocr. Terminal failure with the
			// "try --ocr" hint baked into the sentinel error.
			return nil, nil, ErrEmptyExtraction
		}
	}

	info := runPdfinfo(ctx, body)
	info.ExtractorTier = tier
	info.HasTextLayer = tier != "tesseract"
	info.UsedOCR = tier == "tesseract"
	info.Pages = splitPages(out)
	if info.PageCount == 0 {
		info.PageCount = len(info.Pages)
	}

	return renderMarkdown(out), info, nil
}
