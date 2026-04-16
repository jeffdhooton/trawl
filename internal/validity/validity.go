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
	// SoftBlock is non-nil when the response was a 200 OK that looked like
	// an anti-bot challenge wall (Cloudflare "Just a moment", Akamai
	// challenge, DataDome captcha, etc.). When populated, Valid is false
	// and Escalate is true — the router tries the next tier. The pointer
	// survives for downstream consumers even on successful fetches via a
	// later tier: router.Attempt preserves per-tier SoftBlock so a post-
	// hoc observer can see "tier 1 got walled but tier 2 saved us."
	SoftBlock *SoftBlockDetection
}

// SoftBlockDetection records which anti-bot vendor and marker triggered
// the soft-block heuristic. Serialized via output.SoftBlockInfo — kept
// distinct here so the validity package has no dependency on output.
type SoftBlockDetection struct {
	// Vendor is the anti-bot vendor family the marker came from.
	// One of: cloudflare, akamai, datadome, incapsula, perimeterx, generic.
	Vendor string
	// Marker is the specific substring that matched. Useful for tuning
	// the marker list when false positives surface in production.
	Marker string
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
	// DetectSoftBlock: when true, scan small text/html bodies for anti-bot
	// challenge markers (Cloudflare "Just a moment", Akamai challenge,
	// DataDome captcha, etc.) and treat matches as escalation-worthy
	// failures even though the HTTP status is 200.
	DetectSoftBlock bool
}

// SoftBlockMaxBytes caps the body size at which soft-block detection
// runs. Real challenge pages are small (CF's "Just a moment" HTML is
// ~4KB; Akamai's challenge is similar). Above this cap we skip the
// scan to avoid false positives on long articles that mention
// "captcha" or "challenge" in prose.
const SoftBlockMaxBytes = 50 * 1024

// Default returns a reasonable default Checker config.
func Default() Config {
	return Config{
		MinBodyBytes:    512,
		DetectSPAShell:  true,
		DetectSoftBlock: true,
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
		strings.Contains(ct, "text/plain"),
		strings.Contains(ct, "application/pdf"),
		strings.Contains(ct, "application/x-pdf"):
		// Non-HTML but still a valid, directly-usable response.
		// PDFs are handled by the PDF engine at the record-build layer
		// (see cmd/trawl.transformPDFIfNeeded). The router returns the
		// raw bytes here; extraction happens downstream.
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

	// 3b. Soft-block detection. A 200 OK whose body smells like an
	// anti-bot challenge wall is treated as an escalation-worthy
	// failure — the next tier (typically chromium with stealth) may
	// get through the challenge. Gated by body size so a long article
	// that happens to mention "captcha" doesn't trip the detector.
	if c.cfg.DetectSoftBlock && len(p.Body) <= SoftBlockMaxBytes {
		if det := detectSoftBlock(p.Body); det != nil {
			return Result{
				Valid:     false,
				Escalate:  true,
				Reason:    "soft block: " + det.Vendor + "/" + det.Marker,
				SoftBlock: det,
			}
		}
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

// softBlockMarker pairs an anti-bot vendor with one case-insensitive
// substring whose presence in a small text/html body strongly implies
// a challenge wall rather than real content.
type softBlockMarker struct {
	vendor  string
	pattern string // lowercased; detectSoftBlock lowercases the body once
}

// softBlockMarkers is the extensible catalog of challenge-page
// signatures. Keep this list conservative — each marker is a substring
// match, so a marker that also appears in legitimate body content
// will false-positive on small pages. Vendor strings are the stable
// API surface (exposed via output.SoftBlockInfo.Vendor); don't rename
// without considering downstream consumers.
var softBlockMarkers = []softBlockMarker{
	// Cloudflare challenge / Turnstile / IUAM.
	{"cloudflare", "cf-chl"},
	{"cloudflare", "cf-browser-verification"},
	{"cloudflare", "cf-turnstile"},
	{"cloudflare", "just a moment"},
	{"cloudflare", "checking your browser"},
	{"cloudflare", "challenges.cloudflare.com"},

	// Akamai Bot Manager.
	{"akamai", "akam_blocked"},
	{"akamai", "pardon our interruption"},
	{"akamai", "reference #18."}, // Akamai's "Reference #18.xxxxx" block page prefix

	// DataDome.
	{"datadome", "datadome"},
	{"datadome", "dd-captcha"},

	// Imperva / Incapsula.
	{"incapsula", "_incapsula_resource"},
	{"incapsula", "incap_ses"},

	// PerimeterX / HUMAN.
	{"perimeterx", "px-captcha"},
	{"perimeterx", "_pxaction"},
	{"perimeterx", "_pxhd"},

	// Generic CAPTCHA widgets loaded as the top-level content.
	{"generic", "/recaptcha/api.js"},
	{"generic", "hcaptcha.com/1/api.js"},

	// Generic block / access-denied walls.
	{"generic", "attention required"},
	{"generic", "you have been blocked"},
	{"generic", "access denied"},
	{"generic", "request unsuccessful"},
}

// detectSoftBlock scans the body for any registered marker. Returns
// the first hit (vendors are ordered from most-specific to most-
// generic in softBlockMarkers, so a real CF challenge won't be
// misclassified as "generic" just because the page also includes an
// "Access denied" string).
func detectSoftBlock(body []byte) *SoftBlockDetection {
	if len(body) == 0 {
		return nil
	}
	lower := strings.ToLower(string(body))
	for _, m := range softBlockMarkers {
		if strings.Contains(lower, m.pattern) {
			return &SoftBlockDetection{Vendor: m.vendor, Marker: m.pattern}
		}
	}
	return nil
}
