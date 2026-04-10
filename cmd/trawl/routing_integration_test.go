package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// chromiumLocallyAvailable mirrors the helper in internal/engine so the
// routing integration test can skip when chrome isn't installed.
func chromiumLocallyAvailable() bool {
	candidates := []string{"google-chrome", "chromium", "chromium-browser", "chrome"}
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.Command("test", "-x", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome").Output(); err == nil {
			return true
		}
	}
	return false
}

// TestRoutingEscalatesSPAShell boots a server with two paths:
//   - /static: serves normal HTML that the HTTP tier should succeed on
//   - /spa:    serves an empty #root shell that HTTP must escalate past to
//     chromium, which executes the inline script and gets real content
//
// The test then batches both URLs and verifies each landed on the expected
// tier by inspecting the frontier and the JSONL output.
func TestRoutingEscalatesSPAShell(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping routing integration in -short mode")
	}
	if !chromiumLocallyAvailable() {
		t.Skip("chrome/chromium not found on this host")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/static", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<h1 class="title">Static Title</h1>
			<p>` + strings.Repeat("content ", 200) + `</p>
		</body></html>`))
	})
	mux.HandleFunc("/spa", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<div id="root"></div>
			<script>
				document.getElementById('root').innerHTML =
					'<h1 class="title">Hydrated Title</h1>';
			</script>
			<!-- ` + strings.Repeat("pad ", 200) + ` -->
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	trawlHome := withTrawlHome(t)
	urlFile := writeURLFile(t, trawlHome, []string{
		srv.URL + "/static",
		srv.URL + "/spa",
	})
	outputFile := filepath.Join(trawlHome, "results.jsonl")

	opts := batchOpts{
		selectors:   []string{"title=h1.title"},
		outputPath:  outputFile,
		timeout:     60 * time.Second,
		concurrency: 2,
		ratePerSec:  50,
		jobID:       "test-routing",
		tiers:       "http,chromium",
	}

	// Chromium needs time to boot + render; give the whole job 90s.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := runBatch(ctx, urlFile, opts); err != nil {
		t.Fatalf("runBatch: %v", err)
	}

	records := readJSONL(t, outputFile)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	byPath := map[string]int{} // path -> index in records
	for i, r := range records {
		switch {
		case strings.HasSuffix(r.CanonicalURL, "/static"):
			byPath["static"] = i
		case strings.HasSuffix(r.CanonicalURL, "/spa"):
			byPath["spa"] = i
		}
	}

	staticRec := records[byPath["static"]]
	spaRec := records[byPath["spa"]]

	if staticRec.Tier != "http" {
		t.Errorf("static record tier = %q, want http", staticRec.Tier)
	}
	if spaRec.Tier != "chromium" {
		t.Errorf("spa record tier = %q, want chromium", spaRec.Tier)
	}

	staticTitle, _ := staticRec.Extracted["title"].(string)
	if staticTitle != "Static Title" {
		t.Errorf("static title = %q, want Static Title", staticTitle)
	}
	spaTitle, _ := spaRec.Extracted["title"].(string)
	if spaTitle != "Hydrated Title" {
		t.Errorf("spa title = %q, want Hydrated Title", spaTitle)
	}
}
