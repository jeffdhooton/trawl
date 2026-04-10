package extract

import (
	"bytes"
	"net/url"

	readability "codeberg.org/readeck/go-readability/v2"
)

// Readable runs Mozilla-style readability boilerplate removal on body
// and returns the cleaned HTML. The base URL lets the parser resolve
// relative links inside the cleaned article.
//
// Readability is inherently best-effort: a page that isn't an "article"
// (e.g. a landing page, a product listing, a pricing grid) may come
// back empty or garbled. This function never hard-errors for that
// reason — instead it returns the ORIGINAL body verbatim whenever
// readability fails or produces an empty result. Callers can detect
// the fallback by comparing output length to input length, but most
// pipelines just use whatever Readable returns and move on.
//
// Metadata extraction must NOT use Readable's output — readability
// strips most of the <head>, including the og:* tags we want. Run
// metadata extraction on the original body first.
func Readable(body []byte, baseURL string) []byte {
	if len(body) == 0 {
		return body
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return body
	}
	article, err := readability.FromReader(bytes.NewReader(body), base)
	if err != nil {
		return body
	}
	if article.Node == nil {
		return body
	}
	var buf bytes.Buffer
	if err := article.RenderHTML(&buf); err != nil {
		return body
	}
	cleaned := buf.Bytes()
	if len(cleaned) == 0 {
		return body
	}
	return cleaned
}
