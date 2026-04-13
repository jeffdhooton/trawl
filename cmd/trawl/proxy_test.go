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
	err := applyProxy(&httpCfg, &chromiumCfg, opts)
	if err == nil {
		t.Fatal("expected mutual exclusion error")
	}
}

func TestApplyProxySingleURL(t *testing.T) {
	httpCfg := engine.DefaultHTTPConfig()
	chromiumCfg := engine.DefaultChromiumConfig()
	opts := proxyOpts{proxyURL: "http://user:pass@gate.proxy.com:7000"}
	if err := applyProxy(&httpCfg, &chromiumCfg, opts); err != nil {
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
