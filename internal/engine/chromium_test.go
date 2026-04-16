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

func TestChromiumStealthHidesWebdriver(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chromium test in -short mode")
	}
	if !chromiumAvailable() {
		t.Skip("chrome/chromium not found on this host")
	}

	// Page reads navigator.webdriver and renders the result into a div
	// that the engine can scrape from the returned HTML. Without
	// stealth, headless Chrome reports `true`. With stealth on, our
	// init script overrides the property to `undefined`, which JS
	// stringifies as "undefined".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html><body>
				<div id="result"></div>
				<script>
					document.getElementById('result').textContent = String(navigator.webdriver);
				</script>
			</body></html>
		`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Baseline: stealth OFF — headless reports navigator.webdriver = true.
	plain := NewChromium(DefaultChromiumConfig())
	defer plain.Close()
	plainRes, err := plain.Fetch(ctx, Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("plain Fetch: %v", err)
	}
	if !strings.Contains(string(plainRes.Body), `id="result">true<`) {
		t.Errorf("baseline expected webdriver=true, got: %s", plainRes.Body)
	}

	// Stealth ON — script patches webdriver to undefined.
	stealthCfg := DefaultChromiumConfig()
	stealthCfg.Stealth = true
	stealthEng := NewChromium(stealthCfg)
	defer stealthEng.Close()
	stealthRes, err := stealthEng.Fetch(ctx, Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("stealth Fetch: %v", err)
	}
	if !strings.Contains(string(stealthRes.Body), `id="result">undefined<`) {
		t.Errorf("stealth expected webdriver=undefined, got: %s", stealthRes.Body)
	}
	if stealthRes.Evasion == nil || !stealthRes.Evasion.Stealth {
		t.Error("Result.Evasion.Stealth not stamped on stealth fetch")
	}
}

// TestChromiumStealthDepthPatches covers the v0.8.2 stealth additions:
// canvas fingerprint noise, rich chrome object, expanded permissions,
// outer/inner dimension realism, and deviceMemory/hardwareConcurrency
// defaults. Each assertion targets one patch so a regression localizes.
func TestChromiumStealthDepthPatches(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chromium test in -short mode")
	}
	if !chromiumAvailable() {
		t.Skip("chrome/chromium not found on this host")
	}

	// Probe page renders checks synchronously into #sync, then async
	// permission result into #async. The engine captures the DOM after
	// page load, so we populate #sync right away (all our patches are
	// synchronous) and write #async from the permission promise chain.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html><body>
				<div id="sync"></div>
				<div id="async">pending</div>
				<script>
					// Canvas probe: draw once, sample twice. Both must
					// produce the same hash (per-document seed stable).
					const c = document.createElement('canvas');
					c.width = 200; c.height = 50;
					const ctx = c.getContext('2d');
					ctx.fillStyle = '#f60';
					ctx.fillRect(10, 10, 120, 30);
					ctx.fillStyle = '#069';
					ctx.font = '16px Arial';
					ctx.fillText('trawl fp probe', 20, 30);
					const cHashA = c.toDataURL();
					const cHashB = c.toDataURL();

					const parts = [];
					const push = (k, v) => parts.push(k + '=' + String(v));
					push('webdriver', navigator.webdriver);
					push('plugins_len', navigator.plugins.length);
					push('langs_len', (navigator.languages || []).length);
					push('chrome_app', typeof window.chrome.app);
					push('chrome_csi_type', typeof window.chrome.csi);
					push('chrome_loadTimes_type', typeof window.chrome.loadTimes);
					push('chrome_runtime', typeof window.chrome.runtime);
					push('notif_perm', typeof Notification !== 'undefined' ? Notification.permission : 'missing');
					push('devmem_typeof', typeof navigator.deviceMemory);
					push('devmem_gte1', (navigator.deviceMemory || 0) >= 1);
					push('hwconc_typeof', typeof navigator.hardwareConcurrency);
					push('hwconc_gte1', (navigator.hardwareConcurrency || 0) >= 1);
					push('canvas_same_doc_stable', cHashA === cHashB);
					push('canvas_len_nonzero', cHashA.length > 0);
					document.getElementById('sync').textContent = parts.join(';');

					// Async: geolocation permission shim.
					navigator.permissions.query({name: 'geolocation'})
						.then(r => { document.getElementById('async').textContent = 'geo_perm=' + r.state; })
						.catch(err => { document.getElementById('async').textContent = 'geo_perm=err:' + err.message; });
				</script>
			</body></html>
		`))
	}))
	defer srv.Close()

	cfg := DefaultChromiumConfig()
	cfg.Stealth = true
	e := NewChromium(cfg)
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := e.Fetch(ctx, Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	body := string(res.Body)

	// Helper: assert substring is present; miss → report full body
	// for easier diagnosis than a bare "not found".
	want := func(frag string) {
		t.Helper()
		if !strings.Contains(body, frag) {
			t.Errorf("missing %q in body:\n%s", frag, body)
		}
	}

	want("webdriver=undefined")
	want("plugins_len=3")
	want("langs_len=2")
	want("chrome_app=object")
	want("chrome_csi_type=function")
	want("chrome_loadTimes_type=function")
	want("chrome_runtime=object")
	want("notif_perm=default")
	want("devmem_typeof=number")
	want("devmem_gte1=true")
	want("hwconc_typeof=number")
	want("hwconc_gte1=true")
	want("canvas_same_doc_stable=true")
	want("canvas_len_nonzero=true")
	// geo_perm is set from an async promise; check permissively.
	if !strings.Contains(body, "geo_perm=prompt") && !strings.Contains(body, "async\">geo_perm=") {
		t.Errorf("permissions.query shim did not surface geo_perm=prompt; body:\n%s", body)
	}
}

// TestChromiumCanvasNoiseVariesAcrossSessions verifies that the canvas
// fingerprint noise is seeded fresh per fetch — the same page rendered
// in two separate Chromium sessions produces different toDataURL hashes.
// This is the "a population of automation instances looks diverse"
// property that DataDome-style canvas fingerprinting relies on.
func TestChromiumCanvasNoiseVariesAcrossSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chromium test in -short mode")
	}
	if !chromiumAvailable() {
		t.Skip("chrome/chromium not found on this host")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html><body>
				<div id="result"></div>
				<script>
					const c = document.createElement('canvas');
					c.width = 300; c.height = 60;
					const ctx = c.getContext('2d');
					ctx.fillStyle = '#f60';
					ctx.fillRect(0, 0, 300, 60);
					ctx.fillStyle = '#069';
					ctx.font = '18px Arial';
					ctx.fillText('variance probe', 10, 30);
					document.getElementById('result').textContent = c.toDataURL();
				</script>
			</body></html>
		`))
	}))
	defer srv.Close()

	cfg := DefaultChromiumConfig()
	cfg.Stealth = true

	fetchHash := func() string {
		e := NewChromium(cfg)
		defer e.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := e.Fetch(ctx, Request{URL: srv.URL})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		// Extract the data URL between the div tags.
		i := strings.Index(string(res.Body), `id="result">`)
		if i < 0 {
			t.Fatalf("result div missing from body: %s", res.Body)
		}
		rest := string(res.Body)[i+len(`id="result">`):]
		j := strings.Index(rest, "</div>")
		if j < 0 {
			t.Fatalf("result div not closed: %s", res.Body)
		}
		return rest[:j]
	}

	a := fetchHash()
	b := fetchHash()

	if len(a) < 100 || len(b) < 100 {
		t.Fatalf("canvas output suspiciously short: a=%d b=%d", len(a), len(b))
	}
	if a == b {
		t.Errorf("canvas PNG bytes identical across separate Chromium sessions — fingerprint noise not taking effect on the final output")
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
