package pdf

import "strings"

// TestPDFOpts configures the fixture generator. All fields are
// optional; zero values produce a valid minimal PDF with one page of
// Lorem Ipsum — callers that only need "a PDF" can pass zero value.
type TestPDFOpts struct {
	// Pages is the list of per-page text. Empty or nil defaults to a
	// single page with a placeholder message. Use len(Pages) to drive
	// the page count; each entry gets its own page in the output.
	Pages []string
	// Title populates the /Info dict's /Title field. Empty omits.
	Title string
	// Author populates the /Info dict's /Author field. Empty omits.
	Author string
}

// MinimalTestPDF returns a tiny but conformant PDF containing the given
// single-page text, rendered with Helvetica 12pt. Equivalent to
// MinimalTestPDFWithOpts(TestPDFOpts{Pages: []string{text}}); kept as
// a convenience for the common "one page of text" case used by Phase
// 2-4 live tests.
//
// Do not use in production code — the generator has zero error
// handling on caller input and panics on allocation failure.
func MinimalTestPDF(text string) []byte {
	return MinimalTestPDFWithOpts(TestPDFOpts{Pages: []string{text}})
}

// MinimalTestPDFWithOpts is the full-featured generator. Supports
// multi-page output plus optional /Info metadata. All outputs stay
// well under 50KB for the default content.
//
// The generated PDF uses Helvetica as the only font (Type1 core font,
// no font embedding required) and computes xref offsets at build time.
// pdftotext, pdfinfo, and pdftoppm all accept the output without
// complaint.
func MinimalTestPDFWithOpts(opts TestPDFOpts) []byte {
	if len(opts.Pages) == 0 {
		opts.Pages = []string{"Placeholder body text for a minimal test PDF."}
	}

	var buf strings.Builder
	write := func(s string) { buf.WriteString(s) }

	// Object numbering:
	//   1: /Catalog
	//   2: /Pages (with Kids referencing N page objects)
	//   3: /Font (shared across all pages)
	//   4: /Info (optional; only emitted when Title or Author set)
	//   Then for each page i (1..N):
	//     page object      = 4 + 2*i - 1 (if Info present) or 3 + 2*i - 1
	//     contents stream  = page object + 1
	hasInfo := opts.Title != "" || opts.Author != ""
	objectBase := 3 // catalog(1), pages(2), font(3)
	if hasInfo {
		objectBase = 4
	}
	totalObjects := objectBase + 2*len(opts.Pages)
	offsets := make([]int, totalObjects+1)

	write("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")

	offsets[1] = buf.Len()
	write("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// Build the Kids list: [<pageObj1> 0 R <pageObj2> 0 R ...]
	var kidsParts []string
	for i := 0; i < len(opts.Pages); i++ {
		pageObjNum := objectBase + 1 + 2*i
		kidsParts = append(kidsParts, itoa(pageObjNum)+" 0 R")
	}
	kidsStr := "[" + strings.Join(kidsParts, " ") + "]"

	offsets[2] = buf.Len()
	write("2 0 obj\n<< /Type /Pages /Kids " + kidsStr + " /Count " + itoa(len(opts.Pages)) + " >>\nendobj\n")

	offsets[3] = buf.Len()
	write("3 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	if hasInfo {
		offsets[4] = buf.Len()
		write("4 0 obj\n<<")
		if opts.Title != "" {
			write(" /Title (" + escapePDFString(opts.Title) + ")")
		}
		if opts.Author != "" {
			write(" /Author (" + escapePDFString(opts.Author) + ")")
		}
		write(" >>\nendobj\n")
	}

	// Emit page + contents pairs.
	for i, text := range opts.Pages {
		pageObjNum := objectBase + 1 + 2*i
		contentObjNum := pageObjNum + 1

		content := "BT /F1 12 Tf 72 720 Td (" + escapePDFString(text) + ") Tj ET"

		offsets[pageObjNum] = buf.Len()
		write(itoa(pageObjNum) + " 0 obj\n")
		write("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents " +
			itoa(contentObjNum) + " 0 R /Resources << /Font << /F1 3 0 R >> >> >>\n")
		write("endobj\n")

		offsets[contentObjNum] = buf.Len()
		write(itoa(contentObjNum) + " 0 obj\n<< /Length " + itoa(len(content)) + " >>\nstream\n" + content + "\nendstream\nendobj\n")
	}

	xrefOffset := buf.Len()
	write("xref\n0 " + itoa(totalObjects+1) + "\n")
	write("0000000000 65535 f \n")
	for i := 1; i <= totalObjects; i++ {
		write(pad10(offsets[i]) + " 00000 n \n")
	}
	write("trailer\n<< /Size " + itoa(totalObjects+1) + " /Root 1 0 R")
	if hasInfo {
		write(" /Info 4 0 R")
	}
	write(" >>\nstartxref\n" + itoa(xrefOffset) + "\n%%EOF\n")

	return []byte(buf.String())
}

// escapePDFString escapes characters that are special inside
// paren-delimited PDF string literals. Per PDF 1.7 §7.3.4.2: backslash,
// open paren, and close paren must be backslash-escaped.
func escapePDFString(s string) string {
	return strings.NewReplacer("\\", `\\`, "(", `\(`, ")", `\)`).Replace(s)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return strings.TrimLeft(pad10(n), "0")
}

func pad10(n int) string {
	digits := []byte("0000000000")
	for i := len(digits) - 1; i >= 0 && n > 0; i-- {
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits)
}
