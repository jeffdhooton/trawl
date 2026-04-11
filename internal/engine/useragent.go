package engine

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"
	"sync"
)

// UserAgentStrategy controls how the HTTP engine picks a User-Agent
// header for each request. The default ("declared") preserves trawl's
// historical polite identity; the others enable Tier 1 mimicry from
// docs/EVASION.md §5.1.
type UserAgentStrategy int

const (
	// UAStrategyDeclared sends `trawl/<version>` exactly as the engine
	// has always done. The default; never lies about what we are.
	UAStrategyDeclared UserAgentStrategy = iota
	// UAStrategyRotating picks one Chromium-family UA per host on first
	// contact and reuses it for the lifetime of the engine. Sticky-per-
	// host because humans don't change browsers mid-session — rotating
	// the UA on every request is itself a tell.
	UAStrategyRotating
	// UAStrategyFixed sends an operator-supplied string verbatim on
	// every request. The string is captured in HTTPConfig.UserAgent.
	UAStrategyFixed
)

// ParseUAStrategy reads a CLI value into a strategy + an optional
// fixed override string. Accepted forms:
//
//	""           → declared (the empty string is the same as omitted)
//	"declared"   → UAStrategyDeclared
//	"rotating"   → UAStrategyRotating
//	"fixed:<s>"  → UAStrategyFixed, override = <s>
//
// Anything else is an error so typos at the CLI fail loudly instead
// of silently degrading to "declared."
func ParseUAStrategy(raw string) (UserAgentStrategy, string, error) {
	s := strings.TrimSpace(raw)
	switch {
	case s == "" || s == "declared":
		return UAStrategyDeclared, "", nil
	case s == "rotating":
		return UAStrategyRotating, "", nil
	case strings.HasPrefix(s, "fixed:"):
		fixed := strings.TrimPrefix(s, "fixed:")
		if fixed == "" {
			return 0, "", fmt.Errorf("user-agent strategy %q: missing string after 'fixed:'", raw)
		}
		return UAStrategyFixed, fixed, nil
	default:
		return 0, "", fmt.Errorf("user-agent strategy %q: must be 'declared', 'rotating', or 'fixed:<string>'", raw)
	}
}

// ChromeUA bundles a User-Agent string with the matching client-hint
// headers a real Chromium browser would send alongside it. Bundling
// them keeps "browser-like headers" coherent — a request with a
// Windows UA but a "macOS" platform hint is a giveaway, so the picker
// always emits a self-consistent set.
type ChromeUA struct {
	UA           string
	SecCHUA      string
	SecCHUAPlat  string // "macOS" | "Windows" | "Linux"
	SecCHUAMob   string // "?0" desktop, "?1" mobile
}

// chromeUAPool is the rotation set for UAStrategyRotating. Every entry
// is a real Chromium-family browser from the recent stable channel as
// of late 2025 / early 2026. The pool is intentionally small — ~10
// entries — because rotating across many UAs creates a different tell
// (hosts that see "every visitor uses a different browser version"
// look bot-y on aggregate).
//
// All entries are Chromium because the matching Sec-CH-UA / Sec-CH-UA-
// Platform / Sec-CH-UA-Mobile headers are part of the browser-like
// profile, and Firefox / Safari don't send those headers at all —
// mixing them in would force inconsistent header sets.
//
// Refresh cadence: revisit when stable Chrome moves more than three
// majors past the highest version here. Stale UAs stand out.
var chromeUAPool = []ChromeUA{
	{
		UA:          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36",
		SecCHUA:     `"Not A(Brand";v="8", "Chromium";v="132", "Google Chrome";v="132"`,
		SecCHUAPlat: `"macOS"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		SecCHUA:     `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`,
		SecCHUAPlat: `"macOS"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36",
		SecCHUA:     `"Not A(Brand";v="8", "Chromium";v="132", "Google Chrome";v="132"`,
		SecCHUAPlat: `"Windows"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		SecCHUA:     `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`,
		SecCHUAPlat: `"Windows"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
		SecCHUA:     `"Chromium";v="134", "Not:A-Brand";v="24", "Google Chrome";v="134"`,
		SecCHUAPlat: `"Windows"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36",
		SecCHUA:     `"Not A(Brand";v="8", "Chromium";v="132", "Google Chrome";v="132"`,
		SecCHUAPlat: `"Linux"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		SecCHUA:     `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`,
		SecCHUAPlat: `"Linux"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
		SecCHUA:     `"Chromium";v="134", "Not:A-Brand";v="24", "Google Chrome";v="134"`,
		SecCHUAPlat: `"macOS"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36 Edg/132.0.0.0",
		SecCHUA:     `"Not A(Brand";v="8", "Chromium";v="132", "Microsoft Edge";v="132"`,
		SecCHUAPlat: `"Windows"`,
		SecCHUAMob:  "?0",
	},
	{
		UA:          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36 Edg/132.0.0.0",
		SecCHUA:     `"Not A(Brand";v="8", "Chromium";v="132", "Microsoft Edge";v="132"`,
		SecCHUAPlat: `"macOS"`,
		SecCHUAMob:  "?0",
	},
}

// UAPicker resolves a (host → ChromeUA) mapping with sticky-per-host
// semantics: the first call for a host picks a random pool entry, and
// every subsequent call for the same host returns that exact entry.
// Safe for concurrent use.
type UAPicker struct {
	mu     sync.Mutex
	byHost map[string]ChromeUA
}

// NewUAPicker constructs an empty picker. The caller is the HTTP
// engine; one picker is held per engine instance so the host→UA map
// lives for the engine's lifetime (which is the job's lifetime).
func NewUAPicker() *UAPicker {
	return &UAPicker{byHost: make(map[string]ChromeUA)}
}

// Pick returns the sticky ChromeUA for the given host, choosing a
// pool entry on first contact. The host string is the value of
// url.URL.Host (so port-suffixed hosts like "example.com:8080" get
// their own entry, which is fine — they almost never appear in
// practice).
func (p *UAPicker) Pick(host string) ChromeUA {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ua, ok := p.byHost[host]; ok {
		return ua
	}
	ua := pickInitialUA(host)
	p.byHost[host] = ua
	return ua
}

// pickInitialUA chooses a starting UA for a never-seen host. We seed
// the pool index with a hash of the host name plus a random nudge so
// (a) the same host gets the same UA across runs in the absence of
// concurrency races (mildly nice for cache friendliness) and (b)
// different hosts in the same job span the pool.
func pickInitialUA(host string) ChromeUA {
	h := fnv.New32a()
	_, _ = h.Write([]byte(host))
	idx := (int(h.Sum32()) + rand.IntN(len(chromeUAPool))) % len(chromeUAPool)
	if idx < 0 {
		idx = -idx
	}
	return chromeUAPool[idx]
}
