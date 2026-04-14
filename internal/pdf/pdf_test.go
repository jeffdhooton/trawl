package pdf

import (
	"errors"
	"testing"
)

func TestExtractInvalidPDF(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"empty", []byte{}},
		{"too short", []byte("%PDF")},
		{"html masquerading as pdf", []byte("<html><body>not a pdf</body></html>")},
		{"random bytes", []byte{0x00, 0x01, 0x02, 0x03, 0x04}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Extract(tc.body, Opts{})
			if !errors.Is(err, ErrInvalidPDF) {
				t.Errorf("want ErrInvalidPDF, got %v", err)
			}
		})
	}
}

func TestExtractMissingPdftotext(t *testing.T) {
	defer overrideLookPath(func(string) bool { return false })()
	_, _, err := Extract(validPDFBody(), Opts{})
	if !errors.Is(err, ErrPdftotextMissing) {
		t.Errorf("want ErrPdftotextMissing, got %v", err)
	}
}

func TestExtractOCRMissingPdftoppm(t *testing.T) {
	// pdftotext present, pdftoppm absent, tesseract present — pdftoppm
	// check fires first (it's the rasterizer).
	defer overrideLookPath(func(name string) bool {
		return name == "pdftotext" || name == "tesseract"
	})()
	_, _, err := Extract(validPDFBody(), Opts{UseOCR: true})
	if !errors.Is(err, ErrPdftoppmMissing) {
		t.Errorf("want ErrPdftoppmMissing, got %v", err)
	}
}

func TestExtractOCRMissingTesseract(t *testing.T) {
	// pdftotext + pdftoppm present, tesseract missing.
	defer overrideLookPath(func(name string) bool {
		return name == "pdftotext" || name == "pdftoppm"
	})()
	_, _, err := Extract(validPDFBody(), Opts{UseOCR: true})
	if !errors.Is(err, ErrTesseractMissing) {
		t.Errorf("want ErrTesseractMissing, got %v", err)
	}
}

func TestExtractWithoutOCRDoesNotCheckTesseract(t *testing.T) {
	// Tesseract missing but UseOCR false — should NOT return
	// ErrTesseractMissing. Falls through to "not implemented" because
	// Phase 1 is a skeleton.
	defer overrideLookPath(func(name string) bool {
		return name == "pdftotext"
	})()
	_, _, err := Extract(validPDFBody(), Opts{UseOCR: false})
	if errors.Is(err, ErrTesseractMissing) {
		t.Errorf("should not probe tesseract when UseOCR=false; got %v", err)
	}
}

func TestInstallHint(t *testing.T) {
	for _, bin := range []string{"pdftotext", "tesseract", "pdfinfo", "pdftoppm"} {
		if InstallHint(bin) == "" {
			t.Errorf("InstallHint(%q) returned empty", bin)
		}
	}
	if InstallHint("unknown-binary") != "" {
		t.Error("InstallHint(unknown) should return empty string")
	}
}

func TestDetectionCaches(t *testing.T) {
	calls := 0
	defer overrideLookPath(func(string) bool {
		calls++
		return false
	})()
	HasPdftotext()
	HasPdftotext()
	HasPdftotext()
	if calls != 1 {
		t.Errorf("HasPdftotext should cache; got %d lookPath calls, want 1", calls)
	}
}

func TestOCRMissingBinaries(t *testing.T) {
	// Both missing → both listed.
	defer overrideLookPath(func(string) bool { return false })()
	missing := OCRMissingBinaries()
	if len(missing) != 2 {
		t.Fatalf("want 2 missing, got %v", missing)
	}
	if missing[0] != "pdftoppm" || missing[1] != "tesseract" {
		t.Errorf("unexpected order or names: %v", missing)
	}
	if OCRAvailable() {
		t.Error("OCRAvailable should be false when both missing")
	}
}

func TestOCRAvailableWhenBothPresent(t *testing.T) {
	defer overrideLookPath(func(name string) bool {
		return name == "pdftoppm" || name == "tesseract"
	})()
	if !OCRAvailable() {
		t.Error("OCRAvailable should be true when both binaries present")
	}
	if len(OCRMissingBinaries()) != 0 {
		t.Errorf("want empty missing list, got %v", OCRMissingBinaries())
	}
}

// validPDFBody returns bytes that start with the PDF magic header so
// Extract's invalid-PDF guard passes. Not a functional PDF — Phase 1
// doesn't actually invoke pdftotext, only the guard and detection path.
func validPDFBody() []byte {
	body := []byte("%PDF-1.4\n")
	body = append(body, make([]byte, 100)...)
	return body
}

// overrideLookPath swaps lookPath for the duration of a test and returns
// a restore function. Also calls ResetDetectionForTest so the new override
// fires on the next Has* call (sync.Once would otherwise latch the old
// result from a prior test).
func overrideLookPath(fn func(string) bool) func() {
	orig := lookPath
	lookPath = fn
	ResetDetectionForTest()
	return func() {
		lookPath = orig
		ResetDetectionForTest()
	}
}
