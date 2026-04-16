package main

import (
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/spf13/cobra"
)

// pdfFlags bundles the CLI knobs that tune the PDF engine's tier
// behavior. Zero value means "Tier 1+2 only, no OCR" — the
// conservative default that works against any environment with
// poppler-utils installed.
type pdfFlags struct {
	ocr         bool
	ocrLang     string
	pdfMaxPages int
}

// toJob translates the cobra-bound flags into the job-package
// PDFOpts used by Run/RunOne.
func (p pdfFlags) toJob() job.PDFOpts {
	return job.PDFOpts{
		OCR:      p.ocr,
		OCRLang:  p.ocrLang,
		MaxPages: p.pdfMaxPages,
	}
}

// registerPDFFlags wires the PDF-engine flags onto a cobra command.
// Called from scrape, batch, crawl so all three share the same
// surface.
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
