package router

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeffdhooton/trawl/internal/cache"
	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/tierlearn"
	"github.com/jeffdhooton/trawl/internal/validity"
)

type fakeEngine struct {
	name   string
	result *engine.Result
	err    error
	calls  int
}

func (f *fakeEngine) Name() string  { return f.name }
func (f *fakeEngine) Close() error  { return nil }
func (f *fakeEngine) Fetch(_ context.Context, req engine.Request) (*engine.Result, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.result == nil {
		return nil, errors.New("fake: no result configured")
	}
	res := *f.result
	res.URL = req.URL
	return &res, nil
}

func validHTML() []byte {
	return []byte(`<html><head><title>t</title></head><body><h1>hi</h1>` +
		`<!--` + string(make([]byte, 600)) + `--></body></html>`)
}

func spaShell() []byte {
	return []byte(`<html><body><div id="root"></div>` +
		`<!--` + string(make([]byte, 600)) + `--></body></html>`)
}

func TestRouteFirstTierValid(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}
	chromiumE := &fakeEngine{name: "chromium"}

	r, err := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != "http" {
		t.Errorf("tier = %q, want http", out.Tier)
	}
	if chromiumE.calls != 0 {
		t.Errorf("chromium should not have been called")
	}
	if len(out.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(out.Attempts))
	}
}

func TestRouteEscalatesOnSPAShell(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != "chromium" {
		t.Errorf("tier = %q, want chromium", out.Tier)
	}
	if httpE.calls != 1 || chromiumE.calls != 1 {
		t.Errorf("calls = http:%d chromium:%d", httpE.calls, chromiumE.calls)
	}
	if len(out.Attempts) != 2 {
		t.Errorf("attempts = %d, want 2", len(out.Attempts))
	}
}

func TestRouteDoesNotEscalateOn404(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 404, ContentType: "text/html", Body: []byte("gone"),
	}}
	chromiumE := &fakeEngine{name: "chromium"}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	_, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if chromiumE.calls != 0 {
		t.Errorf("chromium should not be called on 404")
	}
}

func TestRouteAllTiersExhausted(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err == nil {
		t.Fatal("expected error when all tiers fail")
	}
	if out.LastResult == nil {
		t.Error("LastResult should hold the last attempt's result")
	}
	if len(out.Attempts) != 2 {
		t.Errorf("attempts = %d", len(out.Attempts))
	}
}

func TestRouteFetchErrorFallsThrough(t *testing.T) {
	httpE := &fakeEngine{name: "http", err: errors.New("connection refused")}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
		Header: http.Header{},
	}}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("expected fallthrough to chromium, got %v", err)
	}
	if out.Tier != "chromium" {
		t.Errorf("tier = %q", out.Tier)
	}
}

// memCache is an in-memory tierlearn.Cache for router tests. We don't need
// persistence here; just observe and lookup semantics matching BadgerCache.
type memCache struct {
	prefs map[string]string
}

func newMemCache() *memCache                   { return &memCache{prefs: map[string]string{}} }
func (m *memCache) Preferred(host string) string { return m.prefs[host] }
func (m *memCache) Observe(host, tier string)    { m.prefs[host] = tier }
func (m *memCache) Close() error                 { return nil }

// TestRoutePreferredTierSkipsHTTP verifies that a pre-seeded cache with
// host→chromium causes Route to start at chromium and never call http.
func TestRoutePreferredTierSkipsHTTP(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}

	cache := newMemCache()
	cache.prefs["example.com"] = "chromium"

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	r.WithCache(cache)

	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != "chromium" {
		t.Errorf("tier = %q, want chromium", out.Tier)
	}
	if httpE.calls != 0 {
		t.Errorf("http should not have been called (preferred=chromium), calls=%d", httpE.calls)
	}
	if chromiumE.calls != 1 {
		t.Errorf("chromium calls = %d, want 1", chromiumE.calls)
	}
	if out.PreferredTier != "chromium" {
		t.Errorf("PreferredTier = %q, want chromium", out.PreferredTier)
	}
}

// TestRouteObserveRecordsSuccessfulTier verifies that a successful fetch
// writes the tier to the cache so the NEXT fetch from the same host
// starts there.
func TestRouteObserveRecordsSuccessfulTier(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}

	cache := newMemCache()
	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	r.WithCache(cache)

	// First fetch: http returns SPA shell, escalates to chromium which
	// succeeds. Cache should learn host→chromium.
	_, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cache.Preferred("example.com"); got != "chromium" {
		t.Errorf("cache after first fetch = %q, want chromium", got)
	}
	if httpE.calls != 1 || chromiumE.calls != 1 {
		t.Errorf("first fetch calls = http:%d chromium:%d, want 1/1", httpE.calls, chromiumE.calls)
	}

	// Second fetch from the same host should skip http entirely.
	_, err = r.Route(context.Background(), engine.Request{URL: "https://example.com/page2"})
	if err != nil {
		t.Fatal(err)
	}
	if httpE.calls != 1 {
		t.Errorf("http calls after learn = %d, want still 1", httpE.calls)
	}
	if chromiumE.calls != 2 {
		t.Errorf("chromium calls = %d, want 2", chromiumE.calls)
	}
}

// TestRouteFailureDoesNotTeach verifies that a fetch where every tier
// fails leaves the cache untouched.
func TestRouteFailureDoesNotTeach(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}

	cache := newMemCache()
	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	r.WithCache(cache)

	_, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err == nil {
		t.Fatal("expected all-tiers-exhausted error")
	}
	if got := cache.Preferred("example.com"); got != "" {
		t.Errorf("cache should not have learned from a failed fetch, got %q", got)
	}
}

// TestRouteStalePreferenceFallsThrough: cache says "lightpanda" but the
// router has no such engine. Should silently fall back to the default
// ladder and succeed.
func TestRouteStalePreferenceFallsThrough(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}

	cache := newMemCache()
	cache.prefs["example.com"] = "lightpanda" // not in the ladder

	r, _ := New([]engine.Engine{httpE}, validity.NewChecker(validity.Default()))
	r.WithCache(cache)

	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("stale preference should not fail the route: %v", err)
	}
	if out.Tier != "http" {
		t.Errorf("tier = %q, want http (default ladder)", out.Tier)
	}
}

// Compile-time check that memCache satisfies the Cache interface.
var _ tierlearn.Cache = (*memCache)(nil)

// memContentCache is an in-memory cache.Cache for content-cache tests.
type memContentCache struct {
	entries map[string]*engine.Result
	puts    int
	gets    int
}

func newMemContentCache() *memContentCache {
	return &memContentCache{entries: map[string]*engine.Result{}}
}

func (m *memContentCache) Get(url, tier string) (*engine.Result, bool) {
	m.gets++
	res, ok := m.entries[url+"|"+tier]
	if !ok {
		return nil, false
	}
	return res, true
}

func (m *memContentCache) Put(url, tier string, res *engine.Result) {
	m.puts++
	m.entries[url+"|"+tier] = res
}

func (m *memContentCache) Close() error { return nil }

var _ cache.Cache = (*memContentCache)(nil)

// TestRouteContentCacheHitSkipsFetch verifies that a pre-populated
// content cache short-circuits the engine and never calls Fetch.
func TestRouteContentCacheHitSkipsFetch(t *testing.T) {
	httpE := &fakeEngine{name: "http"} // deliberately no result — would error
	chromiumE := &fakeEngine{name: "chromium"}

	cc := newMemContentCache()
	cc.entries["https://example.com/|http"] = &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	r.WithContentCache(cc)

	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("cache hit should short-circuit, got %v", err)
	}
	if out.Tier != "http" {
		t.Errorf("tier = %q, want http", out.Tier)
	}
	if !out.FromCache {
		t.Error("outcome.FromCache = false, want true")
	}
	if httpE.calls != 0 {
		t.Errorf("http engine was called despite cache hit, calls=%d", httpE.calls)
	}
	if cc.puts != 0 {
		t.Errorf("cache Put called %d times on a hit — should be 0", cc.puts)
	}
}

// TestRouteContentCacheMissPutsOnSuccess verifies that a successful live
// fetch is written to the cache so the next request hits.
func TestRouteContentCacheMissPutsOnSuccess(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}
	chromiumE := &fakeEngine{name: "chromium"}

	cc := newMemContentCache()
	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	r.WithContentCache(cc)

	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if out.FromCache {
		t.Error("miss path reported FromCache=true")
	}
	if cc.puts != 1 {
		t.Errorf("cache Put called %d times, want 1", cc.puts)
	}
	if httpE.calls != 1 {
		t.Errorf("http calls = %d, want 1", httpE.calls)
	}
}

// TestRouteContentCacheInvalidEscalates verifies that a stale cached
// stub that fails validity gets escalated past — the cache must not
// trap the user in a bad response.
func TestRouteContentCacheInvalidEscalates(t *testing.T) {
	httpE := &fakeEngine{name: "http"} // would fail if called; cache must cover it
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}

	cc := newMemContentCache()
	// Seed the http entry with an SPA shell so validity escalates past it.
	cc.entries["https://example.com/|http"] = &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	r.WithContentCache(cc)

	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("escalation past invalid cache entry should succeed, got %v", err)
	}
	if out.Tier != "chromium" {
		t.Errorf("tier = %q, want chromium", out.Tier)
	}
	// The outcome's FromCache flag reflects whether the FINAL result came
	// from the cache. Here chromium served the win, so it should be false.
	if out.FromCache {
		t.Error("FromCache = true but the winning tier was chromium")
	}
}
