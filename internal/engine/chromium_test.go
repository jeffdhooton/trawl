package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// chromiumAvailable reports whether a browser chromedp can use is reachable
// on this host. We check the common macOS path plus PATH.
func chromiumAvailable() bool {
	candidates := []string{"google-chrome", "chromium", "chromium-browser", "chrome"}
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"); err == nil {
			return true
		}
		// exec.LookPath won't resolve .app bundles; stat is enough.
		if _, err := exec.Command("test", "-x", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome").Output(); err == nil {
			return true
		}
	}
	return false
}

func TestChromiumFetchStaticHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chromium test in -short mode")
	}
	if !chromiumAvailable() {
		t.Skip("chrome/chromium not found on this host")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>hello chromium</h1></body></html>`))
	}))
	defer srv.Close()

	e := NewChromium(DefaultChromiumConfig())
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := e.Fetch(ctx, Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(string(res.Body), "hello chromium") {
		t.Errorf("body missing expected content: %q", res.Body)
	}
	if !strings.Contains(res.ContentType, "text/html") {
		t.Errorf("content-type = %q", res.ContentType)
	}
}

func TestChromiumFetchRendersJS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chromium test in -short mode")
	}
	if !chromiumAvailable() {
		t.Skip("chrome/chromium not found on this host")
	}

	// Page has an empty #root shell and a script that populates it. Only a
	// JS-executing engine will see the rendered content.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html><body>
				<div id="root"></div>
				<script>
					document.getElementById('root').innerHTML = '<h1 class="title">Hydrated Content</h1>';
				</script>
			</body></html>
		`))
	}))
	defer srv.Close()

	e := NewChromium(DefaultChromiumConfig())
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := e.Fetch(ctx, Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(string(res.Body), "Hydrated Content") {
		t.Errorf("expected hydrated content in body, got: %s", res.Body)
	}
}

func TestChromiumCapturesScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chromium test in -short mode")
	}
	if !chromiumAvailable() {
		t.Skip("chrome/chromium not found on this host")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>screenshot me</h1></body></html>`))
	}))
	defer srv.Close()

	e := NewChromium(DefaultChromiumConfig())
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// WantScreenshot=false should leave Screenshot nil.
	noShot, err := e.Fetch(ctx, Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch (no screenshot): %v", err)
	}
	if noShot.Screenshot != nil {
		t.Errorf("Screenshot populated without WantScreenshot: %d bytes", len(noShot.Screenshot))
	}

	// WantScreenshot=true should return a non-empty PNG.
	withShot, err := e.Fetch(ctx, Request{URL: srv.URL, WantScreenshot: true})
	if err != nil {
		t.Fatalf("Fetch (screenshot): %v", err)
	}
	if len(withShot.Screenshot) < 100 {
		t.Fatalf("Screenshot too small to be a real PNG: %d bytes", len(withShot.Screenshot))
	}
	// PNG magic: 89 50 4e 47 0d 0a 1a 0a
	magic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if len(withShot.Screenshot) < len(magic) {
		t.Fatalf("Screenshot too short for PNG header")
	}
	for i, b := range magic {
		if withShot.Screenshot[i] != b {
			t.Errorf("Screenshot byte %d = 0x%x, want 0x%x (not a PNG?)", i, withShot.Screenshot[i], b)
			break
		}
	}
}
