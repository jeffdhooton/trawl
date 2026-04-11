package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
