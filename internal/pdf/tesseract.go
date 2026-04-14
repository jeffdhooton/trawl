package pdf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// runOCR invokes pdftoppm to rasterize the PDF into per-page images,
// then tesseract on each image, returning the extracted per-page text.
// Lives as a package var so tests can substitute a pure-Go
// implementation that doesn't require pdftoppm/tesseract on PATH.
//
// Production code and the live smoke tests hit defaultRunOCR; unit
// tests that exercise the Tier 3 escalation logic override this to
// return canned page text.
var runOCR = defaultRunOCR

// rasterizeDPI is the resolution (dots per inch) used when converting
// PDF pages to PNG for OCR. 300 DPI is the poppler/tesseract standard
// for text documents — high enough that letter shapes survive, low
// enough that per-page rasterization finishes in reasonable time.
const rasterizeDPI = 300

// pageImagePattern matches pdftoppm's output filenames
// (<prefix>-<page>.png). Page number is zero-padded based on total page
// count; we parse the number out to sort numerically across any padding
// width.
var pageImagePattern = regexp.MustCompile(`page-(\d+)\.png$`)

// defaultRunOCR is the real Tier 3 implementation. Pipeline:
//
//  1. Create a tempdir, mkdir'd fresh so the outer filesystem never
//     sees partial state.
//  2. Invoke `pdftoppm -r 300 -png [-l MaxPages] - <tmpdir>/page`
//     with the PDF piped on stdin. pdftoppm writes page-1.png,
//     page-2.png, ... up to the cap.
//  3. Glob the PNG files and sort by numeric page index.
//  4. For each image, invoke `tesseract <file> - -l <lang>` and
//     capture the stdout.
//  5. Clean up the tempdir regardless of outcome.
//
// Returns []Page ordered by page number. Empty slice is a valid
// return — caller distinguishes "no pages" from "no text" via the
// Page.Text fields.
func defaultRunOCR(ctx context.Context, body []byte, opts Opts) ([]Page, error) {
	tmpdir, err := os.MkdirTemp("", "trawl-pdf-ocr-*")
	if err != nil {
		return nil, fmt.Errorf("ocr: mkdir tempdir: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	prefix := filepath.Join(tmpdir, "page")
	args := []string{"-r", strconv.Itoa(rasterizeDPI), "-png"}
	if opts.MaxPages > 0 {
		args = append(args, "-l", strconv.Itoa(opts.MaxPages))
	}
	args = append(args, "-", prefix)

	if _, err := runCmd(ctx, 0, "pdftoppm", body, args...); err != nil {
		return nil, fmt.Errorf("pdftoppm: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(tmpdir, "page-*.png"))
	if err != nil {
		return nil, fmt.Errorf("ocr: glob pages: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("ocr: pdftoppm produced no page images")
	}
	sortFilesByPageNumber(files)

	lang := opts.OCRLang
	if lang == "" {
		lang = "eng"
	}

	pages := make([]Page, 0, len(files))
	for i, file := range files {
		imgBytes, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("ocr: read %s: %w", filepath.Base(file), err)
		}
		text, err := ocrImage(ctx, imgBytes, lang)
		if err != nil {
			return nil, fmt.Errorf("ocr: tesseract page %d: %w", i+1, err)
		}
		pages = append(pages, Page{
			Number: i + 1,
			Text:   string(text),
		})
	}
	return pages, nil
}

// ocrImage runs tesseract against a single PNG and returns the extracted
// text. Tesseract reads from stdin with `-` and writes plain text to
// stdout with the same sentinel. --psm 3 (automatic page segmentation,
// default) is used implicitly.
func ocrImage(ctx context.Context, png []byte, lang string) ([]byte, error) {
	return runCmd(ctx, 0, "tesseract", png,
		"-", "-",
		"-l", lang,
	)
}

// sortFilesByPageNumber sorts pdftoppm output paths by the numeric
// page index in the filename, rather than lexicographically. Handles
// both zero-padded (page-001.png) and non-padded (page-1.png) forms
// in case future pdftoppm versions change the convention.
func sortFilesByPageNumber(files []string) {
	sort.Slice(files, func(i, j int) bool {
		ni := pageNumberFrom(files[i])
		nj := pageNumberFrom(files[j])
		if ni == nj {
			return files[i] < files[j]
		}
		return ni < nj
	})
}

func pageNumberFrom(path string) int {
	m := pageImagePattern.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// stitchOCRPages joins per-page OCR text into a single byte stream with
// form-feed separators, matching pdftotext's page-break convention. The
// result flows through the same splitPages / renderMarkdown helpers as
// Tier 1+2 output.
func stitchOCRPages(pages []Page) []byte {
	if len(pages) == 0 {
		return nil
	}
	var buf []byte
	for i, p := range pages {
		if i > 0 {
			buf = append(buf, pageSeparator)
		}
		buf = append(buf, p.Text...)
	}
	return buf
}
