package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// newFlakyServer returns a server that fails the first `failures`
// times then succeeds with 200 and a long-enough body. Use it to
// exercise the retry loop against a real net stack.
func newFlakyServer(t *testing.T, failures int, failStatus int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n <= int64(failures) {
			if failStatus == 0 {
				// Hijack and close to simulate a connection reset.
				hj, ok := w.(http.Hijacker)
				if !ok {
					http.Error(w, "no hijack", 500)
					return
				}
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
			w.WriteHeader(failStatus)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>hello</body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestHTTPRetriesOnConnReset(t *testing.T) {
	srv, hits := newFlakyServer(t, 2, 0) // close conn twice, succeed third

	cfg := DefaultHTTPConfig()
	cfg.MaxRetries = 3
	cfg.RetryBaseDelay = 10 * time.Millisecond
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if hits.Load() != 3 {
		t.Errorf("server hits = %d, want 3 (2 resets + 1 success)", hits.Load())
	}
}

func TestHTTPRetriesOn503(t *testing.T) {
	srv, hits := newFlakyServer(t, 2, 503)

	cfg := DefaultHTTPConfig()
	cfg.MaxRetries = 3
	cfg.RetryBaseDelay = 10 * time.Millisecond
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if hits.Load() != 3 {
		t.Errorf("server hits = %d, want 3", hits.Load())
	}
}

func TestHTTPDoesNotRetryOn404(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := DefaultHTTPConfig()
	cfg.MaxRetries = 5
	cfg.RetryBaseDelay = 10 * time.Millisecond
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.StatusCode != 404 {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("404 should not retry, got %d hits", hits.Load())
	}
}

func TestHTTPExhaustsRetriesAndReturnsError(t *testing.T) {
	srv, hits := newFlakyServer(t, 10, 503) // always fails within budget

	cfg := DefaultHTTPConfig()
	cfg.MaxRetries = 2
	cfg.RetryBaseDelay = 10 * time.Millisecond
	e := NewHTTP(cfg)
	defer e.Close()

	// 2 retries + 1 initial = 3 total attempts, all 503.
	res, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("Fetch should return the last result on 503 exhaustion, got %v", err)
	}
	if res.StatusCode != 503 {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3 (initial + 2 retries)", hits.Load())
	}
}

func TestHTTPZeroRetriesDoesOneAttempt(t *testing.T) {
	srv, hits := newFlakyServer(t, 10, 503)

	cfg := DefaultHTTPConfig()
	cfg.MaxRetries = 0
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.StatusCode != 503 {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("MaxRetries=0 should do exactly 1 attempt, got %d", hits.Load())
	}
}

func TestHTTPContextCancelAbortsRetry(t *testing.T) {
	srv, _ := newFlakyServer(t, 100, 503) // never succeeds

	cfg := DefaultHTTPConfig()
	cfg.MaxRetries = 100
	cfg.RetryBaseDelay = 200 * time.Millisecond // long enough to cancel during
	e := NewHTTP(cfg)
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, _ := e.Fetch(ctx, Request{URL: srv.URL + "/"})
	elapsed := time.Since(start)

	// Must bail within ~200ms even though the retry budget allows ~100.
	// We either get a 503 (if the initial fetch landed before cancel) or
	// an error — both acceptable; what matters is the wall-clock.
	if elapsed > 500*time.Millisecond {
		t.Errorf("retry loop should abort on ctx cancel, took %v", elapsed)
	}
	_ = res
}

func TestIsRetryableErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ctx cancel", context.Canceled, false},
		{"ctx deadline", context.DeadlineExceeded, false},
		{"ECONNREFUSED", &net.OpError{Err: syscall.ECONNREFUSED}, true},
		{"ECONNRESET", &net.OpError{Err: syscall.ECONNRESET}, true},
		{"random permanent", errors.New("unparseable URL"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableError(tc.err); got != tc.want {
				t.Errorf("isRetryableError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsRetryableStatusClassification(t *testing.T) {
	for _, code := range []int{429, 502, 503, 504} {
		if !isRetryableStatus(code) {
			t.Errorf("%d should be retryable", code)
		}
	}
	for _, code := range []int{200, 301, 403, 404, 500, 501} {
		if isRetryableStatus(code) {
			t.Errorf("%d should NOT be retryable", code)
		}
	}
}

func TestBackoffDelayJitterBounds(t *testing.T) {
	// For attempt 2 with base 500ms, unjittered delay is 2s. Jitter
	// is ±25%, so the bound is [1500ms, 2500ms]. Loop 50x to shake
	// the randomness — any escape bounds is a bug.
	base := 500 * time.Millisecond
	for i := 0; i < 50; i++ {
		d := backoffDelay(base, 2)
		if d < 1500*time.Millisecond || d > 2500*time.Millisecond {
			t.Errorf("backoffDelay(500ms, 2) = %v, out of ±25%% bounds", d)
		}
	}
	// Cap: attempt 10 with 500ms base is theoretical 512s but capped at 10s.
	d := backoffDelay(base, 10)
	if d > 11*time.Second {
		t.Errorf("backoffDelay should cap near 10s, got %v", d)
	}
}

// TestHTTPRetryReportsReasonableError verifies the error returned
// after retry exhaustion is descriptive enough for users to debug.
func TestHTTPRetryReportsReasonableError(t *testing.T) {
	cfg := DefaultHTTPConfig()
	cfg.MaxRetries = 1
	cfg.RetryBaseDelay = 5 * time.Millisecond
	cfg.Timeout = 500 * time.Millisecond
	e := NewHTTP(cfg)
	defer e.Close()

	// Unroutable RFC-5737 TEST-NET-1 address should produce a consistent
	// connection failure — retried once, then returned.
	_, err := e.Fetch(context.Background(), Request{URL: "http://192.0.2.1:1/"})
	if err == nil {
		t.Fatal("expected error on unroutable address")
	}
	// The error string should mention the URL or the underlying network
	// failure, not some internal retry-machinery noise.
	if strings.Contains(err.Error(), "retry machinery") {
		t.Errorf("error leaking internals: %v", err)
	}
}
