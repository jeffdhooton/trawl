package cache

import (
	"net/http"
	"testing"
	"time"

	"github.com/jeffdhooton/trawl/internal/engine"
)

func newTestCache(t *testing.T, ttl time.Duration) *BadgerCache {
	t.Helper()
	dir := t.TempDir()
	c, err := Open(dir, Config{TTL: ttl})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sampleResult() *engine.Result {
	return &engine.Result{
		URL:         "https://example.com/page",
		FinalURL:    "https://example.com/page",
		StatusCode:  200,
		Header:      http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		ContentType: "text/html; charset=utf-8",
		Body:        []byte("<html><body>hello</body></html>"),
		Duration:    123 * time.Millisecond,
		Redirects:   []string{"https://example.com/old"},
	}
}

func TestCachePutAndGet(t *testing.T) {
	c := newTestCache(t, time.Hour)
	res := sampleResult()

	c.Put(res.URL, "http", res)

	got, ok := c.Get(res.URL, "http")
	if !ok {
		t.Fatal("Get returned miss after Put")
	}
	if string(got.Body) != string(res.Body) {
		t.Errorf("Body = %q, want %q", got.Body, res.Body)
	}
	if got.StatusCode != res.StatusCode {
		t.Errorf("StatusCode = %d, want %d", got.StatusCode, res.StatusCode)
	}
	if got.ContentType != res.ContentType {
		t.Errorf("ContentType = %q, want %q", got.ContentType, res.ContentType)
	}
	if got.FinalURL != res.FinalURL {
		t.Errorf("FinalURL = %q, want %q", got.FinalURL, res.FinalURL)
	}
	if got.Duration != res.Duration {
		t.Errorf("Duration = %v, want %v", got.Duration, res.Duration)
	}
	if len(got.Redirects) != 1 || got.Redirects[0] != res.Redirects[0] {
		t.Errorf("Redirects = %v, want %v", got.Redirects, res.Redirects)
	}
	if got.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("Header Content-Type = %q", got.Header.Get("Content-Type"))
	}
}

func TestCacheKeyIncludesTier(t *testing.T) {
	c := newTestCache(t, time.Hour)
	res := sampleResult()

	c.Put(res.URL, "http", res)
	// A request against the same URL for a different tier must miss.
	if _, ok := c.Get(res.URL, "chromium"); ok {
		t.Error("chromium hit on http-only entry — key doesn't include tier")
	}
	if _, ok := c.Get(res.URL, "http"); !ok {
		t.Error("http miss on the entry we just Put")
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	c := newTestCache(t, 50*time.Millisecond)
	res := sampleResult()
	c.Put(res.URL, "http", res)

	if _, ok := c.Get(res.URL, "http"); !ok {
		t.Fatal("fresh entry missed")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get(res.URL, "http"); ok {
		t.Error("expired entry returned a hit — TTL not applied")
	}
}

func TestCacheTTLZeroNeverExpires(t *testing.T) {
	c := newTestCache(t, 0)
	res := sampleResult()
	c.Put(res.URL, "http", res)

	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get(res.URL, "http"); !ok {
		t.Error("TTL=0 entry expired unexpectedly")
	}
}

func TestCachePersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	c1, err := Open(dir, Config{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	res := sampleResult()
	c1.Put(res.URL, "http", res)
	_ = c1.Close()

	c2, err := Open(dir, Config{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	got, ok := c2.Get(res.URL, "http")
	if !ok {
		t.Fatal("entry not found after reopen")
	}
	if string(got.Body) != string(res.Body) {
		t.Errorf("Body = %q, want %q", got.Body, res.Body)
	}
}

func TestNopCacheAlwaysMisses(t *testing.T) {
	c := NopCache{}
	res := sampleResult()
	c.Put(res.URL, "http", res)
	if _, ok := c.Get(res.URL, "http"); ok {
		t.Error("NopCache returned a hit — should always miss")
	}
}

func TestCachePutNilIsNoOp(t *testing.T) {
	c := newTestCache(t, time.Hour)
	c.Put("https://example.com", "http", nil)
	c.Put("", "http", sampleResult())
	c.Put("https://example.com", "", sampleResult())
	if _, ok := c.Get("https://example.com", "http"); ok {
		t.Error("nil/empty Put should not store anything")
	}
}
