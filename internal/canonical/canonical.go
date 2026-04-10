// Package canonical normalizes URLs so equivalent URLs produce the same key.
//
// Canonicalization is deliberate and conservative: we lowercase the host,
// strip the fragment, remove known tracking parameters, sort remaining query
// params, and normalize the path's trailing slash. The output is always a
// valid URL string that can be fed back into http.Get.
package canonical

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// DefaultTrackingParams is the list of query parameters stripped during
// canonicalization. Callers can override via Options.TrackingParams.
var DefaultTrackingParams = []string{
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
	"utm_id", "utm_name", "utm_reader", "utm_social", "utm_brand",
	"fbclid", "gclid", "dclid", "gbraid", "wbraid",
	"mc_cid", "mc_eid",
	"_ga", "_gl",
	"msclkid", "yclid", "twclid", "igshid",
	"ref", "ref_src", "ref_url",
}

// Options controls canonicalization behavior.
type Options struct {
	// TrackingParams lists query params to strip. If nil, DefaultTrackingParams is used.
	TrackingParams []string
	// KeepFragment retains the URL fragment (#...) instead of stripping it.
	KeepFragment bool
}

// Canonicalize returns a canonical form of rawURL.
//
// Rules:
//   - Scheme and host are lowercased.
//   - Default ports are stripped (:80 for http, :443 for https).
//   - Fragment is removed unless Options.KeepFragment is true.
//   - Known tracking query params are removed.
//   - Remaining query params are sorted alphabetically.
//   - Empty path is normalized to "/".
//
// Relative URLs and URLs without a scheme are rejected.
func Canonicalize(rawURL string, opts Options) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("empty url")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if u.Scheme == "" {
		return "", fmt.Errorf("missing scheme: %q", rawURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host: %q", rawURL)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	if host, port, ok := splitHostPort(u.Host); ok {
		if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
			u.Host = host
		}
	}

	if u.Path == "" {
		u.Path = "/"
	}

	if !opts.KeepFragment {
		u.Fragment = ""
		u.RawFragment = ""
	}

	trackingParams := opts.TrackingParams
	if trackingParams == nil {
		trackingParams = DefaultTrackingParams
	}
	u.RawQuery = normalizeQuery(u.RawQuery, trackingParams)

	return u.String(), nil
}

// MustCanonicalize is a convenience wrapper for tests and constants.
func MustCanonicalize(rawURL string) string {
	c, err := Canonicalize(rawURL, Options{})
	if err != nil {
		panic(err)
	}
	return c
}

// Host extracts the lowercased hostname (without port) from a URL.
func Host(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	h := strings.ToLower(u.Hostname())
	if h == "" {
		return "", fmt.Errorf("missing host: %q", rawURL)
	}
	return h, nil
}

func splitHostPort(host string) (h, p string, ok bool) {
	// net.SplitHostPort errors on IPv6 brackets; do it manually for the common case.
	i := strings.LastIndex(host, ":")
	if i < 0 {
		return host, "", false
	}
	if strings.Contains(host[:i], ":") && !strings.HasPrefix(host, "[") {
		// IPv6 without brackets; can't split safely.
		return host, "", false
	}
	return host[:i], host[i+1:], true
}

func normalizeQuery(raw string, tracking []string) string {
	if raw == "" {
		return ""
	}
	drop := make(map[string]struct{}, len(tracking))
	for _, k := range tracking {
		drop[strings.ToLower(k)] = struct{}{}
	}

	type pair struct{ k, v string }
	var kept []pair

	for _, segment := range strings.Split(raw, "&") {
		if segment == "" {
			continue
		}
		k, v, hasValue := strings.Cut(segment, "=")
		if _, drop := drop[strings.ToLower(k)]; drop {
			continue
		}
		if hasValue {
			kept = append(kept, pair{k, "=" + v})
		} else {
			kept = append(kept, pair{k, ""})
		}
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].k == kept[j].k {
			return kept[i].v < kept[j].v
		}
		return kept[i].k < kept[j].k
	})

	var b strings.Builder
	for i, p := range kept {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteString(p.v)
	}
	return b.String()
}
