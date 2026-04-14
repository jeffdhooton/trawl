package main

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/jeffdhooton/trawl/internal/pdf"
)

// pdfFlags bundles the CLI knobs that tune the PDF engine's tier
// behavior. Zero value means "Tier 1+2 only, no OCR" — the conservative
// default that works against any environment with poppler-utils
// installed.
type pdfFlags struct {
	// ocr enables Tier 3 (tesseract OCR) when Tier 1+2 return empty.
	// Requires tesseract and pdftoppm on PATH; command-start validation
	// fails fast when they're missing so batch jobs don't run dozens of
	// URLs in before discovering the environment can't support OCR.
	ocr bool
	// ocrLang is the tesseract language pack code (e.g. "eng",
	// "eng+deu"). Default "eng". Only meaningful when --ocr is set.
	ocrLang string
	// pdfMaxPages caps the number of pages OCR'd per document. Tier
	// 1+2 always process the full document; the cap only protects
	// against misdirected OCR runs on very long scanned docs. Default
	// 50 — balances coverage and CPU cost.
	pdfMaxPages int
}

// registerPDFFlags wires the PDF-engine flags onto a cobra command.
// Called from scrape, batch, crawl so all three share the same surface.
func registerPDFFlags(cmd *cobra.Command, p *pdfFlags) {
	cmd.Flags().BoolVar(&p.ocr, "ocr", false,
		"enable Tier 3 OCR via tesseract for scanned PDFs where pdftotext "+
			"finds no text layer. Requires tesseract and pdftoppm installed. Off by default.")
	cmd.Flags().StringVar(&p.ocrLang, "ocr-lang", "eng",
		"tesseract language pack code (e.g. 'eng', 'eng+deu'). Only meaningful with --ocr.")
	cmd.Flags().IntVar(&p.pdfMaxPages, "pdf-max-pages", 50,
		"max pages to OCR per document. Tier 1+2 always process the full document. "+
			"0 = unlimited. Only applies when --ocr is on.")
}

// validatePDFFlags checks the environment can support the requested PDF
// features BEFORE the command starts fetching URLs. Fails fast on missing
// OCR binaries when --ocr is set; batch jobs running 100 URLs before
// discovering tesseract is missing is a bad user experience.
//
// A missing pdftotext is NOT a hard-fail here — trawl stays useful for
// HTML targets even on machines without poppler-utils, and PDF soft-fails
// per-row with a clear install hint (see transformPDFIfNeeded).
func validatePDFFlags(p pdfFlags) error {
	if !p.ocr {
		return nil
	}
	missing := pdf.OCRMissingBinaries()
	if len(missing) == 0 {
		return nil
	}
	hint := pdf.InstallHint(missing[0])
	return fmt.Errorf("--ocr requires %s to be installed — %s",
		strings.Join(missing, " and "), hint)
}

// applyPDFFlags builds the internal/pdf.Opts from the CLI flags. Returns
// a zero-valued Opts when --ocr isn't set so downstream code doesn't
// need to branch.
func applyPDFFlags(p pdfFlags) pdf.Opts {
	return pdf.Opts{
		UseOCR:   p.ocr,
		OCRLang:  p.ocrLang,
		MaxPages: p.pdfMaxPages,
	}
}

// logPDFConfig emits a single info-level line when non-default PDF
// settings are active. Keeps the startup log honest about what the job
// is actually doing — mirrors logEvasion / logProxy.
func logPDFConfig(p pdfFlags) {
	if !p.ocr {
		return
	}
	log.Info().
		Bool("ocr", true).
		Str("ocr_lang", p.ocrLang).
		Int("max_pages", p.pdfMaxPages).
		Msg("PDF OCR enabled (Tier 3)")
}
