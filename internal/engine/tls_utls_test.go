package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestValidateTLSPreset(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty is fine", "", false},
		{"chrome supported", "chrome", false},
		{"safari not yet shipped", "safari", true},
		{"garbage rejected", "fnord", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTLSPreset(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

// startCapturingTLSServer stands up a tls.Listen on a self-signed cert
// and returns the listener URL plus a cert pool the client can trust.
// Each accepted handshake's ClientHelloInfo is captured into store
// (only the first is kept; subsequent fetches overwrite). The server
// writes a minimal HTTP/1.1 200 response and closes the connection.
//
// Helper for both forge-on and forge-off tests so they share the same
// TLS-server fixture and can be compared apples-to-apples.
func startCapturingTLSServer(t *testing.T, store *atomic.Pointer[tls.ClientHelloInfo]) (string, *x509.CertPool, func()) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	parsed, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		// GetConfigForClient is invoked on every handshake with the
		// observed ClientHelloInfo — perfect capture point for our test.
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			snap := *chi
			store.Store(&snap)
			return nil, nil
		},
		// Server speaks HTTP/1.1 only — advertise that via ALPN so a
		// stdlib client doesn't try to establish h2 over our minimal
		// "write a 200 and close" handler.
		NextProtos: []string{"http/1.1"},
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 4096)
				_, _ = c.Read(buf)
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}(conn)
		}
	}()

	url := "https://" + ln.Addr().String() + "/"
	return url, pool, func() {
		_ = ln.Close()
		wg.Wait()
	}
}

// TestUTLSTransportEndToEnd verifies the Tier 3 transport actually
// completes a real TLS handshake against a local server with a custom
// CA pool, and that Result.Evasion.TLSMatch is stamped. The cipher
// list inspection (next test) is the proof we're sending different
// bytes; this test is the proof everything still WORKS.
func TestUTLSTransportEndToEnd(t *testing.T) {
	var captured atomic.Pointer[tls.ClientHelloInfo]
	url, pool, stop := startCapturingTLSServer(t, &captured)
	defer stop()

	cfg := DefaultHTTPConfig()
	cfg.TLSMatch = "chrome"
	cfg.TLSRootCAs = pool
	cfg.MaxRetries = 0 // make a single attempt; failure should fail the test
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: url})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if res.Evasion == nil {
		t.Fatal("Result.Evasion not stamped")
	}
	if res.Evasion.TLSMatch != "chrome" {
		t.Errorf("TLSMatch = %q, want chrome", res.Evasion.TLSMatch)
	}
	if captured.Load() == nil {
		t.Fatal("server never observed a handshake")
	}
}

// TestUTLSTransportClientHelloDiffersFromStdlib runs the same fetch
// twice (forge on / off) against the same TLS server and asserts the
// observed ClientHello cipher lists are NOT identical. This is the
// cheapest empirical proof that --tls-match chrome puts different
// bytes on the wire than Go's stdlib — without committing the test
// to a specific cipher list (which would break every time uTLS
// rolled forward).
func TestUTLSTransportClientHelloDiffersFromStdlib(t *testing.T) {
	// Forged path.
	var capturedForged atomic.Pointer[tls.ClientHelloInfo]
	urlF, poolF, stopF := startCapturingTLSServer(t, &capturedForged)
	defer stopF()

	cfgF := DefaultHTTPConfig()
	cfgF.TLSMatch = "chrome"
	cfgF.TLSRootCAs = poolF
	cfgF.MaxRetries = 0
	eF := NewHTTP(cfgF)
	defer eF.Close()
	if _, err := eF.Fetch(context.Background(), Request{URL: urlF}); err != nil {
		t.Fatalf("forged fetch: %v", err)
	}

	// Stdlib path (same server fixture, fresh capture).
	var capturedStd atomic.Pointer[tls.ClientHelloInfo]
	urlS, poolS, stopS := startCapturingTLSServer(t, &capturedStd)
	defer stopS()

	cfgS := DefaultHTTPConfig()
	cfgS.TLSRootCAs = poolS
	cfgS.MaxRetries = 0
	eS := NewHTTP(cfgS)
	defer eS.Close()
	if _, err := eS.Fetch(context.Background(), Request{URL: urlS}); err != nil {
		t.Fatalf("stdlib fetch: %v", err)
	}

	chiF := capturedForged.Load()
	chiS := capturedStd.Load()
	if chiF == nil || chiS == nil {
		t.Fatal("missing capture from one of the handshakes")
	}

	if cipherSuitesEqual(chiF.CipherSuites, chiS.CipherSuites) {
		t.Errorf("forged and stdlib ClientHello cipher lists are identical (%v) — forgery is not active",
			chiF.CipherSuites)
	}
}

func cipherSuitesEqual(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// selfSignedCert generates a self-signed TLS certificate for localhost
// and returns the cert, the private key, and a cert pool that trusts it.
func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	parsed, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return cert, pool
}

// startH2Server stands up a TLS server that supports HTTP/2 (and h1
// fallback) via the standard net/http + h2 ALPN path. Returns the
// base URL and a cleanup function.
func startH2Server(t *testing.T, cert tls.Certificate, pool *x509.CertPool) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Proto)
	})
	srv := &http.Server{
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"h2", "http/1.1"},
		},
	}
	// Use http2.ConfigureServer to wire up h2 support on the server
	// side. Without this the server would only speak h1 even though
	// NextProtos advertises h2.
	if err := http2.ConfigureServer(srv, nil); err != nil {
		t.Fatalf("h2 configure: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.TLSConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	url := "https://" + ln.Addr().String()
	return url, func() { _ = srv.Close() }
}

// startH1OnlyServer stands up a TLS server that ONLY supports
// HTTP/1.1 — no h2 ALPN. Used to verify the h1 fallback path.
func startH1OnlyServer(t *testing.T, cert tls.Certificate) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Proto)
	})
	srv := &http.Server{
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"http/1.1"},
		},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.TLSConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	url := "https://" + ln.Addr().String()
	return url, func() { _ = srv.Close() }
}

// TestUTLSH2Negotiation verifies that --tls-match chrome negotiates
// HTTP/2 against an h2-capable server. This is the core test for the
// "HTTP/2 over forged TLS" feature.
func TestUTLSH2Negotiation(t *testing.T) {
	cert, pool := selfSignedCert(t)
	url, stop := startH2Server(t, cert, pool)
	defer stop()

	cfg := DefaultHTTPConfig()
	cfg.TLSMatch = "chrome"
	cfg.TLSRootCAs = pool
	cfg.MaxRetries = 0
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: url + "/"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	// The server handler writes "proto=HTTP/2.0" when it receives h2.
	body := string(res.Body)
	if body != "proto=HTTP/2.0" {
		t.Errorf("body = %q, want %q — h2 was not negotiated", body, "proto=HTTP/2.0")
	}
}

// TestUTLSH1Fallback verifies that --tls-match chrome gracefully
// falls back to HTTP/1.1 when the server doesn't support h2.
func TestUTLSH1Fallback(t *testing.T) {
	cert, pool := selfSignedCert(t)
	url, stop := startH1OnlyServer(t, cert)
	defer stop()

	cfg := DefaultHTTPConfig()
	cfg.TLSMatch = "chrome"
	cfg.TLSRootCAs = pool
	cfg.MaxRetries = 0
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: url + "/"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	body := string(res.Body)
	if body != "proto=HTTP/1.1" {
		t.Errorf("body = %q, want %q — h1 fallback failed", body, "proto=HTTP/1.1")
	}
}

// TestUTLSALPNIncludesH2 verifies that the forged ClientHello now
// advertises both h2 and http/1.1 in its ALPN extension (matching
// real Chrome), not just http/1.1 like the pre-h2 implementation.
func TestUTLSALPNIncludesH2(t *testing.T) {
	var captured atomic.Pointer[tls.ClientHelloInfo]
	url, pool, stop := startCapturingTLSServer(t, &captured)
	defer stop()

	cfg := DefaultHTTPConfig()
	cfg.TLSMatch = "chrome"
	cfg.TLSRootCAs = pool
	cfg.MaxRetries = 0
	e := NewHTTP(cfg)
	defer e.Close()

	// The capturing server only speaks h1, so we'll exercise the
	// fallback path — but what matters here is the ClientHello ALPN.
	_, _ = e.Fetch(context.Background(), Request{URL: url})

	chi := captured.Load()
	if chi == nil {
		t.Fatal("server never observed a handshake")
	}

	hasH2 := false
	hasH1 := false
	for _, proto := range chi.SupportedProtos {
		switch proto {
		case "h2":
			hasH2 = true
		case "http/1.1":
			hasH1 = true
		}
	}
	if !hasH2 {
		t.Errorf("ClientHello ALPN %v missing h2", chi.SupportedProtos)
	}
	if !hasH1 {
		t.Errorf("ClientHello ALPN %v missing http/1.1", chi.SupportedProtos)
	}
}
