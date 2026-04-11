package engine

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPFetchBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<h1>hello</h1>"))
	}))
	defer srv.Close()

	e := NewHTTP(DefaultHTTPConfig())
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(string(res.Body), "hello") {
		t.Errorf("body = %q, want contains hello", res.Body)
	}
	if res.Duration <= 0 {
		t.Errorf("duration not recorded")
	}
	if !strings.Contains(res.ContentType, "text/html") {
		t.Errorf("content-type = %q", res.ContentType)
	}
}

func TestHTTPFetchFollowsRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("final"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e := NewHTTP(DefaultHTTPConfig())
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/start"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d", res.StatusCode)
	}
	if !strings.HasSuffix(res.FinalURL, "/end") {
		t.Errorf("FinalURL = %q, want suffix /end", res.FinalURL)
	}
}

func TestHTTPFetchMaxBodyBytes(t *testing.T) {
	big := strings.Repeat("x", 100_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	cfg := DefaultHTTPConfig()
	cfg.MaxBodyBytes = 1000
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Body) != 1000 {
		t.Errorf("body size = %d, want 1000", len(res.Body))
	}
}

func TestHTTPFetchDeclaredUserAgent(t *testing.T) {
	var sawUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	e := NewHTTP(DefaultHTTPConfig())
	defer e.Close()

	if _, err := e.Fetch(context.Background(), Request{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sawUA, "trawl/") {
		t.Errorf("UA = %q, want prefix trawl/", sawUA)
	}
}

func TestHTTPFetchBrowserLikeHeaders(t *testing.T) {
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	cfg := DefaultHTTPConfig()
	cfg.BrowserLikeHeaders = true
	cfg.UserAgentStrategy = UAStrategyRotating
	e := NewHTTP(cfg)
	defer e.Close()

	res, err := e.Fetch(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	wantHeaders := []string{
		"Sec-Fetch-Site",
		"Sec-Fetch-Mode",
		"Sec-Fetch-Dest",
		"Sec-Ch-Ua",
		"Sec-Ch-Ua-Mobile",
		"Sec-Ch-Ua-Platform",
		"Upgrade-Insecure-Requests",
	}
	for _, h := range wantHeaders {
		if captured.Get(h) == "" {
			t.Errorf("missing browser-like header %q", h)
		}
	}
	ua := captured.Get("User-Agent")
	if !strings.Contains(ua, "Chrome") {
		t.Errorf("rotating UA = %q, want Chrome substring", ua)
	}
	if res.Evasion == nil {
		t.Fatal("Result.Evasion not stamped")
	}
	if !res.Evasion.BrowserLike {
		t.Error("Evasion.BrowserLike = false, want true")
	}
	if res.Evasion.UserAgent != ua {
		t.Errorf("Evasion.UserAgent = %q, want %q", res.Evasion.UserAgent, ua)
	}
}

func TestHTTPFetchExtraHeadersOverrideBrowserLike(t *testing.T) {
	var capturedAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	cfg := DefaultHTTPConfig()
	cfg.BrowserLikeHeaders = true
	e := NewHTTP(cfg)
	defer e.Close()

	want := "application/custom"
	hdr := http.Header{}
	hdr.Set("Accept", want)
	if _, err := e.Fetch(context.Background(), Request{URL: srv.URL, ExtraHeaders: hdr}); err != nil {
		t.Fatal(err)
	}
	if capturedAccept != want {
		t.Errorf("Accept = %q, want %q (caller header should win over browser-like default)", capturedAccept, want)
	}
}

func TestHTTPFetchCookieJarPersists(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
		_, _ = w.Write([]byte("set"))
	})
	var sawCookie string
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err == nil {
			sawCookie = c.Value
		}
		_, _ = w.Write([]byte("echo"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := DefaultHTTPConfig()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CookieJar = jar
	e := NewHTTP(cfg)
	defer e.Close()

	if _, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/set"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Fetch(context.Background(), Request{URL: srv.URL + "/echo"}); err != nil {
		t.Fatal(err)
	}
	if sawCookie != "abc123" {
		t.Errorf("cookie not replayed: got %q, want abc123", sawCookie)
	}
}

func TestHTTPFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("slow"))
	}))
	defer srv.Close()

	cfg := DefaultHTTPConfig()
	cfg.Timeout = 50 * time.Millisecond
	e := NewHTTP(cfg)
	defer e.Close()

	if _, err := e.Fetch(context.Background(), Request{URL: srv.URL}); err == nil {
		t.Error("expected timeout error, got nil")
	}
}
