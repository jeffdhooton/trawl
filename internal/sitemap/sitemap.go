// Package sitemap discovers and enumerates URLs advertised by a site's
// sitemap(s) per https://www.sitemaps.org/protocol.html.
//
// Discovery order:
//
//  1. Fetch <base>/robots.txt and extract any "Sitemap:" directives.
//  2. If robots.txt is missing or declares no sitemaps, fall back to the
//     well-known paths /sitemap.xml and /sitemap_index.xml.
//  3. For each discovered sitemap, fetch and parse. A <sitemapindex>
//     recursively enumerates its children up to MaxDepth.
//  4. URLs from <url><loc>...</loc></url> entries are collected, deduped
//     in the order first seen, and returned.
//
// The package is intentionally general-purpose: it has no knowledge of
// pricing pages, SaaS conventions, or any domain-specific heuristics. It
// returns whatever the site author declared.
package sitemap

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/temoto/robotstxt"
)

// Options controls Discover behavior. Zero values pick sensible defaults.
type Options struct {
	// HTTPClient used for all fetches. Nil falls back to a client derived
	// from http.DefaultTransport with Timeout set.
	HTTPClient *http.Client
	// UserAgent for every request. Empty defaults to "trawl-sitemap".
	UserAgent string
	// MaxURLs caps the total number of URLs returned across all sitemaps.
	// Protects callers from unbounded memory growth on sites that advertise
	// hundreds of thousands of URLs. Default 50000. Set to 0 for unlimited.
	MaxURLs int
	// MaxDepth caps sitemap-index recursion. Protects against cyclic or
	// pathologically deep sitemap trees. Default 3.
	MaxDepth int
	// Timeout bounds each individual fetch. Default 30s.
	Timeout time.Duration
}

// Trace records what Discover did so callers can log, debug, or surface
// it to users. Every fetch attempt (successful or not) produces one Source.
type Trace struct {
	// RobotsURL is the exact URL Discover fetched for robots.txt.
	RobotsURL string
	// RobotsFetched is true if robots.txt returned a 2xx body (even if it
	// had zero Sitemap: directives).
	RobotsFetched bool
	// SitemapsFromRobots is the Sitemap: directives found in robots.txt.
	SitemapsFromRobots []string
	// Sources is the ordered list of sitemaps Discover actually fetched.
	Sources []Source
	// Truncated is true if the MaxURLs cap was hit and further URLs were
	// discarded.
	Truncated bool
}

// Source records one sitemap fetch attempt.
type Source struct {
	URL        string
	FetchedAt  time.Time
	DurationMS int64
	StatusCode int
	// URLSetCount is the number of <url><loc> entries extracted from this
	// file (zero if this was a sitemap-index).
	URLSetCount int
	// IndexCount is the number of <sitemap><loc> entries extracted (zero
	// if this was a urlset).
	IndexCount int
	// Depth is the recursion depth at which this sitemap was fetched
	// (0 = top-level, 1 = referenced by a top-level index, ...).
	Depth int
	// Error is non-empty if the fetch or parse failed. Discover does not
	// abort on per-source errors; it logs them here and continues.
	Error string
}

// Common well-known sitemap paths probed when robots.txt has no Sitemap:
// directive. Order matters — /sitemap.xml is by far the most common.
var wellKnownPaths = []string{
	"/sitemap.xml",
	"/sitemap_index.xml",
	"/sitemap-index.xml",
}

// Discover runs the discovery protocol against baseURL and returns the
// deduped URL list, a Trace, and a fatal error if the input itself was
// unusable (e.g. baseURL fails to parse). Per-source errors are recorded
// in Trace.Sources and do not abort the run.
func Discover(ctx context.Context, baseURL string, opts Options) ([]string, Trace, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, Trace{}, fmt.Errorf("parse base: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, Trace{}, fmt.Errorf("base must be http or https, got %q", u.Scheme)
	}
	opts = opts.withDefaults()
	client := opts.HTTPClient

	trace := Trace{}

	// Step 1: fetch robots.txt and extract Sitemap: directives.
	robotsURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}).String()
	trace.RobotsURL = robotsURL
	robotsSitemaps, robotsOK := fetchRobotsSitemaps(ctx, client, robotsURL, opts.UserAgent, opts.Timeout)
	trace.RobotsFetched = robotsOK
	trace.SitemapsFromRobots = robotsSitemaps

	// Step 2: decide the seed list of sitemap URLs to walk.
	seeds := robotsSitemaps
	if len(seeds) == 0 {
		for _, p := range wellKnownPaths {
			seeds = append(seeds, (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: p}).String())
		}
	}

	// Step 3: walk. Breadth-first: each discovered sitemap-index queues
	// its children at depth+1. MaxDepth guards against cycles.
	seen := map[string]bool{} // dedupe sitemap URLs (visit each once)
	urlSeen := map[string]bool{}
	var urls []string

	type queueItem struct {
		url   string
		depth int
	}
	queue := make([]queueItem, 0, len(seeds))
	for _, s := range seeds {
		queue = append(queue, queueItem{url: s, depth: 0})
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if item.depth > opts.MaxDepth {
			continue
		}
		if seen[item.url] {
			continue
		}
		seen[item.url] = true

		src, children, pageURLs := fetchSitemap(ctx, client, item.url, opts.UserAgent, opts.Timeout)
		src.Depth = item.depth
		trace.Sources = append(trace.Sources, src)

		// Child sitemap-indexes are queued for recursion.
		for _, child := range children {
			queue = append(queue, queueItem{url: child, depth: item.depth + 1})
		}

		// URL entries are deduped and added up to the MaxURLs cap.
		for _, pu := range pageURLs {
			if opts.MaxURLs > 0 && len(urls) >= opts.MaxURLs {
				trace.Truncated = true
				return urls, trace, nil
			}
			if urlSeen[pu] {
				continue
			}
			urlSeen[pu] = true
			urls = append(urls, pu)
		}
	}

	return urls, trace, nil
}

func (o Options) withDefaults() Options {
	if o.MaxURLs == 0 {
		o.MaxURLs = 50000
	}
	if o.MaxDepth == 0 {
		o.MaxDepth = 3
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.UserAgent == "" {
		o.UserAgent = "trawl-sitemap"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: o.Timeout}
	}
	return o
}

// fetchRobotsSitemaps fetches baseHost/robots.txt and returns the list of
// Sitemap: directives it declares. Returns ok=false if robots.txt was
// unreachable or did not return 2xx — callers treat that as "no data,"
// not as a fatal error.
func fetchRobotsSitemaps(ctx context.Context, client *http.Client, robotsURL, ua string, timeout time.Duration) ([]string, bool) {
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, false
	}
	data, err := robotstxt.FromBytes(body)
	if err != nil {
		return nil, false
	}
	return data.Sitemaps, true
}

// fetchSitemap fetches one sitemap URL and parses it as either <urlset>
// or <sitemapindex>. Returns the Source record, the list of child sitemap
// URLs (if this was an index), and the list of page URLs (if this was a
// urlset). Per-file errors live on Source.Error; fetch or parse failure
// does not abort discovery.
//
// Named returns matter here: the deferred duration update needs to write
// to the same variable that flows back to the caller, and positional
// returns would copy src before the defer runs.
func fetchSitemap(ctx context.Context, client *http.Client, sitemapURL, ua string, timeout time.Duration) (src Source, children []string, pageURLs []string) {
	src.URL = sitemapURL
	src.FetchedAt = time.Now().UTC()
	start := time.Now()
	defer func() {
		src.DurationMS = time.Since(start).Milliseconds()
	}()

	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, sitemapURL, nil)
	if err != nil {
		src.Error = err.Error()
		return src, nil, nil
	}
	req.Header.Set("User-Agent", ua)
	// Accept gzipped responses explicitly so servers that only gzip when
	// asked will still compress. Go's http.Transport will transparently
	// decompress unless we set Accept-Encoding ourselves — we want manual
	// handling so we can also deal with .xml.gz files served as-is.
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		src.Error = err.Error()
		return src, nil, nil
	}
	defer resp.Body.Close()
	src.StatusCode = resp.StatusCode

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		src.Error = fmt.Sprintf("http %d", resp.StatusCode)
		return src, nil, nil
	}

	var reader io.Reader = io.LimitReader(resp.Body, 100<<20) // 100 MiB cap
	// Unwrap gzip either by Content-Encoding header or by .gz URL suffix.
	needsGunzip := resp.Header.Get("Content-Encoding") == "gzip" ||
		strings.HasSuffix(strings.ToLower(sitemapURL), ".gz")
	if needsGunzip {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			src.Error = "gunzip: " + err.Error()
			return src, nil, nil
		}
		defer gz.Close()
		reader = gz
	}

	var parseErr error
	children, pageURLs, parseErr = parseSitemap(reader)
	if parseErr != nil {
		src.Error = "parse: " + parseErr.Error()
		return src, nil, nil
	}
	src.IndexCount = len(children)
	src.URLSetCount = len(pageURLs)
	return src, children, pageURLs
}

// parseSitemap streams either <urlset> or <sitemapindex> and returns the
// two flavors of <loc> entries. Uses encoding/xml's streaming Decoder so
// a 100 MiB sitemap doesn't balloon memory.
//
// The parser intentionally does not validate that a file mixes only one
// of the two element types — it extracts whatever <loc> entries it finds
// under either <url> or <sitemap> parents. In practice sitemaps never mix
// the two, but tolerant parsing is safer than strict.
func parseSitemap(r io.Reader) (children, pageURLs []string, err error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false // tolerate encoding declarations, comments, etc.

	// stack tracks the current element path as a tiny slice so <loc> can
	// look up which parent it belongs to.
	var stack []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			if t.Name.Local == "loc" && len(stack) >= 2 {
				var loc string
				if err := dec.DecodeElement(&loc, &t); err != nil {
					// Pop ourselves since DecodeElement consumed the end tag.
					stack = stack[:len(stack)-1]
					continue
				}
				stack = stack[:len(stack)-1]
				loc = strings.TrimSpace(loc)
				if loc == "" {
					continue
				}
				parent := ""
				if len(stack) > 0 {
					parent = stack[len(stack)-1]
				}
				switch parent {
				case "url":
					pageURLs = append(pageURLs, loc)
				case "sitemap":
					children = append(children, loc)
				}
			}
		case xml.EndElement:
			if len(stack) > 0 && stack[len(stack)-1] == t.Name.Local {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return children, pageURLs, nil
}

