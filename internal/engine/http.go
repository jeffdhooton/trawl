package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

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

// Fetch implements Engine.
func (e *HTTP) Fetch(ctx context.Context, req Request) (*Result, error) {
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
