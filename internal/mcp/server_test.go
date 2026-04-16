package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// withTestSetup spins up a tiny content server and connects an
// in-memory MCP client/server pair. Returns the client session +
// the test server's base URL. Tests just call session.CallTool and
// inspect the result.
func withTestSetup(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()

	// Hermetic TRAWL_HOME so cache/job dirs don't pollute the user's
	// home — same convention as cmd/trawl integration tests.
	t.Setenv("TRAWL_HOME", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/p/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/p/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Pad to >512 bytes so validity.Default's MinBodyBytes
		// threshold doesn't escalate to chromium (which isn't
		// configured in these unit tests).
		fmt.Fprintf(w, `<html><head><title>Page %s</title></head>
		<body><h1 class="t">Title %s</h1>
		<p>Body content %s. %s</p>
		<a href="/p/next">next</a>
		</body></html>`, id, id, id, strings.Repeat("padding ", 80))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/p/1</loc></url>
  <url><loc>%s/p/2</loc></url>
  <url><loc>%s/p/3</loc></url>
</urlset>`, base, base, base)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	server := NewServer()
	t1, t2 := mcp.NewInMemoryTransports()

	ctx := context.Background()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session, srv.URL
}

// firstRecord parses the first TextContent block of a CallToolResult
// as an output.Record. Each tool that returns records emits one
// TextContent per record (per result.go's recordResult).
func firstRecord(t *testing.T, res *mcp.CallToolResult) output.Record {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("result had no content blocks; full result: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content block is not TextContent: %T", res.Content[0])
	}
	var rec output.Record
	if err := json.Unmarshal([]byte(tc.Text), &rec); err != nil {
		t.Fatalf("parse record JSON: %v\ntext was: %s", err, tc.Text)
	}
	return rec
}

func textLines(res *mcp.CallToolResult) []string {
	out := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out = append(out, tc.Text)
		}
	}
	return out
}

func TestToolsRegisteredHasFiveTools(t *testing.T) {
	session, _ := withTestSetup(t)
	ctx := context.Background()

	want := map[string]bool{
		"trawl_scrape":  false,
		"trawl_batch":   false,
		"trawl_crawl":   false,
		"trawl_map":     false,
		"trawl_sitemap": false,
	}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("tools list error: %v", err)
		}
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %s not registered", name)
		}
	}
}

func TestScrapeTool_HappyPath(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_scrape",
		Arguments: map[string]any{
			"url":    base + "/p/42",
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
	rec := firstRecord(t, res)
	if rec.StatusCode != 200 {
		t.Errorf("status = %d, want 200", rec.StatusCode)
	}
	if rec.BodyFormat != "markdown" {
		t.Errorf("body format = %q, want markdown", rec.BodyFormat)
	}
	if !strings.Contains(rec.Body, "Title 42") {
		t.Errorf("body missing expected text; got %q", rec.Body)
	}
	if rec.Metadata.Page == nil || rec.Metadata.Page.Title != "Page 42" {
		t.Errorf("metadata.page.title = %v, want 'Page 42'", rec.Metadata.Page)
	}
}

func TestScrapeTool_RejectsEmptyURL(t *testing.T) {
	session, _ := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "trawl_scrape",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for empty URL")
	}
}

func TestBatchTool_HappyPath(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	urls := []string{base + "/p/1", base + "/p/2", base + "/p/3"}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_batch",
		Arguments: map[string]any{
			"urls":      urls,
			"selectors": []string{"title=h1.t"},
			"tiers":     "http",
			"timeoutMs": 5000,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("batch reported error: %v", res.Content)
	}
	if got := len(res.Content); got != 3 {
		t.Errorf("got %d content blocks, want 3", got)
	}
	// Inspect each record for status 200 + the expected extracted title.
	for _, line := range textLines(res) {
		var rec output.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse record: %v", err)
		}
		if rec.StatusCode != 200 {
			t.Errorf("record %s: status %d", rec.URL, rec.StatusCode)
		}
		title, _ := rec.Extracted["title"].(string)
		if !strings.HasPrefix(title, "Title ") {
			t.Errorf("record %s: title = %q", rec.URL, title)
		}
	}
}

func TestBatchTool_OversizeURLListRejected(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	urls := make([]string, BatchURLLimit+1)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/p/%d", base, i)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "trawl_batch",
		Arguments: map[string]any{"urls": urls},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for oversize batch")
	}
	// Error should mention the limit + the CLI hint.
	msg := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(msg, "trawl batch") {
		t.Errorf("error should hint at CLI alternative; got %q", msg)
	}
}

func TestSitemapTool_ReturnsURLs(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "trawl_sitemap",
		Arguments: map[string]any{"url": base},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("sitemap reported error: %v", res.Content)
	}
	urls := textLines(res)
	if len(urls) != 3 {
		t.Errorf("got %d URLs, want 3", len(urls))
	}
}

func TestMapTool_RejectsBadSources(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_map",
		Arguments: map[string]any{
			"seed":    base,
			"sources": "potato",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for bad sources value")
	}
}

func TestMapTool_HappyPath(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_map",
		Arguments: map[string]any{
			"seed":      base,
			"sources":   "sitemap",
			"timeoutMs": 5000,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("map reported error: %v", res.Content)
	}
	urls := textLines(res)
	if len(urls) != 3 {
		t.Errorf("got %d URLs, want 3", len(urls))
	}
}

func TestCrawlTool_RequiresLimit(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_crawl",
		Arguments: map[string]any{
			"seed":  base + "/p/1",
			"depth": 1,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for missing limit")
	}
}

func TestCrawlTool_OversizeLimitRejected(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_crawl",
		Arguments: map[string]any{
			"seed":  base + "/p/1",
			"depth": 1,
			"limit": CrawlURLLimit + 1,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for oversize crawl limit")
	}
}

func TestCrawlTool_HappyPath(t *testing.T) {
	session, base := withTestSetup(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "trawl_crawl",
		Arguments: map[string]any{
			"seed":       base + "/p/1",
			"depth":      1,
			"limit":      5,
			"sameDomain": true,
			"tiers":      "http",
			"timeoutMs":  5000,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("crawl reported error: %v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected at least one record")
	}
}
