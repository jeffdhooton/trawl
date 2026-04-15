package engine

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog/log"
)

// stealthJS is the script injected before navigation when
// ChromiumConfig.Stealth is true. See internal/engine/stealth.js for
// the patches and EVASION.md §5.2 for what they actually defend
// against (and what they don't).
//
//go:embed stealth.js
var stealthJS string

// ChromiumConfig tunes the headless Chromium engine.
type ChromiumConfig struct {
	// UserAgent overrides the browser's default UA.
	UserAgent string
	// ExecPath overrides chromedp's Chrome auto-detection. Empty = auto.
	ExecPath string
	// NavigationTimeout caps the total time from navigate → stable DOM.
	NavigationTimeout time.Duration
	// WaitAfterLoad is a grace period after the load event during which
	// JS-rendered content can settle. Zero disables the wait.
	WaitAfterLoad time.Duration
	// Headless runs chromium without a UI. Default true.
	Headless bool
	// Stealth, when true, injects the stealth init script before every
	// navigation. The script patches navigator.webdriver, plugins,
	// languages, window.chrome, WebGL strings, and the permissions
	// shim — see internal/engine/stealth.js. Tier 2 from EVASION.md.
	// Default false; opt-in via --stealth.
	Stealth bool
	// ProxyURL, when non-empty, routes all chromium requests through
	// this HTTP proxy. Only a single URL is supported (Chrome's
	// --proxy-server flag is process-wide). For rotating pools, the
	// cmd layer pins the first proxy in the pool here.
	ProxyURL string
	// BrowserLike is a record-only marker that propagates the
	// operator's --browser-like intent into chromium-served rows. The
	// chromium engine doesn't actually do anything with the flag —
	// chromium IS a real browser, so the Tier 1 header-injection
	// dance is a no-op here — but stamping it on the result keeps
	// metadata.evasion consistent for jobs that mix tiers.
	BrowserLike bool
}

// DefaultChromiumConfig returns chromium defaults geared toward scraping.
func DefaultChromiumConfig() ChromiumConfig {
	return ChromiumConfig{
		NavigationTimeout: 45 * time.Second,
		WaitAfterLoad:     500 * time.Millisecond,
		Headless:          true,
	}
}

// Chromium is the tier-3 engine — chromedp driving a real browser.
//
// P1 stage 1 opens a fresh browser allocator per Fetch for simplicity. That
// is slow (~1-2s/page overhead on top of the navigation itself) and is
// replaced by a warm pool in a later P1 stage. The Engine contract is
// unchanged.
type Chromium struct {
	cfg ChromiumConfig

	mu          sync.Mutex
	allocCtx    context.Context
	allocCancel context.CancelFunc
}

// NewChromium constructs a Chromium engine. The allocator (a shared browser
// process) is created lazily on the first Fetch, then reused for subsequent
// Fetches within this engine's lifetime.
func NewChromium(cfg ChromiumConfig) *Chromium {
	if cfg.NavigationTimeout == 0 {
		cfg.NavigationTimeout = 45 * time.Second
	}
	return &Chromium{cfg: cfg}
}

// Name implements Engine.
func (c *Chromium) Name() string { return "chromium" }

// Close tears down the browser allocator if one was started.
func (c *Chromium) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.allocCancel != nil {
		c.allocCancel()
		c.allocCancel = nil
		c.allocCtx = nil
	}
	return nil
}

func (c *Chromium) allocator(parent context.Context) (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.allocCtx != nil {
		return c.allocCtx, nil
	}

	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.DisableGPU,
		chromedp.NoDefaultBrowserCheck,
		chromedp.NoFirstRun,
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-features", "site-per-process,TranslateUI"),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-ipc-flooding-protection", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
	)
	if c.cfg.Headless {
		opts = append(opts, chromedp.Headless)
	}
	if c.cfg.ExecPath != "" {
		opts = append(opts, chromedp.ExecPath(c.cfg.ExecPath))
	}
	if c.cfg.UserAgent != "" {
		opts = append(opts, chromedp.UserAgent(c.cfg.UserAgent))
	}
	if c.cfg.ProxyURL != "" {
		opts = append(opts, chromedp.ProxyServer(c.cfg.ProxyURL))
	}

	// Allocator outlives individual Fetch calls so the browser is reused.
	// Parent must be context.Background() — NOT a timeout-scoped context —
	// because chromedp derives the allocator's ctx from it, and cancelling
	// that parent would kill the stored allocator.
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)

	// Verify the browser actually starts by creating and immediately
	// closing a throwaway context. The 30s probe timeout is scoped to the
	// probe tab, not the allocator, so it can fire without poisoning the
	// long-lived allocator context.
	probeCtx, probeCancel := chromedp.NewContext(ctx)
	probeTimeout, probeTimeoutCancel := context.WithTimeout(probeCtx, 30*time.Second)
	if err := chromedp.Run(probeTimeout); err != nil {
		probeTimeoutCancel()
		probeCancel()
		cancel()
		return nil, fmt.Errorf("chromium launch failed (is Chrome installed?): %w", err)
	}
	probeTimeoutCancel()
	probeCancel()

	c.allocCtx = ctx
	c.allocCancel = cancel
	_ = parent
	return ctx, nil
}

// Fetch implements Engine.
func (c *Chromium) Fetch(ctx context.Context, req Request) (*Result, error) {
	alloc, err := c.allocator(ctx)
	if err != nil {
		return nil, fmt.Errorf("chromium allocator: %w", err)
	}

	// One browser context (tab) per fetch. Cheap. chromedp requires that its
	// own context type survive all the way to Run, so we cannot wrap it in
	// context.WithCancel — to honor the caller's ctx we watch it in a
	// goroutine and cancel the browser context directly.
	browserCtx, browserCancel := chromedp.NewContext(alloc)
	defer browserCancel()

	timeoutCtx, timeoutCancel := context.WithTimeout(browserCtx, c.cfg.NavigationTimeout)
	defer timeoutCancel()

	callerDone := make(chan struct{})
	defer close(callerDone)
	go func() {
		select {
		case <-ctx.Done():
			browserCancel()
		case <-callerDone:
		}
	}()

	start := time.Now()

	var (
		html       string
		finalURL   string
		status     int64
		respHeader map[string]any
		screenshot []byte
	)

	// Capture the top-level navigation response so we can report a real
	// status code instead of always 200.
	capture := makeResponseCapture(browserCtx, req.URL, &status, &respHeader)
	defer capture.stop()

	actions := []chromedp.Action{}
	if c.cfg.Stealth {
		// Inject the stealth patches before navigation. AddScript-
		// ToEvaluateOnNewDocument is the chromium-native way to run
		// JS on every new document context, including the main
		// frame, before any page script. Re-arming on every Fetch
		// is required because chromedp.NewContext gives us a fresh
		// browser context (tab) per fetch.
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(stealthJS).Do(ctx)
			return err
		}))
	}
	actions = append(actions,
		chromedp.Navigate(req.URL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	)
	if c.cfg.WaitAfterLoad > 0 {
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			log.Debug().Str("url", req.URL).Str("wait", c.cfg.WaitAfterLoad.String()).Msg("chromium: waiting after page load")
			return nil
		}))
		actions = append(actions, chromedp.Sleep(c.cfg.WaitAfterLoad))
	}
	// User-supplied interactive actions (click, scroll, wait, etc.) run
	// after the page has loaded and settled, before we capture the final
	// DOM state. The HTTP engine silently ignores these.
	if len(req.Actions) > 0 {
		log.Debug().Str("url", req.URL).Int("count", len(req.Actions)).Msg("chromium: running pre-scrape actions")
		actions = append(actions, req.Actions...)
	}
	actions = append(actions,
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
		chromedp.Location(&finalURL),
	)
	// Screenshot must come AFTER OuterHTML so we don't miss any post-load
	// DOM mutations. chromedp.FullScreenshot hardcodes JPEG via the quality
	// parameter, so we call page.CaptureScreenshot directly to get PNG with
	// captureBeyondViewport=true (full scrollable page).
	if req.WantScreenshot {
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			buf, err := page.CaptureScreenshot().
				WithCaptureBeyondViewport(true).
				WithFromSurface(true).
				WithFormat(page.CaptureScreenshotFormatPng).
				Do(ctx)
			if err != nil {
				return err
			}
			screenshot = buf
			return nil
		}))
	}

	if err := chromedp.Run(timeoutCtx, actions...); err != nil {
		return nil, fmt.Errorf("chromium fetch: %w", err)
	}

	statusCode := int(status)
	if statusCode == 0 {
		// Capture missed (redirect chain, subresource-only navigation, etc.)
		// Fall back to 200 since we did get HTML back.
		statusCode = 200
	}

	header := http.Header{}
	ct := ""
	if respHeader != nil {
		for k, v := range respHeader {
			if s, ok := v.(string); ok {
				header.Set(k, s)
				if ctHeader(k) {
					ct = s
				}
			}
		}
	}
	if ct == "" {
		ct = "text/html"
	}

	body := []byte(html)
	// Cap body size to prevent memory blowup on pages with huge inline
	// data (base64 images, embedded datasets). The HTTP engine applies
	// the same limit via io.LimitReader; chromium needs it post-hoc
	// because OuterHTML returns the entire DOM as a string.
	const maxChromiumBody = 20 << 20 // 20 MiB, matches HTTP default
	if len(body) > maxChromiumBody {
		log.Warn().
			Str("url", req.URL).
			Int("body_bytes", len(body)).
			Int("max_bytes", maxChromiumBody).
			Msg("chromium body truncated to size limit")
		body = body[:maxChromiumBody]
	}

	res := &Result{
		URL:         req.URL,
		FinalURL:    finalURL,
		StatusCode:  statusCode,
		Header:      header,
		ContentType: ct,
		Body:        body,
		Duration:    time.Since(start),
		Screenshot:  screenshot,
	}
	if c.cfg.Stealth || c.cfg.BrowserLike || c.cfg.ProxyURL != "" {
		res.Evasion = &EvasionInfo{
			BrowserLike: c.cfg.BrowserLike,
			Stealth:     c.cfg.Stealth,
			UserAgent:   c.cfg.UserAgent,
			Proxy:       c.cfg.ProxyURL != "",
		}
	}
	return res, nil
}

func ctHeader(k string) bool {
	return len(k) == 12 && (k == "Content-Type" || k == "content-type")
}

// --- response capture helpers -------------------------------------------

type responseCapture struct {
	stop func()
}

func makeResponseCapture(
	browserCtx context.Context,
	targetURL string,
	status *int64,
	header *map[string]any,
) responseCapture {
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	chromedp.ListenTarget(browserCtx, func(ev any) {
		select {
		case <-done:
			return
		default:
		}
		if r, ok := ev.(*network.EventResponseReceived); ok {
			// The first document-type response is the main navigation.
			if r.Type == network.ResourceTypeDocument {
				*status = int64(r.Response.Status)
				*header = r.Response.Headers
			}
		}
	})
	return responseCapture{stop: stop}
}

