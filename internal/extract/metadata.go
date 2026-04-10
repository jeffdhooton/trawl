package extract

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// PageMetadata is the automatic "who/what/when" info we scrape from every
// HTML page. None of these fields are domain-specific — they're the
// surface every structured-content pipeline expects (title for the card,
// og:image for the preview, canonical for dedup, etc).
//
// All fields are omitempty so pages missing a given signal don't pollute
// JSONL records with empty strings.
type PageMetadata struct {
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Canonical   string            `json:"canonical,omitempty"`
	Language    string            `json:"language,omitempty"`
	PublishedAt *time.Time        `json:"published_at,omitempty"`
	OpenGraph   map[string]string `json:"open_graph,omitempty"`
	Twitter     map[string]string `json:"twitter,omitempty"`
	JSONLD      []any             `json:"json_ld,omitempty"`
}

// Metadata parses body as HTML and returns whatever page-level metadata
// it can find. It never errors on malformed input — goquery is permissive
// and missing fields produce zero values. Pass baseURL so relative canonical
// links can be resolved to absolute URLs.
//
// The returned pointer is nil only when the body contains no recoverable
// HTML at all. An HTML document with zero metadata still yields a non-nil
// but empty PageMetadata.
func Metadata(body []byte, baseURL string) *PageMetadata {
	if len(body) == 0 {
		return nil
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil
	}

	meta := &PageMetadata{}

	// Title: prefer <meta property="og:title"> over <title>, because the
	// former is usually the "social card" title (cleaner, often shorter).
	// Fall back to <title> when og:title is absent.
	meta.Title = strings.TrimSpace(metaProp(doc, "og:title"))
	if meta.Title == "" {
		meta.Title = strings.TrimSpace(doc.Find("title").First().Text())
	}

	// Description: og:description → twitter:description → <meta name="description">
	meta.Description = firstNonEmpty(
		metaProp(doc, "og:description"),
		metaName(doc, "twitter:description"),
		metaName(doc, "description"),
	)

	// Canonical: <link rel="canonical" href="...">, resolved against baseURL.
	if href, ok := doc.Find(`link[rel="canonical"]`).First().Attr("href"); ok {
		meta.Canonical = resolveURL(baseURL, strings.TrimSpace(href))
	}

	// Language: prefer <html lang="..."> since it's authoritative per HTML5.
	// Fall back to the og:locale tag which some sites set without lang.
	if lang, ok := doc.Find("html").First().Attr("lang"); ok {
		meta.Language = strings.TrimSpace(lang)
	}
	if meta.Language == "" {
		meta.Language = metaProp(doc, "og:locale")
	}

	// Open Graph and Twitter card tags — stored WITHOUT the prefix so the
	// map is just {"title": "...", "image": "..."} instead of {"og:title":
	// "..."}. Makes downstream JSON consumers friendlier.
	meta.OpenGraph = collectPrefixed(doc, "property", "og:")
	meta.Twitter = collectPrefixed(doc, "name", "twitter:")
	if len(meta.OpenGraph) == 0 {
		meta.OpenGraph = nil
	}
	if len(meta.Twitter) == 0 {
		meta.Twitter = nil
	}

	// PublishedAt: check the usual suspects in priority order. Parse the
	// first non-empty value that yields a valid timestamp; leave nil
	// otherwise. Skip <time datetime> for v1 — it's noisy and site-specific.
	meta.PublishedAt = parsePublishedAt(
		metaProp(doc, "article:published_time"),
		metaProp(doc, "og:published_time"),
		metaName(doc, "pubdate"),
		metaName(doc, "date"),
		metaName(doc, "dc.date"),
	)

	// JSON-LD: collect every <script type="application/ld+json"> and
	// unmarshal into `any`. Each script may contain a single object OR an
	// array; we preserve the shape because downstream consumers often use
	// schema.org @type discrimination to decide how to interpret.
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		raw := strings.TrimSpace(s.Text())
		if raw == "" {
			return
		}
		var val any
		if err := json.Unmarshal([]byte(raw), &val); err != nil {
			return // silently skip malformed blocks — real sites have them
		}
		meta.JSONLD = append(meta.JSONLD, val)
	})

	return meta
}

// metaProp returns the content of <meta property="X" content="...">.
func metaProp(doc *goquery.Document, property string) string {
	val, _ := doc.Find(`meta[property="` + property + `"]`).First().Attr("content")
	return strings.TrimSpace(val)
}

// metaName returns the content of <meta name="X" content="...">. Uses
// cascadia's `i` flag for case-insensitive attribute matching, which
// catches e.g. `name="Description"` in the wild — HTML spec says name
// is case-insensitive but cascadia matches attribute values exactly by
// default.
func metaName(doc *goquery.Document, name string) string {
	val, _ := doc.Find(`meta[name="` + name + `" i]`).First().Attr("content")
	return strings.TrimSpace(val)
}

// collectPrefixed iterates over <meta {attr}="{prefix}X" content="Y">
// and returns a map of X→Y. Used for og:* and twitter:* families so we
// strip the prefix for friendly JSON output.
func collectPrefixed(doc *goquery.Document, attr, prefix string) map[string]string {
	out := map[string]string{}
	doc.Find("meta[" + attr + "]").Each(func(_ int, s *goquery.Selection) {
		key, _ := s.Attr(attr)
		if !strings.HasPrefix(key, prefix) {
			return
		}
		content, ok := s.Attr("content")
		if !ok {
			return
		}
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			return
		}
		out[strings.TrimPrefix(key, prefix)] = trimmed
	})
	return out
}

// parsePublishedAt walks a priority-ordered list of candidate strings and
// returns the first one that parses as a timestamp. Returns nil if none
// parse. The format chain is loose because published_at values in the
// wild take many shapes — ISO 8601 is ideal but RFC1123 and human-ish
// forms also appear.
func parsePublishedAt(candidates ...string) *time.Time {
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC1123,
		time.RFC1123Z,
		time.RFC822,
		time.RFC822Z,
	}
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, raw); err == nil {
				t = t.UTC()
				return &t
			}
		}
	}
	return nil
}

// resolveURL turns a possibly-relative href into an absolute URL using
// baseURL. Returns the original href if either is unparseable — never
// errors, because metadata extraction must not fail on weird inputs.
func resolveURL(baseURL, href string) string {
	if href == "" {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}

// firstNonEmpty returns the first non-empty string from its arguments,
// trimming each as it checks. Used for field precedence chains.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
