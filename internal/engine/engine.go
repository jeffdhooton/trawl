// Package engine defines the pluggable fetcher abstraction and ships the
// HTTP tier. Each engine corresponds to one tier in trawl's escalation ladder
// (net/http → Lightpanda → Chromium). Engines are interchangeable behind the
// Engine interface so the router can swap them per-URL.
package engine

import (
	"context"
	"net/http"
	"time"
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
}
