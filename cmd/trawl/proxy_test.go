package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeffdhooton/trawl/internal/engine"
)

func TestDomainStickyRotator(t *testing.T) {
	pool := []*url.URL{
		{Scheme: "http", Host: "proxy1:8080"},
		{Scheme: "http", Host: "proxy2:8080"},
		{Scheme: "http", Host: "proxy3:8080"},
	}
	rot := &domainStickyRotator{pool: pool, assigned: make(map[string]int)}

	// Same domain always gets the same proxy.
	req1, _ := http.NewRequest("GET", "https://example.com/page1", nil)
	req2, _ := http.NewRequest("GET", "https://example.com/page2", nil)
	p1, _ := rot.proxyForRequest(req1)
	p2, _ := rot.proxyForRequest(req2)
	if p1.Host != p2.Host {
		t.Errorf("same domain got different proxies: %s vs %s", p1.Host, p2.Host)
	}

	// Different domains should spread across the pool.
	domains := []string{"a.com", "b.com", "c.com", "d.com", "e.com",
		"f.com", "g.com", "h.com", "i.com", "j.com"}
	seen := map[string]bool{}
	for _, d := range domains {
		req, _ := http.NewRequest("GET", "https://"+d+"/", nil)
		p, _ := rot.proxyForRequest(req)
		seen[p.Host] = true
	}
	if len(seen) < 2 {
		t.Errorf("10 domains all mapped to the same proxy — rotation broken (seen: %v)", seen)
	}
}

func TestLoadProxyPool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxies.txt")
	content := "http://user:pass@proxy1.example.com:8080\n" +
		"# this is a comment\n" +
		"\n" +
		"http://proxy2.example.com:9090\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pool, err := loadProxyPool(path)
	if err != nil {
		t.Fatalf("loadProxyPool: %v", err)
	}
	if len(pool) != 2 {
		t.Fatalf("pool size = %d, want 2", len(pool))
	}
	if pool[0].Host != "proxy1.example.com:8080" {
		t.Errorf("pool[0].Host = %q", pool[0].Host)
	}
	if pool[1].Host != "proxy2.example.com:9090" {
		t.Errorf("pool[1].Host = %q", pool[1].Host)
	}
}

func TestLoadProxyPoolRejectsNoScheme(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(path, []byte("proxy.example.com:8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadProxyPool(path)
	if err == nil {
		t.Fatal("expected error for URL without scheme")
	}
}

func TestApplyProxyMutualExclusion(t *testing.T) {
	httpCfg := engine.DefaultHTTPConfig()
	chromiumCfg := engine.DefaultChromiumConfig()
	opts := proxyOpts{proxyURL: "http://x:8080", proxyFile: "/tmp/p.txt"}
	_, err := applyProxy(&httpCfg, &chromiumCfg, opts)
	if err == nil {
		t.Fatal("expected mutual exclusion error")
	}
}

func TestDomainStickyRotatorRotate(t *testing.T) {
	pool := []*url.URL{
		{Scheme: "http", Host: "proxy1:8080"},
		{Scheme: "http", Host: "proxy2:8080"},
		{Scheme: "http", Host: "proxy3:8080"},
	}
	rot := &domainStickyRotator{pool: pool, assigned: make(map[string]int)}

	// Initial assignment for example.com
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	p1, _ := rot.proxyForRequest(req)
	originalHost := p1.Host

	// Rotate should move to the next proxy.
	if !rot.Rotate("example.com") {
		t.Fatal("Rotate returned false, want true")
	}
	p2, _ := rot.proxyForRequest(req)
	if p2.Host == originalHost {
		t.Errorf("after Rotate, proxy should differ: got %s again", p2.Host)
	}

	// Rotate again — should move again.
	rot.Rotate("example.com")
	p3, _ := rot.proxyForRequest(req)
	if p3.Host == p2.Host {
		t.Errorf("second Rotate didn't change proxy: %s", p3.Host)
	}

	// Rotate wraps around the pool.
	rot.Rotate("example.com")
	p4, _ := rot.proxyForRequest(req)
	if p4.Host != originalHost {
		t.Errorf("after 3 rotations (pool=3), should wrap to original: got %s, want %s", p4.Host, originalHost)
	}
}

func TestDomainStickyRotatorRotateSingleProxy(t *testing.T) {
	pool := []*url.URL{{Scheme: "http", Host: "only:8080"}}
	rot := &domainStickyRotator{pool: pool, assigned: make(map[string]int)}

	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	rot.proxyForRequest(req) // seed the assignment

	if rot.Rotate("example.com") {
		t.Fatal("Rotate should return false for single-proxy pool")
	}
}

func TestDomainStickyRotatorRotateUnknownDomain(t *testing.T) {
	pool := []*url.URL{
		{Scheme: "http", Host: "proxy1:8080"},
		{Scheme: "http", Host: "proxy2:8080"},
	}
	rot := &domainStickyRotator{pool: pool, assigned: make(map[string]int)}

	// Rotate for a domain that was never assigned.
	if rot.Rotate("never-seen.com") {
		t.Fatal("Rotate should return false for unassigned domain")
	}
}

func TestParseStatusCodes(t *testing.T) {
	tests := []struct {
		input   string
		want    []int
		wantErr bool
	}{
		{"403,429,503", []int{403, 429, 503}, false},
		{"403", []int{403}, false},
		{" 403 , 429 ", []int{403, 429}, false},
		{"", nil, true},
		{"abc", nil, true},
		{"99", nil, true},   // below 100
		{"600", nil, true},  // above 599
	}
	for _, tt := range tests {
		codes, err := parseStatusCodes(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseStatusCodes(%q) = %v, want error", tt.input, codes)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseStatusCodes(%q) error: %v", tt.input, err)
			continue
		}
		if len(codes) != len(tt.want) {
			t.Errorf("parseStatusCodes(%q) = %v, want %v", tt.input, codes, tt.want)
			continue
		}
		for i := range codes {
			if codes[i] != tt.want[i] {
				t.Errorf("parseStatusCodes(%q)[%d] = %d, want %d", tt.input, i, codes[i], tt.want[i])
			}
		}
	}
}

func TestApplyProxySingleURL(t *testing.T) {
	httpCfg := engine.DefaultHTTPConfig()
	chromiumCfg := engine.DefaultChromiumConfig()
	opts := proxyOpts{proxyURL: "http://user:pass@gate.proxy.com:7000"}
	if _, err := applyProxy(&httpCfg, &chromiumCfg, opts); err != nil {
		t.Fatalf("applyProxy: %v", err)
	}
	if httpCfg.ProxyFunc == nil {
		t.Fatal("ProxyFunc not set")
	}
	if !httpCfg.ProxyEnabled {
		t.Fatal("ProxyEnabled not set")
	}
	if chromiumCfg.ProxyURL != "http://user:pass@gate.proxy.com:7000" {
		t.Errorf("ChromiumConfig.ProxyURL = %q", chromiumCfg.ProxyURL)
	}

	// The proxy function should return the configured URL for any request.
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	u, err := httpCfg.ProxyFunc(req)
	if err != nil {
		t.Fatalf("ProxyFunc error: %v", err)
	}
	if u.Host != "gate.proxy.com:7000" {
		t.Errorf("proxy URL host = %q, want gate.proxy.com:7000", u.Host)
	}
}
