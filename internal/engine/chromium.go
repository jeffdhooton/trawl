package engine

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

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

	// Allocator is NOT tied to `parent` — we want it to outlive individual
	// Fetch calls so the browser is reused across requests.
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	c.allocCtx = ctx
	c.allocCancel = cancel
	_ = parent // parent reserved for future cancellation wiring
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

	actions := []chromedp.Action{
		chromedp.Navigate(req.URL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	}
	if c.cfg.WaitAfterLoad > 0 {
		actions = append(actions, chromedp.Sleep(c.cfg.WaitAfterLoad))
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

	return &Result{
		URL:         req.URL,
		FinalURL:    finalURL,
		StatusCode:  statusCode,
		Header:      header,
		ContentType: ct,
		Body:        []byte(html),
		Duration:    time.Since(start),
		Screenshot:  screenshot,
	}, nil
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

