package politeness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestAllowedRespectsRobots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	g := NewGate(Default(), srv.Client())
	ctx := context.Background()

	ok, err := g.Allowed(ctx, srv.URL+"/public/page")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("public path should be allowed")
	}

	blocked, err := g.Allowed(ctx, srv.URL+"/private/secret")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("private path should be blocked")
	}
}

func TestAllowedMissingRobotsDefaultsOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	g := NewGate(Default(), srv.Client())
	ok, err := g.Allowed(context.Background(), srv.URL+"/anything")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("missing robots.txt should default to allowed")
	}
}

func TestAllowedIgnoreRobots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := Default()
	cfg.IgnoreRobots = true
	g := NewGate(cfg, srv.Client())

	ok, err := g.Allowed(context.Background(), srv.URL+"/private")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("IgnoreRobots should override disallow")
	}
}

func TestAcquireRateLimitsPerDomain(t *testing.T) {
	cfg := Default()
	cfg.RatePerDomain = rate.Limit(10) // 10 rps, burst 1
	cfg.BurstPerDomain = 1
	cfg.MaxConcurrentPerDomain = 10
	g := NewGate(cfg, nil)

	start := time.Now()
	for range 4 {
		release, _, err := g.Acquire(context.Background(), "https://example.com/x")
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	elapsed := time.Since(start)
	// At 10 rps with burst 1, 4 requests take ~300ms minimum.
	if elapsed < 200*time.Millisecond {
		t.Errorf("4 requests took %v, expected ≥200ms due to rate limit", elapsed)
	}
}

func TestAcquireJitterAddsDelay(t *testing.T) {
	cfg := Default()
	cfg.RatePerDomain = rate.Limit(100) // 10ms baseInterval
	cfg.BurstPerDomain = 100             // burst lets us measure jitter, not rate
	cfg.JitterFraction = 1.0              // up to 100% of baseInterval = up to 10ms
	g := NewGate(cfg, nil)

	totalJitter := int64(0)
	const calls = 30
	var nonZero int
	for i := 0; i < calls; i++ {
		release, ms, err := g.Acquire(context.Background(), "https://example.com/x")
		if err != nil {
			t.Fatal(err)
		}
		release()
		totalJitter += ms
		if ms > 0 {
			nonZero++
		}
	}
	// With burst=100 the rate limiter never holds us back; every Acquire
	// goes straight to the jitter step. With JitterFraction=1.0 and ms
	// granularity, most calls should land at >= 1ms; require at least
	// half to be non-zero so we don't flake on a streak of small draws.
	if nonZero < calls/2 {
		t.Errorf("nonZero jitter calls = %d/%d, want at least half", nonZero, calls)
	}
	if totalJitter == 0 {
		t.Error("no jitter applied across 30 calls — Acquire isn't sleeping")
	}
}

func TestAcquireJitterZeroIsNoop(t *testing.T) {
	cfg := Default()
	cfg.RatePerDomain = rate.Limit(1000)
	cfg.BurstPerDomain = 1000
	cfg.JitterFraction = 0
	g := NewGate(cfg, nil)

	for i := 0; i < 10; i++ {
		release, ms, err := g.Acquire(context.Background(), "https://example.com/x")
		if err != nil {
			t.Fatal(err)
		}
		release()
		if ms != 0 {
			t.Errorf("call %d: ms = %d, want 0 (jitter disabled)", i, ms)
		}
	}
}

func TestAcquireJitterRespectsContext(t *testing.T) {
	cfg := Default()
	cfg.RatePerDomain = rate.Limit(0.5) // 2-second baseInterval
	cfg.BurstPerDomain = 10
	cfg.JitterFraction = 5.0 // up to 10s of jitter — would normally hang
	g := NewGate(cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := g.Acquire(ctx, "https://example.com/x")
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected ctx deadline error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Acquire took %v despite 50ms ctx deadline", elapsed)
	}
}

func TestAcquireCapsConcurrencyPerDomain(t *testing.T) {
	cfg := Default()
	cfg.RatePerDomain = rate.Limit(1000)
	cfg.BurstPerDomain = 1000
	cfg.MaxConcurrentPerDomain = 2
	cfg.MaxConcurrentGlobal = 100
	g := NewGate(cfg, nil)

	var active, maxActive int64
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			release, _, err := g.Acquire(context.Background(), "https://example.com/x")
			if err != nil {
				return
			}
			a := atomic.AddInt64(&active, 1)
			for {
				m := atomic.LoadInt64(&maxActive)
				if a <= m || atomic.CompareAndSwapInt64(&maxActive, m, a) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&active, -1)
			release()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	if maxActive > 2 {
		t.Errorf("maxActive = %d, want ≤ 2", maxActive)
	}
}
