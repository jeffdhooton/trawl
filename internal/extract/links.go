package extract

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// LinkOptions controls FirstLink behavior.
type LinkOptions struct {
	// SameDomain restricts matches to the base URL's host. Default true.
	SameDomain bool
}

// FirstLink scans body for the first <a> whose resolved href matches the
// given CSS selector, applies same-domain filtering, and returns the
// absolute URL. The base URL is used to resolve relative hrefs and to
// enforce same-domain policy.
//
// Returns ("", nil) if no link matches — callers can treat this as a
// "follow_failed" signal without it being a hard error.
//
// The selector follows goquery/cascadia syntax. Common patterns for
// pricing discovery:
//
//	a[href*="pricing"]
//	a[href*="/pricing"], a[href*="/plans"], a[href*="/price"]
//	nav a:contains("Pricing")
func FirstLink(body []byte, base string, selector string, opts LinkOptions) (string, error) {
	if selector == "" {
		return "", fmt.Errorf("empty selector")
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}

	wantHost := strings.ToLower(baseURL.Hostname())

	var resolved string
	doc.Find(selector).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, ok := s.Attr("href")
		if !ok {
			return true // keep looking
		}
		href = strings.TrimSpace(href)
		if href == "" || strings.HasPrefix(href, "#") ||
			strings.HasPrefix(href, "javascript:") ||
			strings.HasPrefix(href, "mailto:") {
			return true
		}

		u, err := baseURL.Parse(href)
		if err != nil {
			return true
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return true
		}

		if opts.SameDomain {
			host := strings.ToLower(u.Hostname())
			// Accept exact match OR a subdomain of the wanted host.
			// Many pricing pages live at e.g. pricing.company.com even
			// when the homepage is company.com; both are "same-domain"
			// in the intuitive sense.
			if host != wantHost &&
				!strings.HasSuffix(host, "."+wantHost) &&
				!strings.HasSuffix(wantHost, "."+host) {
				return true
			}
		}

		u.Fragment = ""
		resolved = u.String()
		return false // stop at first match
	})

	return resolved, nil
}
