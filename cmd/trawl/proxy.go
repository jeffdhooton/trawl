package main

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// proxyOpts holds the --proxy and --proxy-file flags shared by
// scrape / batch / crawl / map. Each command embeds one of these
// in its own *Opts struct and calls registerProxyFlags in the cobra
// constructor; runX then calls applyProxy to push the settings into
// the engine configs before they're used.
type proxyOpts struct {
	proxyURL       string // --proxy (single gateway)
	proxyFile      string // --proxy-file (pool, one URL per line)
	rotateOnStatus string // --rotate-on-status (comma-separated codes)
	rotateRetries  int    // --rotate-retries (max proxy swaps per tier)
}

// registerProxyFlags wires the two flags onto a cobra command.
func registerProxyFlags(cmd *cobra.Command, p *proxyOpts) {
	cmd.Flags().StringVar(&p.proxyURL, "proxy", "",
		`HTTP/HTTPS proxy URL (e.g. http://user:pass@host:port). `+
			`All requests route through this proxy.`)
	cmd.Flags().StringVar(&p.proxyFile, "proxy-file", "",
		`file of proxy URLs (one per line). Requests are routed per-domain-sticky: `+
			`each target domain is pinned to one proxy for the job's lifetime. `+
			`Chromium tier uses the first proxy in the file.`)
	cmd.Flags().StringVar(&p.rotateOnStatus, "rotate-on-status", "",
		`comma-separated HTTP status codes that trigger proxy rotation and retry `+
			`(e.g. "403,429,503"). Only effective with --proxy-file.`)
	cmd.Flags().IntVar(&p.rotateRetries, "rotate-retries", 2,
		`max proxy rotation retries per tier when --rotate-on-status fires`)
}

// proxyResult holds the outputs of applyProxy that callers need
// to wire into the router (rotation config).
type proxyResult struct {
	rotator      router.ProxyRotator // nil when single proxy or no proxy
	rotateCodes  []int               // parsed --rotate-on-status
	rotateMax    int                 // --rotate-retries
}

// applyProxy pushes the parsed proxy config into the engine configs
// and returns the rotation state for wiring into the router. Called
// from runScrape / runBatch / runCrawl / runMap after applyEvasion
// and before any engine is built.
func applyProxy(
	httpCfg *engine.HTTPConfig,
	chromiumCfg *engine.ChromiumConfig,
	opts proxyOpts,
) (proxyResult, error) {
	var pr proxyResult
	pr.rotateMax = opts.rotateRetries

	// Parse rotate-on-status codes early so validation errors surface
	// before any network I/O.
	if opts.rotateOnStatus != "" {
		codes, err := parseStatusCodes(opts.rotateOnStatus)
		if err != nil {
			return pr, fmt.Errorf("--rotate-on-status: %w", err)
		}
		pr.rotateCodes = codes
	}

	if opts.proxyURL == "" && opts.proxyFile == "" {
		return pr, nil
	}
	if opts.proxyURL != "" && opts.proxyFile != "" {
		return pr, fmt.Errorf("cannot use both --proxy and --proxy-file")
	}

	if opts.proxyURL != "" {
		u, err := url.Parse(opts.proxyURL)
		if err != nil {
			return pr, fmt.Errorf("--proxy: invalid URL: %w", err)
		}
		httpCfg.ProxyFunc = func(_ *http.Request) (*url.URL, error) {
			return u, nil
		}
		chromiumCfg.ProxyURL = opts.proxyURL
		httpCfg.ProxyEnabled = true
		// Single proxy — no rotation possible. pr.rotator stays nil.
		return pr, nil
	}

	// --proxy-file: load pool, build per-domain-sticky rotation.
	pool, err := loadProxyPool(opts.proxyFile)
	if err != nil {
		return pr, fmt.Errorf("--proxy-file: %w", err)
	}
	if len(pool) == 0 {
		return pr, fmt.Errorf("--proxy-file: no valid proxy URLs found in %s", opts.proxyFile)
	}

	rot := &domainStickyRotator{pool: pool, assigned: make(map[string]int)}
	httpCfg.ProxyFunc = rot.proxyForRequest
	// Chromium gets the first proxy in the pool — per-domain rotation
	// isn't possible without recycling the browser allocator, which is
	// too expensive. Documented limitation.
	chromiumCfg.ProxyURL = pool[0].String()
	httpCfg.ProxyEnabled = true
	pr.rotator = rot
	log.Info().Int("pool_size", len(pool)).Str("chromium_proxy", pool[0].Host).Msg("proxy pool loaded")
	return pr, nil
}

// parseStatusCodes parses a comma-separated list of HTTP status codes.
func parseStatusCodes(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	codes := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		code, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid status code %q: %w", p, err)
		}
		if code < 100 || code > 599 {
			return nil, fmt.Errorf("status code %d out of range [100,599]", code)
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("no valid status codes")
	}
	return codes, nil
}

// logProxy writes a single info-level line at job start when a proxy
// is configured.
func logProxy(opts proxyOpts) {
	if opts.proxyURL != "" {
		u, _ := url.Parse(opts.proxyURL)
		host := opts.proxyURL
		if u != nil {
			host = u.Host
		}
		log.Info().Str("proxy", host).Msg("proxy enabled")
	} else if opts.proxyFile != "" {
		log.Info().Str("proxy_file", opts.proxyFile).Msg("proxy pool enabled")
	}
}

// loadProxyPool reads a file of proxy URLs (one per line, blank lines
// and # comments skipped) and returns the parsed URLs.
func loadProxyPool(path string) ([]*url.URL, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var pool []*url.URL
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid URL %q: %w", lineNum, line, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("line %d: URL %q missing scheme or host", lineNum, line)
		}
		pool = append(pool, u)
	}
	return pool, scanner.Err()
}

// domainStickyRotator assigns each target domain a fixed proxy from
// the pool using a hash of the domain name. Thread-safe.
type domainStickyRotator struct {
	pool     []*url.URL
	mu       sync.Mutex
	assigned map[string]int // domain → pool index
}

// Rotate forces the next proxy in the pool for a given domain.
// Returns false if the pool has only one entry (rotation impossible).
// Implements router.ProxyRotator.
func (r *domainStickyRotator) Rotate(domain string) bool {
	if len(r.pool) <= 1 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	idx, ok := r.assigned[domain]
	if !ok {
		return false
	}
	r.assigned[domain] = (idx + 1) % len(r.pool)
	return true
}

func (r *domainStickyRotator) proxyForRequest(req *http.Request) (*url.URL, error) {
	domain := req.URL.Hostname()

	r.mu.Lock()
	idx, ok := r.assigned[domain]
	if !ok {
		h := fnv.New32a()
		h.Write([]byte(domain))
		idx = int(h.Sum32()) % len(r.pool)
		r.assigned[domain] = idx
	}
	r.mu.Unlock()

	return r.pool[idx], nil
}
