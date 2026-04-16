package job

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/pdf"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/rs/zerolog/log"
)

// EvasionOpts captures the four Tier 1 + Tier 2 evasion knobs that
// scrape / batch / crawl / map share. The cmd/trawl flag-binding
// structs translate into this. ApplyEvasion pushes the values into
// the engine + politeness + chromium configs before they're used to
// build the live components.
//
// All-zero is the safe default: declared UA, no jitter, no stealth,
// no TLS forging — byte-identical behavior to a pre-evasion run.
type EvasionOpts struct {
	BrowserLike       bool
	UserAgentStrategy string // raw flag value, parsed by engine.ParseUAStrategy
	Stealth           bool
	NoJitter          bool
	TLSMatch          string // empty (off) | "chrome"
}

// Active reports whether any evasion knob is non-zero. Used by the
// LogEvasion gate so quiet runs stay quiet.
func (o EvasionOpts) Active() bool {
	return o.BrowserLike || o.Stealth || o.UserAgentStrategy != "" || o.TLSMatch != ""
}

// ApplyEvasion mutates the three configs to apply the requested
// evasion posture. Returns an error only on a malformed --user-agent
// or --tls-match value, since silently degrading would surprise the
// operator.
//
// Order of operations: parse and validate inputs first (fail fast),
// then mutate — never leave a config half-applied if validation fails.
func ApplyEvasion(
	httpCfg *engine.HTTPConfig,
	gateCfg *politeness.Config,
	chromiumCfg *engine.ChromiumConfig,
	opts EvasionOpts,
) error {
	rawStrategy := opts.UserAgentStrategy
	if rawStrategy == "" && opts.BrowserLike {
		// --browser-like without an explicit --user-agent: rotate
		// through the chromium-family pool. The doc's §9 lean.
		rawStrategy = "rotating"
	}
	strategy, fixedUA, err := engine.ParseUAStrategy(rawStrategy)
	if err != nil {
		return fmt.Errorf("--user-agent: %w", err)
	}
	if err := engine.ValidateTLSPreset(opts.TLSMatch); err != nil {
		return fmt.Errorf("--tls-match: %w", err)
	}

	httpCfg.TLSMatch = opts.TLSMatch
	httpCfg.UserAgentStrategy = strategy
	if strategy == engine.UAStrategyFixed {
		httpCfg.UserAgent = fixedUA
	}
	httpCfg.BrowserLikeHeaders = opts.BrowserLike
	if opts.BrowserLike {
		jar, jerr := cookiejar.New(nil)
		if jerr != nil {
			log.Warn().Err(jerr).Msg("cookie jar init failed; --browser-like will run without cookies")
		} else {
			httpCfg.CookieJar = jar
		}
	}
	if opts.BrowserLike && !opts.NoJitter {
		gateCfg.JitterFraction = 0.2
	}
	chromiumCfg.Stealth = opts.Stealth
	chromiumCfg.BrowserLike = opts.BrowserLike
	if opts.BrowserLike && chromiumCfg.UserAgent == "" {
		// Chromium can't rotate per-host the way the http engine can —
		// the browser allocator is one process per engine instance, and
		// changing UA mid-session would itself be a tell. Pick ONE
		// Chrome UA up front and pin chromium for the run lifetime.
		chromiumCfg.UserAgent = engine.NewUAPicker().Pick("chromium").UA
	}
	return nil
}

// LogEvasion emits one info line at job start when any evasion is
// active. Quiet by default — operators see it only when they opted in.
func LogEvasion(opts EvasionOpts) {
	if !opts.Active() {
		return
	}
	log.Info().
		Bool("browser_like", opts.BrowserLike).
		Bool("stealth", opts.Stealth).
		Str("user_agent_strategy", evasionStrategyForLog(opts)).
		Bool("no_jitter", opts.NoJitter).
		Str("tls_match", opts.TLSMatch).
		Msg("evasion enabled")
}

func evasionStrategyForLog(opts EvasionOpts) string {
	if opts.UserAgentStrategy != "" {
		return opts.UserAgentStrategy
	}
	if opts.BrowserLike {
		return "rotating"
	}
	return "declared"
}

// ProxyOpts captures the proxy + rotation knobs shared across
// commands. Apply pushes the values into engine configs and returns
// the rotator + parsed status codes for the router.
type ProxyOpts struct {
	URL            string // --proxy single gateway
	File           string // --proxy-file pool file
	RotateOnStatus string // --rotate-on-status comma-separated codes
	RotateRetries  int    // --rotate-retries cap per tier
}

// ProxyResult holds the outputs ApplyProxy produces that callers wire
// into the router (rotation config). Returned even when no proxy is
// configured, in which case Rotator is nil and RotateCodes is empty.
type ProxyResult struct {
	Rotator     ProxyRotator
	RotateCodes []int
	RotateMax   int
}

// ProxyRotator matches router.ProxyRotator without dragging the router
// import into the options layer. Implemented by domainStickyRotator.
type ProxyRotator interface {
	Rotate(domain string) bool
}

// ApplyProxy pushes the parsed proxy config into the engine configs
// and returns the rotation state. Called after ApplyEvasion and
// before any engine is built.
func ApplyProxy(
	httpCfg *engine.HTTPConfig,
	chromiumCfg *engine.ChromiumConfig,
	opts ProxyOpts,
) (ProxyResult, error) {
	var pr ProxyResult
	pr.RotateMax = opts.RotateRetries

	if opts.RotateOnStatus != "" {
		codes, err := parseStatusCodes(opts.RotateOnStatus)
		if err != nil {
			return pr, fmt.Errorf("--rotate-on-status: %w", err)
		}
		pr.RotateCodes = codes
	}

	if opts.URL == "" && opts.File == "" {
		return pr, nil
	}
	if opts.URL != "" && opts.File != "" {
		return pr, fmt.Errorf("cannot use both --proxy and --proxy-file")
	}

	if opts.URL != "" {
		u, err := url.Parse(opts.URL)
		if err != nil {
			return pr, fmt.Errorf("--proxy: invalid URL: %w", err)
		}
		httpCfg.ProxyFunc = func(_ *http.Request) (*url.URL, error) {
			return u, nil
		}
		chromiumCfg.ProxyURL = opts.URL
		httpCfg.ProxyEnabled = true
		// Single proxy — no rotation possible. pr.Rotator stays nil.
		return pr, nil
	}

	pool, err := loadProxyPool(opts.File)
	if err != nil {
		return pr, fmt.Errorf("--proxy-file: %w", err)
	}
	if len(pool) == 0 {
		return pr, fmt.Errorf("--proxy-file: no valid proxy URLs found in %s", opts.File)
	}

	rot := &domainStickyRotator{pool: pool, assigned: make(map[string]int)}
	httpCfg.ProxyFunc = rot.proxyForRequest
	// Chromium gets the first proxy in the pool — per-domain rotation
	// isn't possible without recycling the browser allocator.
	chromiumCfg.ProxyURL = pool[0].String()
	httpCfg.ProxyEnabled = true
	pr.Rotator = rot
	log.Info().Int("pool_size", len(pool)).Str("chromium_proxy", pool[0].Host).Msg("proxy pool loaded")
	return pr, nil
}

// LogProxy emits one info line at job start when a proxy is set.
func LogProxy(opts ProxyOpts) {
	if opts.URL != "" {
		u, _ := url.Parse(opts.URL)
		host := opts.URL
		if u != nil {
			host = u.Host
		}
		log.Info().Str("proxy", host).Msg("proxy enabled")
	} else if opts.File != "" {
		log.Info().Str("proxy_file", opts.File).Msg("proxy pool enabled")
	}
}

// PDFOpts captures the PDF-engine knobs that tune tier behavior on
// application/pdf responses. Zero value means "Tier 1+2 only, no
// OCR" — the safe default that works wherever poppler-utils is
// installed.
type PDFOpts struct {
	OCR         bool
	OCRLang     string
	MaxPages    int
}

// Validate fails fast when --ocr was set but the OCR toolchain is
// missing. Hard-failing at command start beats discovering it 100
// URLs into a batch. A missing pdftotext is NOT a hard fail here —
// trawl stays useful for HTML even on machines without poppler-utils,
// and PDFs soft-fail per-row with a clear install hint.
func (p PDFOpts) Validate() error {
	if !p.OCR {
		return nil
	}
	missing := pdf.OCRMissingBinaries()
	if len(missing) == 0 {
		return nil
	}
	hint := pdf.InstallHint(missing[0])
	return fmt.Errorf("--ocr requires %s to be installed — %s",
		strings.Join(missing, " and "), hint)
}

// Build returns the internal/pdf.Opts derived from these knobs.
// Returns the zero value when --ocr isn't set so downstream code
// doesn't need to branch.
func (p PDFOpts) Build() pdf.Opts {
	return pdf.Opts{
		UseOCR:   p.OCR,
		OCRLang:  p.OCRLang,
		MaxPages: p.MaxPages,
	}
}

// Log emits a single info-level line when non-default PDF settings
// are active. Mirrors LogEvasion / LogProxy.
func (p PDFOpts) Log() {
	if !p.OCR {
		return
	}
	log.Info().
		Bool("ocr", true).
		Str("ocr_lang", p.OCRLang).
		Int("max_pages", p.MaxPages).
		Msg("PDF OCR enabled (Tier 3)")
}
