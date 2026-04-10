package extract

import (
	md "github.com/JohannesKaufmann/html-to-markdown"
)

// ToMarkdown converts an HTML body to CommonMark markdown. Pass baseURL
// so relative links get resolved to absolute ones in the output.
//
// On conversion failure the function returns the original body as a
// string and a non-nil error — callers can decide whether to surface
// the raw HTML or skip the record. Content-extraction pipelines should
// treat conversion failure as "render the raw body" and move on;
// markdown generation is best-effort.
func ToMarkdown(body []byte, baseURL string) (string, error) {
	conv := md.NewConverter(md.DomainFromURL(baseURL), true, nil)
	out, err := conv.ConvertBytes(body)
	if err != nil {
		return string(body), err
	}
	return string(out), nil
}
