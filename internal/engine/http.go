package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/jeffdhooton/trawl/internal/version"
)

// HTTPConfig tunes the HTTP engine. Zero values get sensible defaults.
type HTTPConfig struct {
	UserAgent       string
	Timeout         time.Duration
	MaxBodyBytes    int64
	MaxIdleConns    int
	IdleConnTimeout time.Duration
	FollowRedirects bool
	MaxRedirects    int
	// MaxRetries is the number of RETRY attempts after the initial
	// fetch. Zero disables retries (single attempt). Only transient
	// errors — network failures and HTTP 429/5xx — are retried.
	// Permanent failures (4xx except 429, TLS cert errors, ctx
	// cancellation) return on the first attempt. Default: 2
	// (= 3 total attempts).
	MaxRetries int
	// RetryBaseDelay is the base for the exponential backoff between
	// retries. Actual delay is RetryBaseDelay * 2^attempt, capped at
	// 10 seconds, with ±25% jitter. Default: 500ms.
	RetryBaseDelay time.Duration
}

// DefaultHTTPConfig returns production-sensible defaults.
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		UserAgent:       "trawl/" + version.Version + " (+https://github.com/jeffdhooton/trawl)",
		Timeout:         30 * time.Second,
		MaxBodyBytes:    20 << 20, // 20 MiB
		MaxIdleConns:    200,
		IdleConnTimeout: 90 * time.Second,
		FollowRedirects: true,
		MaxRedirects:    10,
		MaxRetries:      2,
		RetryBaseDelay:  500 * time.Millisecond,
	}
}

// HTTP is the tier-1 engine: stdlib net/http with a tuned transport.
type HTTP struct {
	cfg    HTTPConfig
	client *http.Client
}

// NewHTTP constructs an HTTP engine.
func NewHTTP(cfg HTTPConfig) *HTTP {
	if cfg.UserAgent == "" {
		cfg = DefaultHTTPConfig()
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 20 << 20
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = 200
	}
	if cfg.IdleConnTimeout == 0 {
		cfg.IdleConnTimeout = 90 * time.Second
	}
	if cfg.MaxRedirects == 0 {
		cfg.MaxRedirects = 10
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
	}

	e := &HTTP{cfg: cfg}
	e.client = &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !cfg.FollowRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) >= cfg.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", cfg.MaxRedirects)
			}
			return nil
		},
	}
	return e
}

// Name implements Engine.
func (e *HTTP) Name() string { return "http" }

// Close releases idle connections.
func (e *HTTP) Close() error {
	e.client.CloseIdleConnections()
	return nil
}

// Fetch implements Engine. It wraps fetchOnce in a retry loop for
// transient failures: network errors, HTTP 429, and HTTP 5xx. 4xx
// (except 429), TLS cert errors, and ctx cancellation return on the
// first attempt. Backoff is exponential with ±25% jitter, capped at
// 10s. The overall wall-clock is implicitly bounded by the caller's
// ctx deadline — each retry checks ctx.Err() before sleeping AND
// before the next attempt.
//
// Duration in the returned Result is the time of the LAST (successful)
// attempt only — retries don't accumulate into the per-tier duration
// stats so a successful-after-retry fetch isn't conflated with a slow
// page load.
func (e *HTTP) Fetch(ctx context.Context, req Request) (*Result, error) {
	maxRetries := e.cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	base := e.cfg.RetryBaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}

	var (
		res     *Result
		lastErr error
	)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			// Caller cancelled/timeout — return the error we have,
			// falling back to ctx.Err if we've never actually tried.
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}

		res, lastErr = e.fetchOnce(ctx, req)
		if lastErr == nil && !isRetryableStatus(res.StatusCode) {
			return res, nil
		}
		// Retry decision: error type, status code, and remaining budget.
		if attempt == maxRetries {
			break
		}
		if lastErr != nil && !isRetryableError(lastErr) {
			return nil, lastErr
		}

		// Sleep with jittered exponential backoff. If the sleep would
		// overshoot the caller's deadline, bail out now with the last
		// error rather than waking up only to discover ctx has died.
		delay := backoffDelay(base, attempt)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
			break
		}
		reason := "transient network error"
		if lastErr == nil && res != nil {
			reason = fmt.Sprintf("retryable status %d", res.StatusCode)
		}
		log.Debug().
			Str("url", req.URL).
			Int("attempt", attempt+1).
			Str("delay", delay.String()).
			Str("reason", reason).
			Msg("http retry")

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, ctx.Err()
		}
	}

	// Exhausted retries. If the LAST attempt produced a result (even a
	// retryable 5xx) we return it so the router still sees the evidence
	// and its validity check can categorize the failure.
	if res != nil && lastErr == nil {
		return res, nil
	}
	return nil, lastErr
}

// fetchOnce is one HTTP attempt — the entire body of the old Fetch.
// All retry concerns live in Fetch; this function just talks to the
// wire.
func (e *HTTP) fetchOnce(ctx context.Context, req Request) (*Result, error) {
	start := time.Now()

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	hreq.Header.Set("User-Agent", e.cfg.UserAgent)
	hreq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	hreq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, vs := range req.ExtraHeaders {
		for _, v := range vs {
			hreq.Header.Add(k, v)
		}
	}

	resp, err := e.client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if e.cfg.MaxBodyBytes > 0 {
		reader = io.LimitReader(resp.Body, e.cfg.MaxBodyBytes)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return &Result{
		URL:         req.URL,
		FinalURL:    resp.Request.URL.String(),
		StatusCode:  resp.StatusCode,
		Header:      resp.Header,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		Duration:    time.Since(start),
	}, nil
}

// isRetryableError classifies a fetch error as transient (retry) or
// permanent (give up). The goal is to retry on TCP-level flakiness
// without wasting budget on semantic failures (bad cert, unparseable
// URL, etc).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// Context cancellation / deadline exhausted → caller gave up,
	// not our call.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// TLS certificate verification errors are permanent — no amount
	// of retrying fixes an expired cert.
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return false
	}
	var unknownAuthErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthErr) {
		return false
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return false
	}
	// net.Error with Timeout is a classic transient.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// Common transient syscalls: connection refused, connection
	// reset by peer, network unreachable.
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	// Truncated response bodies during read — retry once to see if
	// the server was just cranky.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// Bare io.EOF at the Client.Do layer means "server hung up
	// before sending a complete response" — a transient failure.
	// Distinct from an EOF during body read (already covered by
	// io.ErrUnexpectedEOF above).
	if errors.Is(err, io.EOF) {
		return true
	}
	// net.ErrClosed: we attempted to write to a connection that was
	// closed under us. Transient at the TCP layer.
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	// Default: retry any OpError / url.Error at the net layer. The
	// stdlib wraps most transport failures in these types, and
	// conservatively retrying them is safer than silently dropping.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

// isRetryableStatus returns true for HTTP status codes that suggest
// the server is temporarily unable to serve the request.
func isRetryableStatus(code int) bool {
	switch code {
	case 429, 502, 503, 504:
		return true
	}
	return false
}

// backoffDelay computes the exponential backoff for a given attempt
// index (0-based). Capped at 10 seconds (inclusive of jitter — the
// observed delay never exceeds the cap), with ±25% jitter so N
// retrying workers don't synchronize their retries against a single
// sick server.
func backoffDelay(base time.Duration, attempt int) time.Duration {
	const maxDelay = 10 * time.Second
	// 2^attempt with saturation.
	mult := 1 << attempt
	if mult > 64 {
		mult = 64
	}
	d := base * time.Duration(mult)
	// Jitter: ±25%.
	jitter := float64(d) * 0.25
	delta := (rand.Float64()*2 - 1) * jitter
	d += time.Duration(delta)
	if d > maxDelay {
		d = maxDelay
	}
	if d < 0 {
		d = 0
	}
	return d
}
