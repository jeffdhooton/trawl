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
)

// Tier 3 evasion (per docs/EVASION.md §5.3): replace Go's stdlib TLS
// stack with a forged Chrome ClientHello via refraction-networking/utls.
//
// Strategy: keep stdlib http.Transport intact (for HTTP semantics,
// connection pooling, redirects, retries) and only swap the TLS
// handshake by setting Transport.DialTLSContext. This is a much
// narrower change than swapping the whole RoundTripper for utls's
// roundtripper package — and crucially it leaves all of trawl's
// existing http.go behavior untouched on the non-TLS code path.
//
// Caveats called out in docs/EVASION.md §5.3:
//   - HelloChrome_Auto rolls forward with the utls library, NOT with
//     real Chrome. Quarterly verification against tls.peet.ws is the
//     maintenance commitment recorded in DECISIONS.md.
//   - Stale presets are reliability bugs, not feature gaps.
//   - HTTP/2 transport is NOT shipped here. Stdlib http.Transport's
//     auto-h2 path requires the conn returned by DialTLSContext to be
//     a *tls.Conn; uTLS's UConn is a different type, so stdlib falls
//     back to HTTP/1.1 framing on the wire even when ALPN negotiated
//     h2. To prevent the resulting protocol mismatch (server expects
//     h2 framing because ALPN said h2, client speaks HTTP/1.1) we
//     advertise ONLY http/1.1 in our ClientHello's ALPN list. Real
//     Chrome advertises both h2 and http/1.1, so the forged JA4
//     differs from real Chrome by exactly one ALPN entry. This is
//     the documented limitation; lifting it requires routing h2
//     traffic through golang.org/x/net/http2.Transport with a
//     custom DialTLS, which is its own follow-up.

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

// newUTLSTransport returns an http.Transport whose TLS handshake is
// performed by utls with the given Chrome-family preset. All other
// transport settings mirror the stdlib transport built in NewHTTP so
// behavior outside the handshake is unchanged.
//
// The returned transport is safe to assign to http.Client.Transport
// directly. Stdlib http.Transport will negotiate HTTP/2 via ALPN and
// the forged conn's negotiated protocol — uTLS conns implement
// tls.ConnectionState() correctly so this just works.
func newUTLSTransport(cfg HTTPConfig, preset string) (*http.Transport, error) {
	helloID, ok := supportedTLSPresets[preset]
	if !ok {
		return nil, fmt.Errorf("newUTLSTransport: unsupported preset %q", preset)
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Intentionally HTTP/1.1 only — see the package doc above. We
		// would normally set ForceAttemptHTTP2:true here, but stdlib's
		// h2 path requires DialTLSContext to return a *tls.Conn (which
		// uTLS UConn is not), so attempting h2 only causes the wire
		// protocol to disagree with what ALPN negotiated.
		// Plain TCP dial; uTLS handshake happens in DialTLSContext.
		DialContext: dialer.DialContext,
	}

	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("utls dial: parse addr %q: %w", addr, err)
		}

		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		// Honor any deadline already on the context for the handshake
		// itself. Without this a slow handshake on a stuck server can
		// outlast the caller's per-request budget.
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
		// uTLS parrots bake their ALPN extension into the spec, so
		// Config.NextProtos is ignored when we hand a HelloID. We need
		// http/1.1-only ALPN (see package doc), so grab Chrome's spec,
		// rewrite the ALPN extension, and apply via HelloCustom.
		spec, err := utls.UTLSIdToSpec(helloID)
		if err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("utls spec: %w", err)
		}
		for _, ext := range spec.Extensions {
			if alpn, ok := ext.(*utls.ALPNExtension); ok {
				alpn.AlpnProtocols = []string{"http/1.1"}
				break
			}
		}
		uconn := utls.UClient(raw, uconf, utls.HelloCustom)
		if err := uconn.ApplyPreset(&spec); err != nil {
			_ = uconn.Close()
			return nil, fmt.Errorf("utls apply preset: %w", err)
		}
		if err := uconn.HandshakeContext(ctx); err != nil {
			_ = uconn.Close()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fmt.Errorf("utls handshake: %w", err)
		}
		// Clear the dial-time deadline once the handshake is done so
		// per-request reads/writes use their own ctx-driven deadlines.
		_ = raw.SetDeadline(time.Time{})
		return uconn, nil
	}

	return transport, nil
}
