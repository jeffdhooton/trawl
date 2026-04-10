package engine

import (
	"context"
	"net/http"
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
