// Package failure classifies fetch/extract errors into discrete categories
// so stats.json and the dead-letter queue can aggregate meaningfully.
//
// Classification is deliberately based on error-string pattern matching
// rather than typed errors. The error sources (net/http, crypto/tls,
// chromedp, goquery, the router, the politeness gate) don't share a typed
// error hierarchy, and building one would be a larger refactor than this
// problem warrants. The upside of strings is that errors from inside the
// router's "all tiers exhausted" aggregation are still classifiable because
// the inner messages are preserved verbatim.
package failure

import (
	"regexp"
	"strings"
)

// httpStatusRe matches patterns like "http 404", "status 503", "http: 429"
// that appear embedded in wrapped error strings coming from the router.
var httpStatusRe = regexp.MustCompile(`(?:http|status)[: ]+(\d{3})`)

// Category is one of a finite set of failure buckets. Success is included
// so callers can classify every record uniformly.
type Category string

const (
	CatSuccess          Category = "success"
	CatDNS              Category = "dns_failure"
	CatConnectionRefused Category = "connection_refused"
	CatTLS              Category = "tls_error"
	CatTimeout          Category = "timeout"
	CatHTTP4xx          Category = "http_4xx"
	CatHTTP5xx          Category = "http_5xx"
	CatRobotsBlocked    Category = "robots_blocked"
	CatCloudflareBlock  Category = "cloudflare_block"
	CatParked           Category = "parked_domain"
	CatExtractionFailed Category = "extraction_failed"
	CatFollowFailed     Category = "follow_failed"
	CatTiersExhausted   Category = "all_tiers_exhausted"
	CatSPAShell         Category = "spa_shell"
	CatOther            Category = "other"
)

// IsReachable returns whether a category represents a successfully fetched
// page. "Reachable" is the denominator of the Lightpanda decision rule in
// docs/BENCHMARK.md — dead domains, TLS errors, 4xx/5xx, etc. don't count.
//
// CatExtractionFailed IS reachable: the fetch worked, only extraction broke.
// CatSPAShell is NOT reachable as a FINAL category: it only gets set when
// every tier thought the page was an unhydrated SPA shell, i.e. no tier
// ever returned real content.
func (c Category) IsReachable() bool {
	return c == CatSuccess || c == CatExtractionFailed
}

// Classify returns the best-fit category for a fetch outcome.
//
// Inputs:
//   - err:        the error returned from the router (may be nil on success)
//   - statusCode: the HTTP status observed on the last attempt (0 if none)
//   - errReason:  the Record.Error field (already-formatted reason string)
//
// At least one of err or errReason should be non-empty when the outcome is
// not successful; Classify falls back to CatOther if the input is unclear.
func Classify(err error, statusCode int, errReason string) Category {
	// Success: no error and a 2xx response.
	if err == nil && errReason == "" && statusCode >= 200 && statusCode < 300 {
		return CatSuccess
	}

	// Combine signals into one lowercased string for pattern matching. The
	// router error and the record's reason field often contain the same
	// information but from different angles.
	var parts []string
	if err != nil {
		parts = append(parts, err.Error())
	}
	if errReason != "" {
		parts = append(parts, errReason)
	}
	msg := strings.ToLower(strings.Join(parts, " | "))

	// Explicit reason strings from known trawl code paths first.
	switch {
	case strings.Contains(msg, "blocked by robots.txt"):
		return CatRobotsBlocked
	case strings.Contains(msg, "spa shell"):
		return CatSPAShell
	case strings.Contains(msg, "follow:"):
		return CatFollowFailed
	}

	// Parked-domain heuristic FIRST — WP Engine parking is a specific
	// signature from the real seed data: domains whose only DNS target
	// has a cert issued for *.wpengine.com rather than the domain itself.
	// This has to come before the generic TLS check or wpengine errors
	// get bucketed as tls_error instead of parked_domain.
	if strings.Contains(msg, "wpengine.com") {
		return CatParked
	}

	// Network-level failures. Check these before status codes because a
	// transport error means there was no HTTP response at all.
	switch {
	case strings.Contains(msg, "no such host"),
		strings.Contains(msg, "nxdomain"),
		strings.Contains(msg, "err_name_not_resolved"):
		return CatDNS
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "err_connection_refused"):
		return CatConnectionRefused
	case strings.Contains(msg, "tls:"),
		strings.Contains(msg, "x509:"),
		strings.Contains(msg, "certificate is valid for"),
		strings.Contains(msg, "err_cert_"):
		return CatTLS
	case strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "context canceled"),
		strings.Contains(msg, "timeout"):
		return CatTimeout
	}

	// Cloudflare challenge/blocks. 1020 = access denied by firewall rule,
	// 5xx variants in the 520-530 range are CF-specific. We can't inspect
	// the body here but the status code + text is enough for most cases.
	if strings.Contains(msg, "cf-chl") ||
		strings.Contains(msg, "cloudflare") ||
		statusCode == 1020 {
		return CatCloudflareBlock
	}

	// Status-code buckets, if available. If the caller didn't pass an
	// explicit status code, try to recover one from the error text —
	// router errors often look like "http: http 403" or "status 530".
	sc := statusCode
	if sc == 0 {
		if m := httpStatusRe.FindStringSubmatch(msg); m != nil {
			for i, ch := range m[1] {
				sc = sc*10 + int(ch-'0')
				_ = i
			}
		}
	}
	switch {
	case sc >= 400 && sc < 500:
		return CatHTTP4xx
	case sc >= 500 && sc < 600:
		return CatHTTP5xx
	}

	// The router's aggregator uses this phrase; prefer the inner signals
	// above when they matched, but fall back here when nothing else did.
	if strings.Contains(msg, "all tiers exhausted") {
		return CatTiersExhausted
	}

	// Fallback: extraction-level errors are uncategorizable further.
	if strings.Contains(msg, "extract:") {
		return CatExtractionFailed
	}

	return CatOther
}

// AllCategories returns every known category. Useful for pre-populating
// stats counter maps so categories with zero observations still appear.
func AllCategories() []Category {
	return []Category{
		CatSuccess,
		CatDNS,
		CatConnectionRefused,
		CatTLS,
		CatTimeout,
		CatHTTP4xx,
		CatHTTP5xx,
		CatRobotsBlocked,
		CatCloudflareBlock,
		CatParked,
		CatExtractionFailed,
		CatFollowFailed,
		CatTiersExhausted,
		CatSPAShell,
		CatOther,
	}
}
