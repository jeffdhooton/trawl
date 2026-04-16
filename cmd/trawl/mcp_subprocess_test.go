package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPSubprocess verifies the end-to-end stdio path: build the
// trawl binary, spawn it as `trawl mcp`, send tool calls over JSON-RPC
// via the SDK's CommandTransport, and confirm a real scrape returns
// a populated record.
//
// This catches issues that the in-memory transport tests can't see:
// stdio framing, signal handling, log output going to stderr (not
// stdout), and the cobra wiring.
func TestMCPSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("subprocess transport behaves differently on Windows; covered by in-memory tests in internal/mcp")
	}

	// Build a fresh trawl binary into the test temp dir so we don't
	// depend on whatever's in $PATH.
	bin := filepath.Join(t.TempDir(), "trawl")
	build := exec.Command("go", "build", "-o", bin, "./")
	build.Dir = mustModuleRoot(t) + "/cmd/trawl"
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Tiny content server.
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Pad so the response clears the validity threshold.
		fmt.Fprintf(w, `<html><head><title>Subprocess Test</title></head>
		<body><h1>Hello from subprocess</h1>
		<p>%s</p></body></html>`, strings.Repeat("filler ", 100))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Spawn `trawl mcp` and connect via the SDK's CommandTransport.
	transport := &mcp.CommandTransport{Command: exec.Command(bin, "mcp")}
	transport.Command.Env = append(os.Environ(), "TRAWL_HOME="+t.TempDir())

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_scrape",
		Arguments: map[string]any{
			"url":    srv.URL + "/page",
			"format": "markdown",
			"tiers":  "http",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool reported error: %v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected at least one content block")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content not text: %T", res.Content[0])
	}
	// Decode just enough to confirm the record shape survived stdio.
	var rec output.Record
	if err := decodeRecord(tc.Text, &rec); err != nil {
		t.Fatalf("decode record: %v\ntext: %s", err, tc.Text)
	}
	if rec.StatusCode != 200 {
		t.Errorf("status = %d, want 200", rec.StatusCode)
	}
	if !strings.Contains(rec.Body, "Hello from subprocess") {
		t.Errorf("body missing expected text: %q", rec.Body)
	}
}

// mustModuleRoot returns the absolute path to the repo root by
// climbing until a go.mod is found. Subprocess tests need the
// absolute path because exec.Command's working dir doesn't inherit
// the test's runtime.GOROOT-relative shape.
func mustModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod from %s", wd)
		}
		dir = parent
	}
}

func decodeRecord(s string, rec *output.Record) error {
	return json.Unmarshal([]byte(s), rec)
}
