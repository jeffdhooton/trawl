package router

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/validity"
)

type fakeEngine struct {
	name   string
	result *engine.Result
	err    error
	calls  int
}

func (f *fakeEngine) Name() string  { return f.name }
func (f *fakeEngine) Close() error  { return nil }
func (f *fakeEngine) Fetch(_ context.Context, req engine.Request) (*engine.Result, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.result == nil {
		return nil, errors.New("fake: no result configured")
	}
	res := *f.result
	res.URL = req.URL
	return &res, nil
}

func validHTML() []byte {
	return []byte(`<html><head><title>t</title></head><body><h1>hi</h1>` +
		`<!--` + string(make([]byte, 600)) + `--></body></html>`)
}

func spaShell() []byte {
	return []byte(`<html><body><div id="root"></div>` +
		`<!--` + string(make([]byte, 600)) + `--></body></html>`)
}

func TestRouteFirstTierValid(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}
	chromiumE := &fakeEngine{name: "chromium"}

	r, err := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != "http" {
		t.Errorf("tier = %q, want http", out.Tier)
	}
	if chromiumE.calls != 0 {
		t.Errorf("chromium should not have been called")
	}
	if len(out.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(out.Attempts))
	}
}

func TestRouteEscalatesOnSPAShell(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
	}}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != "chromium" {
		t.Errorf("tier = %q, want chromium", out.Tier)
	}
	if httpE.calls != 1 || chromiumE.calls != 1 {
		t.Errorf("calls = http:%d chromium:%d", httpE.calls, chromiumE.calls)
	}
	if len(out.Attempts) != 2 {
		t.Errorf("attempts = %d, want 2", len(out.Attempts))
	}
}

func TestRouteDoesNotEscalateOn404(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 404, ContentType: "text/html", Body: []byte("gone"),
	}}
	chromiumE := &fakeEngine{name: "chromium"}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	_, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if chromiumE.calls != 0 {
		t.Errorf("chromium should not be called on 404")
	}
}

func TestRouteAllTiersExhausted(t *testing.T) {
	httpE := &fakeEngine{name: "http", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: spaShell(),
	}}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err == nil {
		t.Fatal("expected error when all tiers fail")
	}
	if out.LastResult == nil {
		t.Error("LastResult should hold the last attempt's result")
	}
	if len(out.Attempts) != 2 {
		t.Errorf("attempts = %d", len(out.Attempts))
	}
}

func TestRouteFetchErrorFallsThrough(t *testing.T) {
	httpE := &fakeEngine{name: "http", err: errors.New("connection refused")}
	chromiumE := &fakeEngine{name: "chromium", result: &engine.Result{
		StatusCode: 200, ContentType: "text/html", Body: validHTML(),
		Header: http.Header{},
	}}

	r, _ := New([]engine.Engine{httpE, chromiumE}, validity.NewChecker(validity.Default()))
	out, err := r.Route(context.Background(), engine.Request{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("expected fallthrough to chromium, got %v", err)
	}
	if out.Tier != "chromium" {
		t.Errorf("tier = %q", out.Tier)
	}
}
