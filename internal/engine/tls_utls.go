package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// Tier 3 evasion (per docs/EVASION.md §5.3): replace Go's stdlib TLS
// stack with a forged Chrome ClientHello via refraction-networking/utls.
//
// Strategy: two-transport wrapper (h2 + h1 fallback) behind a single
// RoundTripper. Both transports use the same uTLS dial function that
// forges a Chrome ClientHello with the real Chrome ALPN list
// ["h2", "http/1.1"]. After the handshake, the negotiated ALPN
// determines which transport handles the request:
//
//   - h2 negotiated → golang.org/x/net/http2.Transport (frame-level h2
//     on any net.Conn, no *tls.Conn type assertion)
//   - h1 negotiated → stdlib http.Transport (classic HTTP/1.1 path)
//
// Each transport manages its own connection pool, so the routing
// decision is per-host and amortized after the first request.
//
// Caveats:
//   - HelloChrome_Auto rolls forward with the utls library, NOT with
//     real Chrome. Quarterly verification against tls.peet.ws is the
//     maintenance commitment recorded in DECISIONS.md.
//   - Stale presets are reliability bugs, not feature gaps.
//   - HTTP/2 SETTINGS frame forging is a separate follow-up (§8.3).
//     Go's x/net/http2 sends its own SETTINGS values, which differ
//     from Chrome's. This matters only for detectors that combine
//     TLS + SETTINGS (Akamai, Cloudflare aggressive mode).

// supportedTLSPresets enumerates the preset values --tls-match accepts.
// Centralized so the flag validator and the transport builder agree.
var supportedTLSPresets = map[string]utls.ClientHelloID{
	"chrome": utls.HelloChrome_Auto,
}

// ValidateTLSPreset returns nil if name is empty (no forgery) or one
// of the supported presets. Called from cmd/trawl/evasion.go before
// the engine is constructed so a typo fails the command immediately
// instead of silently degrading to Go's stdlib fingerprint.
func ValidateTLSPreset(name string) error {
	if name == "" {
		return nil
	}
	if _, ok := supportedTLSPresets[name]; !ok {
		known := make([]string, 0, len(supportedTLSPresets))
		for k := range supportedTLSPresets {
			known = append(known, k)
		}
		return fmt.Errorf("unknown --tls-match preset %q (supported: %s)",
			name, strings.Join(known, ", "))
	}
	return nil
}

// errNotH2 is returned by the h2-only dialer when ALPN negotiated
// http/1.1 instead of h2. The utlsRoundTripper catches this and
// falls back to the h1 transport.
var errNotH2 = errors.New("utls: ALPN did not negotiate h2")

// utlsRoundTripper is a dual-protocol RoundTripper that routes each
// request through either an HTTP/2 or HTTP/1.1 transport based on
// what the server negotiated via ALPN. Both transports share the
// same uTLS dial logic (forged Chrome ClientHello with the real
// Chrome ALPN list ["h2", "http/1.1"]).
//
// On the first request to a host, h2 is tried first. If the server
// doesn't support h2 (ALPN negotiated http/1.1), the h2 dialer
// returns errNotH2, and we fall back to the h1 transport which
// dials its own connection. Each transport caches connections in
// its own pool, so subsequent requests to the same host reuse the
// established connection without extra handshakes.
type utlsRoundTripper struct {
	h2 *http2.Transport
	h1 *http.Transport
}

func (rt *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.h2.RoundTrip(req)
	if err != nil && errors.Is(err, errNotH2) {
		return rt.h1.RoundTrip(req)
	}
	return resp, err
}

// CloseIdleConnections is called by http.Client.CloseIdleConnections.
func (rt *utlsRoundTripper) CloseIdleConnections() {
	rt.h2.CloseIdleConnections()
	rt.h1.CloseIdleConnections()
}

// newUTLSTransport returns a RoundTripper whose TLS handshake is
// performed by utls with the given Chrome-family preset. The returned
// transport supports both HTTP/2 and HTTP/1.1, negotiated via ALPN
// with the real Chrome ALPN list — no forced downgrade to h1.
func newUTLSTransport(cfg HTTPConfig, preset string) (http.RoundTripper, error) {
	helloID, ok := supportedTLSPresets[preset]
	if !ok {
		return nil, fmt.Errorf("newUTLSTransport: unsupported preset %q", preset)
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	// utlsDial performs the shared TCP connect + uTLS handshake. The
	// Chrome parrot's real ALPN list (h2 + http/1.1) is preserved so
	// the JA4 fingerprint is identical to real Chrome.
	utlsDial := func(ctx context.Context, network, addr string) (*utls.UConn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("utls dial: parse addr %q: %w", addr, err)
		}

		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		if deadline, ok := ctx.Deadline(); ok {
			if err := raw.SetDeadline(deadline); err != nil {
				_ = raw.Close()
				return nil, fmt.Errorf("utls dial: set deadline: %w", err)
			}
		}

		uconf := &utls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
			RootCAs:    cfg.TLSRootCAs,
		}
		// Use the parrot's full spec as-is — no ALPN override. This
		// preserves Chrome's real ["h2", "http/1.1"] ALPN list so the
		// forged JA4 is indistinguishable from real Chrome.
		uconn := utls.UClient(raw, uconf, helloID)
		if err := uconn.HandshakeContext(ctx); err != nil {
			_ = uconn.Close()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fmt.Errorf("utls handshake: %w", err)
		}
		_ = raw.SetDeadline(time.Time{})
		return uconn, nil
	}

	// h2 transport — golang.org/x/net/http2.Transport handles frame
	// serialization on any net.Conn; no *tls.Conn type assertion.
	// The DialTLSContext dialer returns errNotH2 when the server
	// negotiated http/1.1 instead of h2, so the wrapper can fall back.
	h2t := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			uconn, err := utlsDial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if uconn.ConnectionState().NegotiatedProtocol != "h2" {
				_ = uconn.Close()
				return nil, errNotH2
			}
			return uconn, nil
		},
	}

	// h1 transport — classic stdlib http.Transport for HTTP/1.1
	// fallback. Accepts any negotiated protocol (in practice, h1).
	h1t := &http.Transport{
		Proxy:                 cfg.proxyOrDefault(),
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext:           dialer.DialContext,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			uconn, err := utlsDial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return uconn, nil
		},
	}

	return &utlsRoundTripper{h2: h2t, h1: h1t}, nil
}
