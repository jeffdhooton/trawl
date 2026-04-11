package main

import (
	"fmt"
	"net/http/cookiejar"

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/politeness"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// evasionOpts holds the four Tier 1 + Tier 2 evasion flags shared by
// scrape / batch / crawl / map. Each command embeds one of these in
// its own *Opts struct and calls registerEvasionFlags in the cobra
// constructor; runX then calls applyEvasion to push the settings
// into the http / politeness / chromium configs before they're used.
//
// Defaults are all zero so commands that ignore the helper still
// behave exactly as they did before evasion shipped.
type evasionOpts struct {
	browserLike       bool
	userAgentStrategy string // raw flag value, parsed by engine.ParseUAStrategy
	stealth           bool
	noJitter          bool
	tlsMatch          string // Tier 3: empty (off) | "chrome"
}

// registerEvasionFlags wires the four flags onto a cobra command.
// Long descriptions intentionally point at docs/EVASION.md so the
// CLI help stays self-contained but doesn't try to re-explain the
// principled stance behind each tier.
func registerEvasionFlags(cmd *cobra.Command, e *evasionOpts) {
	cmd.Flags().BoolVar(&e.browserLike, "browser-like", false,
		"enable Tier 1 browser mimicry: rotating UA, full Chrome header set, "+
			"in-memory cookie jar, jittered timing (see docs/EVASION.md §5.1)")
	cmd.Flags().StringVar(&e.userAgentStrategy, "user-agent", "",
		`User-Agent strategy: "declared" (default), "rotating", or "fixed:<string>". `+
			`When --browser-like is set without this flag, "rotating" is implied.`)
	cmd.Flags().BoolVar(&e.stealth, "stealth", false,
		"Tier 2: inject chromium stealth init script (navigator.webdriver, plugins, "+
			"WebGL fingerprints) before navigation (see docs/EVASION.md §5.2)")
	cmd.Flags().BoolVar(&e.noJitter, "no-jitter", false,
		"disable timing jitter even when --browser-like is set (deterministic pacing)")
	cmd.Flags().StringVar(&e.tlsMatch, "tls-match", "",
		`Tier 3: forge ClientHello to match a real browser. Currently only "chrome" `+
			`is supported. Affects only the http engine — chromium uses its own real `+
			`TLS stack. See docs/EVASION.md §5.3.`)
}

// applyEvasion pushes the parsed flag values into the engine + gate
// + chromium configs. Called from runScrape / runBatch / runCrawl /
// runMap after default configs are constructed and before any engine
// or gate is built. The order matters because applyEvasion may
// install a CookieJar onto httpCfg, which NewHTTP only honors if it's
// set before construction.
//
// Returns an error only on a malformed --user-agent value, since
// silently degrading to "declared" would surprise the operator.
func applyEvasion(
	httpCfg *engine.HTTPConfig,
	gateCfg *politeness.Config,
	chromiumCfg *engine.ChromiumConfig,
	opts evasionOpts,
) error {
	rawStrategy := opts.userAgentStrategy
	if rawStrategy == "" && opts.browserLike {
		// --browser-like without an explicit --user-agent: rotate
		// through the chromium-family pool. The doc's §9 lean.
		rawStrategy = "rotating"
	}
	strategy, fixedUA, err := engine.ParseUAStrategy(rawStrategy)
	if err != nil {
		return fmt.Errorf("--user-agent: %w", err)
	}
	// Validate the TLS preset BEFORE we touch any config fields so a
	// typo fails the command instead of silently degrading to Go's
	// stdlib fingerprint (which would defeat the whole opt-in).
	if err := engine.ValidateTLSPreset(opts.tlsMatch); err != nil {
		return fmt.Errorf("--tls-match: %w", err)
	}
	httpCfg.TLSMatch = opts.tlsMatch
	httpCfg.UserAgentStrategy = strategy
	if strategy == engine.UAStrategyFixed {
		// ParseUAStrategy returns the unwrapped string in fixedUA;
		// the engine reads it from cfg.UserAgent so the existing
		// declared/rotating paths don't need a separate field.
		httpCfg.UserAgent = fixedUA
	}
	httpCfg.BrowserLikeHeaders = opts.browserLike
	if opts.browserLike {
		// Per-job in-memory cookie jar. Lifetime is the command run;
		// no $TRAWL_HOME persistence per the §9 design call. Errors
		// from cookiejar.New are impossible with a nil options arg
		// (the only failure mode is an invalid PublicSuffixList) but
		// we still log defensively.
		jar, jerr := cookiejar.New(nil)
		if jerr != nil {
			log.Warn().Err(jerr).Msg("cookie jar init failed; --browser-like will run without cookies")
		} else {
			httpCfg.CookieJar = jar
		}
	}
	if opts.browserLike && !opts.noJitter {
		// ±20% on top of the rate limiter. Empirical tuning is a
		// follow-up; this matches docs/EVASION.md §5.1.
		gateCfg.JitterFraction = 0.2
	}
	chromiumCfg.Stealth = opts.stealth
	chromiumCfg.BrowserLike = opts.browserLike
	// Chromium can't rotate per-host the way the http engine can —
	// the browser allocator is one process per engine instance, and
	// changing UA mid-session would itself be a tell. So when the
	// operator opted into Tier 1 we pick ONE Chrome UA up front and
	// pin chromium to it for the lifetime of the run. Without this,
	// chromium kept emitting `trawl/<ver>` and got 403'd by the same
	// hosts that the http engine sailed past in browser-like mode.
	// Picked deterministically via UAPicker so two runs with the
	// same host get the same chromium identity.
	if opts.browserLike && chromiumCfg.UserAgent == "" {
		chromiumCfg.UserAgent = engine.NewUAPicker().Pick("chromium").UA
	}
	return nil
}

// logEvasion writes a single info-level line at job start when any
// evasion is active. Operators see at a glance what they opted into;
// post-hoc audits cross-reference this against metadata.evasion in
// the JSONL output.
func logEvasion(opts evasionOpts) {
	if !opts.browserLike && !opts.stealth && opts.userAgentStrategy == "" && opts.tlsMatch == "" {
		return
	}
	log.Info().
		Bool("browser_like", opts.browserLike).
		Bool("stealth", opts.stealth).
		Str("user_agent_strategy", evasionStrategyForLog(opts)).
		Bool("no_jitter", opts.noJitter).
		Str("tls_match", opts.tlsMatch).
		Msg("evasion enabled")
}

func evasionStrategyForLog(opts evasionOpts) string {
	if opts.userAgentStrategy != "" {
		return opts.userAgentStrategy
	}
	if opts.browserLike {
		return "rotating"
	}
	return "declared"
}
