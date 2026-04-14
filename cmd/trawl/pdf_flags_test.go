package main

import (
	"strings"
	"testing"

	"github.com/jeffdhooton/trawl/internal/pdf"
)

func TestValidatePDFFlags_OCRDisabled_AlwaysPasses(t *testing.T) {
	if err := validatePDFFlags(pdfFlags{ocr: false}); err != nil {
		t.Errorf("want nil error when --ocr disabled, got %v", err)
	}
}

func TestValidatePDFFlags_OCREnabled_FailsWhenBinariesMissing(t *testing.T) {
	// Strip PATH so no binaries are discoverable, then reset the pdf
	// package's detection cache so it actually re-runs lookPath.
	t.Setenv("PATH", "")
	pdf.ResetDetectionForTest()
	t.Cleanup(pdf.ResetDetectionForTest)

	err := validatePDFFlags(pdfFlags{ocr: true, ocrLang: "eng", pdfMaxPages: 50})
	if err == nil {
		t.Fatal("want error when --ocr set and binaries missing")
	}
	msg := err.Error()
	// Error should name the missing binary AND the install hint.
	if !strings.Contains(msg, "tesseract") || !strings.Contains(msg, "pdftoppm") {
		t.Errorf("error should name both missing binaries; got %q", msg)
	}
	if !strings.Contains(msg, "brew") && !strings.Contains(msg, "apt") {
		t.Errorf("error should include install hint; got %q", msg)
	}
}

func TestApplyPDFFlags_BuildsOpts(t *testing.T) {
	got := applyPDFFlags(pdfFlags{ocr: true, ocrLang: "eng+deu", pdfMaxPages: 10})
	if !got.UseOCR {
		t.Error("UseOCR: want true")
	}
	if got.OCRLang != "eng+deu" {
		t.Errorf("OCRLang: %q", got.OCRLang)
	}
	if got.MaxPages != 10 {
		t.Errorf("MaxPages: %d", got.MaxPages)
	}

	// Zero value maps correctly.
	zero := applyPDFFlags(pdfFlags{})
	if zero.UseOCR {
		t.Error("zero-value UseOCR should be false")
	}
}
