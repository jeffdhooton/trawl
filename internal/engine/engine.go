// Package engine defines the pluggable fetcher abstraction and ships the
// HTTP tier. Each engine corresponds to one tier in trawl's escalation ladder
// (net/http → Lightpanda → Chromium). Engines are interchangeable behind the
// Engine interface so the router can swap them per-URL.
package engine

import (
	"context"
	"net/http"
	"time"

	"github.com/chromedp/chromedp"
)

// Engine fetches a single URL and returns what was observed. It does not
// interpret the body (extraction lives elsewhere) and it does not decide
// validity (the router + validity package handle that).
type Engine interface {
	Fetch(ctx context.Context, req Request) (*Result, error)
	Name() string
	Close() error
}

// Request is what the caller asks the engine to fetch.
type Request struct {
	URL string
	// ExtraHeaders are merged on top of the engine's default headers.
	ExtraHeaders http.Header
	// WantScreenshot asks the engine to populate Result.Screenshot with a
	// full-page PNG if it can. Engines that can't produce screenshots (HTTP)
	// silently leave Screenshot nil. Only chromium implements it today.
	WantScreenshot bool
	// Actions are pre-scrape interactive steps (click, scroll, wait, etc.)
	// that run after the page loads but before DOM capture. Only the
	// chromium engine executes them; the HTTP engine silently ignores them.
	Actions []chromedp.Action
}

// Result is what the engine saw. It includes the raw body; higher layers
// decide how to parse/extract.
type Result struct {
	URL         string
	FinalURL    string // after redirects
	StatusCode  int
	Header      http.Header
	ContentType string
	Body        []byte
	Duration    time.Duration
	Redirects   []string
	// Screenshot is a full-page PNG captured by the engine when
	// Request.WantScreenshot was set. Nil on engines that can't take one
	// (HTTP) or when the caller didn't ask.
	Screenshot []byte
	// Evasion records the engine's view of which anti-detection features
	// were active for THIS fetch (not job-wide). The HTTP engine fills it
	// when BrowserLikeHeaders or a non-declared UserAgentStrategy ran;
	// the chromium engine fills it when Stealth was on. Nil when nothing
	// engine-level was active. Politeness-level jitter is stamped
	// separately by the cmd/trawl layer because the engine doesn't see
	// the gate's pacing decisions.
	Evasion *EvasionInfo
}

// EvasionInfo is the engine-level slice of EvasionStats. Kept distinct
// from output.EvasionStats so the engine package doesn't import output;
// the cmd/trawl layer copies the fields into the record's metadata.
type EvasionInfo struct {
	BrowserLike bool
	Stealth     bool
	UserAgent   string
	// TLSMatch is the active --tls-match preset (e.g. "chrome") when
	// Tier 3 evasion swapped the HTTP engine's transport. Empty when
	// the stdlib transport handled this fetch.
	TLSMatch string
}
