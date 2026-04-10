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
		release, err := g.Acquire(context.Background(), "https://example.com/x")
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
			release, err := g.Acquire(context.Background(), "https://example.com/x")
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
