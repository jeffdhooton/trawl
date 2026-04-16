package job

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
)

// parseStatusCodes parses a comma-separated list of HTTP status codes
// (e.g. "403,429,503"). Codes outside [100,599] error so a typo can't
// silently disable rotation.
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

// loadProxyPool reads a file of proxy URLs (one per line, blank lines
// and # comments skipped) and returns the parsed URLs. Empty pool is
// not an error here — the caller decides whether that's fatal.
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

// LoadProxyPool exposes loadProxyPool for cmd/trawl's proxy-test
// subcommand (which needs the same parser). Internal callers use the
// lowercase form.
func LoadProxyPool(path string) ([]*url.URL, error) {
	return loadProxyPool(path)
}

// domainStickyRotator assigns each target domain a fixed proxy from
// the pool using a hash of the domain name. Thread-safe. Implements
// ProxyRotator.
type domainStickyRotator struct {
	pool     []*url.URL
	mu       sync.Mutex
	assigned map[string]int // domain → pool index
}

// Rotate forces the next proxy in the pool for a given domain.
// Returns false if the pool has only one entry (rotation impossible)
// or this domain hasn't been assigned a proxy yet.
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
