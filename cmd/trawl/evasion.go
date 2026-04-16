package main

import (
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/spf13/cobra"
)

// evasionOpts holds the four Tier 1 + Tier 2 evasion flags shared by
// scrape / batch / crawl / map. Each command embeds one of these in
// its own *Opts struct and calls registerEvasionFlags in the cobra
// constructor; the run* function calls .toJob() to translate into the
// job-package representation before handing off to job.Run /
// job.RunOne.
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

// toJob translates a cobra-bound evasionOpts into the job-package
// EvasionOpts used by Run/RunOne/RunMap.
func (o evasionOpts) toJob() job.EvasionOpts {
	return job.EvasionOpts{
		BrowserLike:       o.browserLike,
		UserAgentStrategy: o.userAgentStrategy,
		Stealth:           o.stealth,
		NoJitter:          o.noJitter,
		TLSMatch:          o.tlsMatch,
	}
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
