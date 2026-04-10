// Package validity decides whether a fetched page is "good enough" or
// whether the router should escalate to a heavier engine.
//
// The default checker applies layered heuristics (cheapest first):
//  1. HTTP status
//  2. Content-Type
//  3. Body size vs threshold
//  4. SPA shell detection (root div with no children)
//  5. Selector contract (user-supplied required selectors must match)
//
// Callers can provide a custom Checker for specialized rules.
package validity

import (
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Result describes why a fetch was judged valid or invalid.
type Result struct {
	Valid  bool
	Reason string // human-readable; empty if Valid
	// Escalate is true when the failure is the kind a heavier engine might
	// fix (SPA shell, selector miss, 5xx). It is false when escalation would
	// not help (bad URL, 4xx client errors other than 429).
	Escalate bool
}

// Page is the minimal input a Checker needs.
type Page struct {
	URL         string
	StatusCode  int
	ContentType string
	Body        []byte
}

// Checker decides whether a page is valid.
type Checker interface {
	Check(Page) Result
}

// Config drives the default Checker.
type Config struct {
	// MinBodyBytes: bodies shorter than this are considered stubs. 0 disables.
	MinBodyBytes int
	// RequiredSelectors: if any of these CSS selectors fails to match the
	// parsed HTML, the page is invalid and escalation is recommended.
	RequiredSelectors []string
	// DetectSPAShell: when true, look for framework hydration markers
	// (<div id="root"></div> etc.) with no children.
	DetectSPAShell bool
}

// Default returns a reasonable default Checker config.
func Default() Config {
	return Config{
		MinBodyBytes:   512,
		DetectSPAShell: true,
	}
}

// NewChecker returns a Checker driven by the given config.
func NewChecker(cfg Config) Checker {
	return defaultChecker{cfg: cfg}
}

type defaultChecker struct{ cfg Config }

func (c defaultChecker) Check(p Page) Result {
	// 1. HTTP status.
	switch {
	case p.StatusCode >= 200 && p.StatusCode < 300:
		// continue
	case p.StatusCode == 429 || (p.StatusCode >= 500 && p.StatusCode < 600):
		return Result{Valid: false, Escalate: true, Reason: httpReason(p.StatusCode)}
	case p.StatusCode >= 400:
		// 4xx (other than 429) will not be fixed by escalation.
		return Result{Valid: false, Escalate: false, Reason: httpReason(p.StatusCode)}
	case p.StatusCode == 0:
		return Result{Valid: false, Escalate: true, Reason: "no response"}
	default:
		return Result{Valid: false, Escalate: true, Reason: httpReason(p.StatusCode)}
	}

	// 2. Content-Type must be HTML-ish. JSON/XML is a direct hit, not escalated.
	ct := strings.ToLower(p.ContentType)
	switch {
	case strings.Contains(ct, "text/html"), strings.Contains(ct, "application/xhtml"):
		// continue
	case strings.Contains(ct, "application/json"),
		strings.Contains(ct, "application/xml"),
		strings.Contains(ct, "text/xml"),
		strings.Contains(ct, "text/plain"):
		// Non-HTML but still a valid, directly-usable response.
		return Result{Valid: true}
	case ct == "":
		// Missing content-type; treat as HTML and fall through.
	default:
		return Result{Valid: false, Escalate: false, Reason: "unsupported content-type: " + ct}
	}

	// 3. Body size.
	if c.cfg.MinBodyBytes > 0 && len(p.Body) < c.cfg.MinBodyBytes {
		return Result{Valid: false, Escalate: true, Reason: "body below threshold"}
	}

	// 4+5. Parse once and run DOM-aware checks.
	needsDOM := c.cfg.DetectSPAShell || len(c.cfg.RequiredSelectors) > 0
	if needsDOM {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(p.Body)))
		if err != nil {
			return Result{Valid: false, Escalate: true, Reason: "html parse failed"}
		}

		if c.cfg.DetectSPAShell && isSPAShell(doc) {
			return Result{Valid: false, Escalate: true, Reason: "spa shell detected"}
		}

		for _, sel := range c.cfg.RequiredSelectors {
			if doc.Find(sel).Length() == 0 {
				return Result{Valid: false, Escalate: true, Reason: "required selector missed: " + sel}
			}
		}
	}

	return Result{Valid: true}
}

// Common hydration markers used by React, Next.js, Vue, Angular, Svelte.
var shellIDs = []string{"root", "__next", "app", "__nuxt", "svelte"}
var shellTags = []string{"app-root"}

func isSPAShell(doc *goquery.Document) bool {
	for _, id := range shellIDs {
		sel := doc.Find("#" + id)
		if sel.Length() > 0 && strings.TrimSpace(sel.Text()) == "" && sel.Children().Length() == 0 {
			return true
		}
	}
	for _, tag := range shellTags {
		sel := doc.Find(tag)
		if sel.Length() > 0 && strings.TrimSpace(sel.Text()) == "" && sel.Children().Length() == 0 {
			return true
		}
	}
	return false
}

func httpReason(code int) string {
	if code == 0 {
		return "no response"
	}
	return "http " + strconv.Itoa(code)
}
