package main

import (
	"fmt"
	"strings"

	"github.com/jeffdhooton/trawl/internal/engine"
	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/jeffdhooton/trawl/internal/validity"
)

// buildRouter constructs a router from tier names. Supported names:
//
//	http       — always available
//	chromium   — requires a local Chrome/Chromium binary on PATH
//
// If forceTier is non-empty, only that engine is used (no escalation).
// chromiumCfg seeds the chromium engine when that tier is in the
// list; the http UA from httpCfg is copied in unless chromiumCfg
// already overrides it, so existing call sites that don't care about
// chromium-specific settings keep working with a zero ChromiumConfig.
func buildRouter(tiers []string, forceTier string, httpCfg engine.HTTPConfig, chromiumCfg engine.ChromiumConfig) (*router.Router, error) {
	var names []string
	if forceTier != "" {
		names = []string{forceTier}
	} else {
		names = tiers
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no tiers configured")
	}

	var engines []engine.Engine
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch name {
		case "http":
			engines = append(engines, engine.NewHTTP(httpCfg))
		case "chromium":
			ccfg := chromiumCfg
			if ccfg.NavigationTimeout == 0 {
				// Caller passed a zero ChromiumConfig — fall back to
				// the engine defaults so we don't construct a chromium
				// with no timeout.
				defaults := engine.DefaultChromiumConfig()
				if ccfg.NavigationTimeout == 0 {
					ccfg.NavigationTimeout = defaults.NavigationTimeout
				}
				if ccfg.WaitAfterLoad == 0 {
					ccfg.WaitAfterLoad = defaults.WaitAfterLoad
				}
				if !ccfg.Headless {
					ccfg.Headless = defaults.Headless
				}
			}
			if ccfg.UserAgent == "" {
				ccfg.UserAgent = httpCfg.UserAgent
			}
			engines = append(engines, engine.NewChromium(ccfg))
		default:
			return nil, fmt.Errorf("unknown tier %q (supported: http, chromium)", name)
		}
	}

	return router.New(engines, validity.NewChecker(validity.Default()))
}

// parseTierList splits a comma-separated tier string like "http,chromium".
func parseTierList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
