package pdf

import (
	"os/exec"
	"sync"
)

// Binary detection is lazy-cached per-process. Testing PATH on every PDF
// fetch would add a few ms of syscall overhead per row; stat-caching makes
// the common case (binary present, same path) free after first hit.
//
// The cache does NOT invalidate. Trawl is a short-lived CLI process — PATH
// changes during a run are not a supported scenario. If a user installs
// poppler-utils mid-run, restart the job. Tests that need a different
// outcome call ResetDetectionForTest.

var (
	pdftotextOnce sync.Once
	pdftotextHas  bool

	pdftoppmOnce sync.Once
	pdftoppmHas  bool

	pdfinfoOnce sync.Once
	pdfinfoHas  bool

	tesseractOnce sync.Once
	tesseractHas  bool
)

// HasPdftotext reports whether pdftotext is on PATH. Required for Tiers
// 1 and 2.
func HasPdftotext() bool {
	pdftotextOnce.Do(func() { pdftotextHas = lookPath("pdftotext") })
	return pdftotextHas
}

// HasPdftoppm reports whether pdftoppm is on PATH. Required for Tier 3
// OCR — it rasterizes PDF pages to PNG/PPM before tesseract ingests them.
// Ships with poppler-utils, same install as pdftotext.
func HasPdftoppm() bool {
	pdftoppmOnce.Do(func() { pdftoppmHas = lookPath("pdftoppm") })
	return pdftoppmHas
}

// HasPdfinfo reports whether pdfinfo is on PATH. Optional — we prefer
// `pdftotext -info` for metadata when available but fall back to
// `pdfinfo` for richer fields. Ships with poppler-utils.
func HasPdfinfo() bool {
	pdfinfoOnce.Do(func() { pdfinfoHas = lookPath("pdfinfo") })
	return pdfinfoHas
}

// HasTesseract reports whether tesseract is on PATH. Required for Tier 3
// OCR only; the cmd layer checks this at command start when --ocr is
// requested, so per-row calls are rare.
func HasTesseract() bool {
	tesseractOnce.Do(func() { tesseractHas = lookPath("tesseract") })
	return tesseractHas
}

// OCRAvailable reports whether the full OCR toolchain (pdftoppm +
// tesseract) is on PATH. The cmd layer uses this at command start to
// fail fast when --ocr is set but the environment can't support it.
func OCRAvailable() bool {
	return HasPdftoppm() && HasTesseract()
}

// OCRMissingBinaries returns the names of any OCR-required binaries
// that aren't on PATH. Empty slice means OCR is fully available. The
// cmd layer uses the list to build a specific install-hint log line
// ("install X and Y" reads better than "install X" when both missing).
func OCRMissingBinaries() []string {
	var missing []string
	if !HasPdftoppm() {
		missing = append(missing, "pdftoppm")
	}
	if !HasTesseract() {
		missing = append(missing, "tesseract")
	}
	return missing
}

// ResetDetectionForTest clears the lazy-cached binary flags so the next
// Has* call re-runs lookPath. Tests that mutate PATH (or stub lookPath)
// must call this to force rediscovery. Production code never calls it.
func ResetDetectionForTest() {
	pdftotextOnce = sync.Once{}
	pdftoppmOnce = sync.Once{}
	pdfinfoOnce = sync.Once{}
	tesseractOnce = sync.Once{}
	pdftotextHas = false
	pdftoppmHas = false
	pdfinfoHas = false
	tesseractHas = false
}

// lookPath is indirected via a package var so tests can stub PATH lookup
// without mutating the process environment.
var lookPath = func(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
