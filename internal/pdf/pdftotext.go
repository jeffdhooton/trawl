package pdf

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// runCmd indirects the low-level exec helper so tests can stub pdftotext
// and pdfinfo invocations without shelling out. Production code never
// overrides this.
var runCmd = run

// pageSeparator is the byte pdftotext emits between pages. `man pdftotext`:
// "Inserts a form feed at the end of each page (default, unless -nopgbrk
// is given)." We rely on this for per-page splitting and for the markdown
// rendering path.
const pageSeparator byte = '\f'

// stubThreshold is the minimum extracted byte count (after whitespace
// and page-separator stripping) before output is considered non-empty.
// 16 bytes is roughly "one short line of text" — below this we assume
// the text layer is missing or degenerate and escalate. Phase 3 uses
// this to decide when to retry with -layout; Phase 6 uses it to decide
// when to fall back to OCR.
const stubThreshold = 16

// pdftotextMode selects the flag set passed to pdftotext.
type pdftotextMode int

const (
	modePlain pdftotextMode = iota
	modeLayout
)

// runPdftotext invokes pdftotext on the given PDF bytes in the specified
// mode. Returns the raw text output (form-feed separated pages) plus any
// error. Encryption errors are mapped to ErrEncrypted; damaged/syntax
// errors propagate as wrapped exec errors.
//
// Caller owns the context deadline. pdftotext reads from stdin ("-") and
// writes to stdout ("-"). -q suppresses non-fatal warnings so stderr
// stays clean for real errors.
func runPdftotext(ctx context.Context, body []byte, mode pdftotextMode) ([]byte, error) {
	args := []string{"-enc", "UTF-8", "-q"}
	if mode == modeLayout {
		args = append(args, "-layout")
	}
	args = append(args, "-", "-")

	out, err := runCmd(ctx, 0, "pdftotext", body, args...)
	if err != nil {
		if isEncryptionError(err) {
			return nil, ErrEncrypted
		}
		return out, fmt.Errorf("pdftotext: %w", err)
	}
	return out, nil
}

// isEncryptionError matches stderr diagnostics from poppler's pdftotext
// when the input is password-protected. Poppler phrases this differently
// across versions; we match any of the known forms.
func isEncryptionError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "incorrect password") ||
		strings.Contains(s, "pdf file is encrypted") ||
		strings.Contains(s, "encrypted pdf")
}

// splitPages splits pdftotext output on form-feed and returns one Page
// per chunk with 1-based Number. Trailing form-feed at EOF is trimmed
// first so the last page isn't duplicated as an empty entry. Within a
// page, text is preserved verbatim including trailing newlines.
func splitPages(out []byte) []Page {
	out = bytes.TrimRight(out, "\f")
	chunks := bytes.Split(out, []byte{pageSeparator})
	pages := make([]Page, 0, len(chunks))
	for i, chunk := range chunks {
		pages = append(pages, Page{
			Number: i + 1,
			Text:   string(chunk),
		})
	}
	return pages
}

// isStubExtraction reports whether the extracted text is below the stub
// threshold after stripping whitespace and page separators. Pure
// whitespace and form-feed-only output both count as empty — common for
// scanned PDFs where pdftotext finds no text layer and emits just page
// breaks.
func isStubExtraction(out []byte) bool {
	trimmed := bytes.TrimSpace(bytes.ReplaceAll(out, []byte{pageSeparator}, nil))
	return len(trimmed) < stubThreshold
}

// renderMarkdown converts pdftotext output to a markdown-compatible
// byte stream. Tier 1 does no structural inference — it replaces
// form-feed page breaks with blank-line paragraph breaks and strips
// the trailing page-break. Raw text IS valid CommonMark, so consumers
// that want real markdown structure can run the output through their
// own post-processor.
func renderMarkdown(out []byte) []byte {
	out = bytes.TrimRight(out, "\f")
	return bytes.ReplaceAll(out, []byte{pageSeparator}, []byte("\n\n"))
}
