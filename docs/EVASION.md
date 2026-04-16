# Trawl — Anti-Detection / Evasion Playbook

**Status:** Tier 1 (browser mimicry), Tier 2 (chromium stealth),
and Tier 3 (Chrome TLS forgery + HTTP/2) are shipped. Tier 4 (proxy
rotation) is deferred to P2. See §5.1–§5.3 SHIPPED subsections.
**Audience:** future contributors deciding what to build (primary),
and operators who want to understand trawl's philosophy around
hostile-site scraping (secondary).
**Related:** `docs/SPEC.md` §2 and §13 originally put stealth in the
"rabbit hole, out of scope" bucket. This document is the principled
revision of that stance — stealth IS coming, but in a shape that
preserves trawl's identity as a polite-first tool rather than an
arms-race dependency.

---

## 1. Why this doc exists

Trawl shipped through 2026-04-11 as a polite-by-default scraper:
robots.txt respected, rate-limited per host, declared User-Agent,
no fingerprint manipulation. That identity held up beautifully on
the first real production workloads — the SEP corpus crawl (100%
reach, zero failures) and the validated HuggingFace schema — but
both of those targets are cooperative. They publish sitemaps, they
don't challenge bot traffic, and their robots.txt literally says
`Allow: /`.

Not every target will be cooperative. Some sites that are
legitimately useful to scrape — pricing dashboards, SaaS feature
comparisons, programmatic-SEO research targets — actively fight
back via fingerprint-based detection, even when the underlying data
is public and permitted by fair use. The question isn't whether
trawl will ever need to deal with that; it's whether the
eventual implementation will be principled or reactive.

This document locks in the principled shape BEFORE there's pressure
to ship something fast. The goal is to have the answer ready when
a hostile target shows up, not to discover we've accidentally
built an adversarial tool while solving one consumer's immediate
problem.

### Why SPEC §2 and §13 are being revised

The original SPEC called stealth a "rabbit hole" and put it
out of scope. That was correct for the design horizon it had:
"what's the minimum tool that handles cooperative sites?" Trawl
is now past that horizon. Leaving stealth undesigned while
building toward hostile targets is the real rabbit hole — it
guarantees an ad-hoc implementation that embeds the first
consumer's specific bypass into the tool permanently.

The revision is: **trawl will ship evasion features, but only
inside a tiered opt-in model with explicit guardrails and
decision rules**. Nothing in this doc changes trawl's default
behavior. Every evasion feature is behind a flag. Polite-by-
default still holds.

---

## 2. The detection landscape

Sites detect automated traffic at three layers. Each layer has its
own trade-offs; understanding them determines which evasion
techniques even matter.

### 2.1 TCP / TLS layer

- **TLS fingerprinting (JA3 / JA4).** The sequence of ciphers,
  extensions, and elliptic curves in a ClientHello is distinctive
  per TLS library. Go's `crypto/tls` has a recognizable signature;
  Chrome's is different; curl's is different again. A server that
  collects JA3 hashes can flag "this request came from Go's net/http"
  without ever reading the User-Agent header.
- **HTTP/2 settings frames.** The SETTINGS frame a client sends
  when opening an HTTP/2 connection includes values for
  INITIAL_WINDOW_SIZE, MAX_CONCURRENT_STREAMS, and others. Chrome
  sends a specific combination. Go sends a different one. Akamai
  and Cloudflare both use this as a signal.
- **TCP fingerprinting (p0f-style).** Much less common in web
  detection today, but theoretically possible — TTL, window size,
  options order. Generally ignored in practice.

**What an evader CAN do:** use `utls` (a fork of Go's `crypto/tls`
that lets you forge Chrome's JA3/JA4 fingerprint), rewrite HTTP/2
SETTINGS frame values to match Chrome.
**What an evader CANNOT easily do:** forge a perfectly-correct
Chrome fingerprint including every subtle quirk. Fingerprinting
libraries evolve; a forgery that was perfect last month may be
flagged this month.

### 2.2 HTTP layer

- **User-Agent analysis.** The simplest signal — `trawl/0.1 (+...)`
  is instantly flaggable. But rotating through a real-browser UA
  list is table stakes and easy.
- **Header set completeness.** Modern Chrome sends ~15 headers on
  a top-level navigation: `Accept`, `Accept-Language`,
  `Accept-Encoding`, `Sec-Fetch-Site`, `Sec-Fetch-Mode`,
  `Sec-Fetch-User`, `Sec-Fetch-Dest`, `Sec-CH-UA`, `Sec-CH-UA-Mobile`,
  `Sec-CH-UA-Platform`, `Upgrade-Insecure-Requests`, `DNT`,
  `Connection`, `Cookie`, and the request-line `Host`. Missing
  `Sec-Fetch-*` headers is a huge tell — only non-browsers omit them.
- **Header order and case.** Chrome emits headers in a specific
  order; Go's net/http uses Go's map iteration order (randomized).
  Canonicalization (`Accept-Language` vs `accept-language`) varies
  between libraries.
- **Cookie behavior.** Real browsers persist cookies across
  requests and replay them. A scraper that starts fresh on every
  request and never sends cookies looks like a bot.
- **Referer chains.** Top-level navigation direct from search
  omits Referer; internal navigation has a Referer from the
  previous page. A scraper that always has empty Referer for
  deep-linked pages is suspicious.

**What an evader CAN do:** send a full browser-like header set
with correct `Sec-Fetch-*` values, rotate User-Agents, persist
cookies per host, synthesize realistic Referer chains.
**What an evader CANNOT do without chromium:** pass header-order
and canonicalization checks perfectly from Go's stdlib. uTLS helps
at the TLS layer; for HTTP header ordering you'd need a custom
transport.

### 2.3 Application layer

- **JavaScript-based fingerprinting.** The bot-detection script
  probes `navigator.webdriver`, window dimensions, installed fonts,
  plugins, WebGL renderer strings, audio context fingerprints, timing
  of mouse/keyboard events, battery API, and a hundred other
  signals. Each of these can be queried via JS that runs in the
  page; a headless browser or non-browser HTTP client fails
  different subsets of these checks.
- **Behavioral patterns.** Rate of requests, depth-first-ness of
  crawl, time-on-page distributions, URL access order. A bot that
  fetches 100 pricing pages in 60 seconds with no intermediate
  browsing looks nothing like a human.
- **Challenge-response (CAPTCHA).** reCAPTCHA v3 scores, hCaptcha
  Turnstile, Cloudflare Challenge Page. These are the "you lost"
  state — once a site serves a challenge, the cheap evasion path
  is over and you're in "pay a CAPTCHA-solving service" territory.
- **Session / account requirements.** Some sites only serve real
  content to logged-in users. Scraping those requires either a
  real authenticated session (acceptable if the operator owns the
  account) or bypassing the auth wall (not acceptable).

**What an evader CAN do with chromium:** use `chromedp-undetected`
patches, execute realistic mouse/scroll events, respect pacing
budgets, maintain long-lived authenticated sessions.
**What an evader CANNOT do:** pass modern challenge-response
systems reliably without human or ML-solver assistance.

---

## 3. The evasion landscape

The techniques, ranked from cheapest-and-most-honest to
most-invasive-and-most-problematic:

| Tier | Technique | Build cost | Maintain cost | Legitimacy | Efficacy vs basic detection | Efficacy vs sophisticated detection |
|------|-----------|------------|---------------|------------|-----------------------------|--------------------------------------|
| 1 | Realistic User-Agent rotation | trivial | low | high | high | low |
| 1 | Full browser-like header set | low | low | high | high | medium |
| 1 | Cookie persistence per host | low | low | high | medium | high |
| 1 | Jittered request timing | trivial | low | high | medium | medium |
| 1 | Referer chain synthesis | medium | medium | medium | medium | medium |
| 2 | `chromedp-undetected` (chromium patches for `navigator.webdriver`, etc.) | low (upstream) | high (upstream arms race) | medium | low (already assumes chromium) | high |
| 2 | Realistic mouse/scroll events in chromium | low | low | medium | low | medium |
| 3 | `utls` TLS fingerprint forgery | medium | high (fingerprints rotate) | low-medium | low (TLS alone isn't the issue) | high (if sophisticated detection is purely JA3-based) |
| 3 | HTTP/2 SETTINGS frame matching | high | high | low | low | medium |
| 4 | Proxy rotation (datacenter) | medium | medium | medium | medium | low (datacenter IPs are flagged) |
| 4 | Proxy rotation (residential) | high ($) | high ($) | low | high | high |
| 5 | CAPTCHA-solving services | low (SDK) | medium ($) | low | — | — |
| 5 | ML-based human-interaction simulation | high | high | low | — | — |
| — | Session theft / credential-based bypass | — | — | none | — | — |

**Legitimacy** is a soft axis: how defensible is the technique if
you had to explain it in court or in a public blog post? Tier 1 is
indistinguishable from a polite browser. Tier 5 techniques are hard
to justify as anything other than adversarial.

**Efficacy columns** assume the detection system is either "basic"
(User-Agent + robots.txt matching only) or "sophisticated"
(JA3 + full fingerprinting + behavioral analysis + Cloudflare-grade
challenge system). No tier alone beats sophisticated detection;
beating sophisticated detection requires stacking tiers AND
accepting high false-positive / flap rates.

---

## 4. Trawl's principled stance

Trawl will ship evasion capabilities in **four tiers**, each behind
an **explicit opt-in flag**. The defaults never change — `trawl
scrape <url>` in 2027 will behave identically to `trawl scrape <url>`
in 2026-04-11. Evasion is always something the operator asks for.

The tiers map roughly to the landscape table above:

### Tier 1 — Honest browser mimicry (the identity-preserving tier)

Ship by default-as-an-option, not default-on. A flag like
`--browser-like` enables the full Chrome header set, rotates User-
Agents from a realistic list, persists cookies per host, and adds
jittered timing. None of this is fingerprint forgery — it's just
"don't be obviously a non-browser when you don't need to be."

Everything at this tier is defensible as "we wanted our scraper
to look like a typical visitor," which is what a polite crawl
genuinely does. Tier 1 is the default path for "I need this to
work against a site that flags `User-Agent: trawl/*`."

### Tier 2 — Chromium stealth patches

When chromium is serving the fetch, apply `chromedp-undetected`-
style patches: `navigator.webdriver = undefined`, fake plugin list,
realistic WebGL strings, basic mouse/scroll events before
extraction. Behind a flag like `--stealth`.

This tier is specifically NOT about beating sophisticated
detection — those systems have counter-patches for every
published stealth library, and the arms race is open-ended. It's
about passing the trivial "is this a headless browser?" check
that many sites still use.

### Tier 3 — TLS / HTTP/2 fingerprint forgery

Behind a flag like `--tls-match chrome`, replace Go's default TLS
stack with `utls` and adjust HTTP/2 SETTINGS frames to match Chrome's
signature. This is the heaviest Tier that still preserves
"honest browser mimicry" framing — we're saying "we want our TLS
to look like Chrome's TLS" which is defensible when your crawl
targets fingerprint-on-the-TLS-layer systems.

This tier is the one with the highest maintenance cost because
fingerprints rotate. A `utls` dependency that's six months stale
is no better than Go's stdlib.

### Tier 4 — Proxy rotation

Already tracked as a deferred P2 feature in `docs/PROXIES.md`.
Evasion and proxy rotation overlap significantly — a properly
rotating residential proxy pool solves many of the same problems
that Tier 3 is chasing. Build this in coordination with `docs/PROXIES.md`,
not separately.

### Tier 5 — CAPTCHA solving, ML-based evasion, session bypass

**Not in scope. See §6 for the explicit refusals.**

---

## 5. What trawl will ship, and when

### 5.1 Tier 1 — Ship when a first consumer asks

A future PR adds the following flags and `HTTPConfig` options:

- `--browser-like` on scrape/batch/crawl — enables the Tier 1
  behaviors collectively.
- `HTTPConfig.UserAgent` becomes `HTTPConfig.UserAgentStrategy`
  with values `declared` (current default, `trawl/<ver>`),
  `rotating` (picks from a small list of recent real browser UAs),
  or `fixed:<string>` (override).
- Cookie jar persisted per host inside the HTTP engine.
- Header-set override table with the full Chrome 13x `Sec-Fetch-*`
  block.
- Jittered per-host delay of ±20% on top of the existing rate
  limit.

**Decision rule (when to build):** a trawl consumer reports that
their target site returns 403/429 specifically on `User-Agent:
trawl/*` and serves real content on `User-Agent: Mozilla/5.0 ...`.
Ship Tier 1 for them, not speculatively.

#### SHIPPED 2026-04-11

Built speculatively (without a consumer ask) on the principle
that the next hostile target should hit a tool that's already
ready. The decision rule above is therefore retroactively waived
for Tier 1; the rule still gates Tiers 3+.

What actually shipped, deviations from the design above:

- `--browser-like`, `--user-agent <strategy>`, `--no-jitter` flags
  on scrape, batch, crawl, and map. Consistent across all four
  commands via the shared `cmd/trawl/evasion.go` helper.
- `HTTPConfig.UserAgentStrategy` enum with declared / rotating /
  fixed values, parsed by `engine.ParseUAStrategy`. The picker
  itself lives in `internal/engine/useragent.go` with a
  ~10-entry pool of recent Chromium-family UAs.
- Cookie jar is **in-memory per job** (`net/http/cookiejar.New`),
  not persisted to disk — closed the §9 question in favor of
  the simpler option. The jar lives for the command's run and
  is gone on exit.
- UA rotation is **sticky per host** (the §9 lean): the first
  fetch for a host picks one UA, and that mapping is held for
  the engine's lifetime. The chromium-family pool excludes
  Firefox/Safari because the matching `Sec-CH-UA` headers don't
  apply to non-Chromium browsers and mixed entries would create
  inconsistent header blocks.
- Browser-like header set: `Accept`, `Accept-Language`,
  `Accept-Encoding`, `Sec-Fetch-{Site,Mode,User,Dest}`,
  `Sec-Ch-Ua{,-Mobile,-Platform}`, `Upgrade-Insecure-Requests`.
  When `--user-agent fixed:<s>` overrides the rotating picker,
  the helper falls back to a stable Chrome 132 hint set so the
  header block stays internally consistent.
- Jitter lives in `politeness.Gate.Acquire`. When
  `Config.JitterFraction > 0` (set to 0.2 by `--browser-like`)
  the acquire path adds up to ±frac × baseInterval of uniform
  random sleep on top of the rate-limiter token. The sleep is
  context-aware: a tight ctx deadline cuts through cleanly.
- `HostRule.Jitter` per-host override (`internal/politeness/
  hostrules.go`) — operators can pin individual hosts to a
  different jitter than the gate default via the `--politeness`
  YAML.
- Caller-provided `req.ExtraHeaders` always win over the auto-set
  Chrome block. The HTTP engine `Header.Del`s before re-adding so
  the override is total, not additive.
- `metadata.evasion` JSON shape on every record:
  `{browser_like, stealth, user_agent, jitter_ms}`. Engine
  populates BrowserLike/Stealth/UserAgent on `engine.Result`;
  the cmd-layer combines that with `Acquire`'s jitterMS in
  `cmd/trawl/scrape.go:stampEvasion`. The field is omitempty
  pointer so default-mode records are byte-identical to the
  pre-evasion baseline.
- `Result.Evasion` (`internal/engine/engine.go`) is the engine-
  side slice; output package never imports engine, so the field
  is mirrored as `output.EvasionStats` and copied at record-build
  time.

What was deferred from this PR:

- **Referer chain synthesis** — middling efficacy in §3, real
  state-tracking complexity. Wait for a consumer hitting a
  Referer-checking target.
- **Soft-block telemetry** (200-but-blocked detection) — needs
  field reports of what those responses look like before we can
  build a heuristic without false positives.
- **On-disk cookie persistence** — the in-memory jar covers v1.
- **Empirical jitter shape tuning** — uniform ±20% is the
  starting point. Revisit when there's data on what looks more
  human to a given detector class.

### 5.2 Tier 2 — Ship after the first SPA fingerprint incident

Add `--stealth` flag on scrape/batch/crawl. When chromium is
serving the fetch, run a stealth init script in the browser
context before navigation: standard `chromedp-undetected` patches.

**Decision rule:** a consumer reports that a SPA target serves
real content in a real Chrome browser but not in
`trawl crawl --tiers chromium`. Verify it's a stealth-patch-fixable
case (not a behavioral check), then ship.

#### SHIPPED 2026-04-11

Built in the same speculative PR as Tier 1. Decision rule
retroactively waived; same reasoning.

What actually shipped, deviations from the design above:

- `--stealth` flag on scrape/batch/crawl/map. When set, the
  chromium engine prepends a `chromedp.ActionFunc` calling
  `page.AddScriptToEvaluateOnNewDocument` with the embedded
  stealth script before `chromedp.Navigate`. The script runs in
  every new document context, including the main frame, before
  any page script.
- `internal/engine/stealth.js` is **maintained in-tree** (the
  §9 alternative — vendor an upstream lib — was rejected). The
  script is short (~110 lines) and the patches are well-known;
  in-tree avoids drift and an extra dep. Patches included:
  navigator.webdriver → undefined, navigator.plugins → 3-entry
  fake list, navigator.languages → ["en-US","en"],
  window.chrome.runtime stub, Permissions.query notification
  override, WebGL UNMASKED_VENDOR/RENDERER → "Intel Inc." /
  "Intel Iris OpenGL Engine", iframe.contentWindow.chrome
  re-attach.
- The script is embedded into the binary via `//go:embed
  stealth.js` so the single-static-binary deploy story holds.
- `ChromiumConfig` grew a `Stealth bool` (does the work) and
  a `BrowserLike bool` (record-only marker so chromium-served
  rows in a `--browser-like --stealth` job stamp `browser_like:
  true` even though chromium itself doesn't run the Tier 1
  header injection — chromium IS already a real browser).
- End-to-end test in `internal/engine/chromium_test.go`:
  `TestChromiumStealthHidesWebdriver` stands up an httptest
  server serving a page that prints `navigator.webdriver` into
  a div, fetches it twice (stealth off / on), and asserts the
  div content flips from `"true"` to `"undefined"`. This is
  the cheapest possible proof the script actually runs.

What was deferred from this PR:

- **Mouse / scroll event simulation** — §3 rates this low-
  efficacy versus the cost. Revisit only if a target's
  behavioral check fires despite the script-injection patches.
- **Vendor catalog beyond Intel/Apple** — the WebGL strings
  are hard-coded to a desktop integrated GPU. If a detector
  starts cross-checking against the UA platform (e.g. expecting
  AMD on a Linux box) we'll add a small lookup table. Not yet.

#### SHIPPED 2026-04-16 — depth enhancement

Live probe run against real-world walled sites after v0.8.1
(soft-block detection) made it possible to *see* which walls
chromium+stealth was still hitting. Headline finding: Cloudflare
IUAM ("Just a moment") → defeated. DataDome and PerimeterX →
still detecting trawl on the chromium path. The patches above
cover the trivial tells (webdriver, plugins, WebGL strings) but
modern canvas/audio fingerprinting and device-attribute checks
are the load-bearing differentiators.

Seven patches added (stealth.js grew ~110 → ~280 lines):

1. **Canvas fingerprint noise** — `toDataURL`, `toBlob`, and
   `getImageData` add imperceptible per-session noise (~0.1% of
   pixel channels XORed in the LSB). A `WeakSet` tracks already-
   perturbed canvases so the same canvas encoded twice in one
   document yields identical hashes (real-browser determinism).
   Per-document `docSeed = Math.random()` keeps the pattern
   deterministic within a render but variable across sessions —
   so a population of trawl runs looks diverse rather than
   clustered on one hash. Mulberry32 PRNG for the distribution.
2. **Audio fingerprint noise** — `AnalyserNode.getFloatFrequencyData`
   / `getByteFrequencyData` and `AudioBuffer.getChannelData`
   get the same treatment with sub-integer sinusoidal jitter.
3. **Rich `window.chrome`** — `chrome.app` (with InstallState /
   RunningState enums + `getDetails`/`getIsInstalled`/`runningState`
   methods), `chrome.csi()`, `chrome.loadTimes()` returning
   plausible shapes. Real Chrome still exposes these deprecated
   APIs; their absence was a detection vector.
4. **Expanded permissions.query shim** — beyond notifications:
   geolocation, camera, microphone, clipboard-read, clipboard-
   write, midi. All return `state: 'prompt'` (the pre-interaction
   default). `Notification.permission` getter forced to
   `'default'` so headless's `'denied'` default doesn't read.
5. **`Navigator.prototype.webdriver` proto defense** — detectors
   that walk the prototype to `Object.getOwnPropertyDescriptor(
   Navigator.prototype, 'webdriver')` now see our getter
   returning undefined rather than the native `true` on the proto.
6. **`navigator.deviceMemory` / `navigator.hardwareConcurrency`** —
   default to `8` if unset. Real browsers always expose these;
   `undefined` was a headless tell.

**NOT SHIPPED (deferred, JS-unreachable):**

- **`window.outerWidth` / `outerHeight` realism.** Chromium exposes
  these as unforgeable [Replaceable] accessors on the Window
  instance; `Object.defineProperty(window, ...)` and
  `window.__defineGetter__` are both silently rejected. A proper
  fix requires CDP `Emulation.setDeviceMetricsOverride` from the
  Go side. Deferred — not a JS-level concern.
- **Font enumeration defense.** Measuring text widths against
  known fonts is a real fingerprint surface, but the obvious
  interventions (block `measureText`, alias fonts) risk breaking
  legitimate page rendering. Revisit only if a consumer hits a
  detector that specifically does font probing AND the current
  patches don't cover it.
- **`Error.prepareStackTrace` / CDP artifact scrubbing.** Headless
  Chromium's stack traces leak through if a detector throws and
  parses `err.stack`. Marginal payoff; skipped.
- **MediaDevices.enumerateDevices, Battery API, full WebGL
  extension catalog.** Low detector-usage in the wild; skipped.

**Live validation matrix (2026-04-16, `--tiers chromium --stealth`):**

| Site            | Wall vendor | v0.8.1 | v0.8.2 |
|-----------------|-------------|--------|--------|
| glassdoor.com   | CF IUAM     | ✅      | ✅      |
| fiverr.com      | PerimeterX  | N/T    | ❌ (PX caught) |
| g2.com          | DataDome    | ❌      | ❌ (DataDome caught) |
| ticketmaster.com | none (from this IP) | ✅ | ✅ |

HTTP tier with `--browser-like --tls-match chrome` continues to
defeat fiverr (1.4 MB response, tier=http). The chromium+stealth
path is actually **less** stealthy than HTTP-with-evasion for
fingerprint-gated sites — chromium exposes CDP artifacts that the
HTTP engine simply doesn't have. Depth enhancements narrow this
gap but don't close it entirely. Soft-block detection from v0.8.1
keeps working as the backstop: every wall we don't defeat shows
up cleanly in `metadata.soft_block` with the vendor attributed.

**Diagnostic guidance for future session:** when a walled site
needs more work, first check which tier serves the wall
(`metadata.soft_block.tiers`). If HTTP tier walls and chromium
succeeds, the fix is more HTTP evasion (TLS variants, HTTP/2
SETTINGS forging). If chromium walls, the fix is more JS stealth
or CDP-level counter-measures. If BOTH tiers wall on the same
vendor (e.g. DataDome), the target is past what a flagless tool
can reasonably defeat — proxy rotation (Tier 4) is the next
lever, not more stealth.

### 5.3 Tier 3 — Ship only on clear data that Tier 1+2 are insufficient

Add `--tls-match chrome|safari|firefox` flag. Replaces the HTTP
engine's transport with `utls`-based fingerprint forgery.

**Decision rule:** a consumer reports that Tier 1+2 fail on a
specific target, AND their packet capture shows the target is
JA3/JA4-blocking (not 429-rate-limiting or Cloudflare-challenging).
Evidence requirement is high because Tier 3's maintenance burden
is real — a stale `utls` version is a reliability bug, not just
a feature gap.

#### SHIPPED 2026-04-11

Built speculatively, same justification as Tier 1 + Tier 2:
"the next hostile target should hit a tool that's already ready."
The §5.3 decision rule above is therefore retroactively waived
for the Chrome preset only. The decision is recorded in
`docs/DECISIONS.md` along with the maintenance commitment, since
Tier 3's maintenance burden is the load-bearing reason the rule
was strict in the first place.

What actually shipped, deviations from the design above:

- `--tls-match <preset>` flag on scrape, batch, crawl, and map.
  Wired through the same shared `cmd/trawl/evasion.go` helper as
  the four Tier 1+2 flags. **Only `chrome` is shipped** —
  `safari` / `firefox` were deferred to "second consumer asks."
  An invalid preset fails the command immediately rather than
  silently degrading to Go's stdlib fingerprint.
- `HTTPConfig.TLSMatch string` field. Empty (the default)
  preserves the byte-identical pre-Tier-3 stdlib transport.
  Non-empty branches `NewHTTP` to construct a uTLS-backed
  transport via `internal/engine/tls_utls.go:newUTLSTransport`.
- **Strategy:** keep stdlib `http.Transport` intact (HTTP
  semantics, conn pooling, redirects, retries all unchanged) and
  swap **only** the TLS handshake by setting
  `Transport.DialTLSContext`. Inside the dialer we open a raw
  TCP conn, hand it to `utls.UClient` with `HelloChrome_Auto`,
  and return the resulting UConn. This is the narrowest possible
  change — every other code path in `internal/engine/http.go`
  is untouched on the non-TLS-match path.
- `HTTPConfig.TLSRootCAs *x509.CertPool` was added alongside.
  Defensible production feature (trust an internal CA without
  disabling verification) and required for the local TLS test
  fixtures to validate self-signed certs. Threaded into BOTH
  the stdlib transport's `TLSClientConfig` and the uTLS path's
  `utls.Config.RootCAs`.
- `metadata.evasion.tls_match` field on the JSONL record (and
  `EvasionInfo.TLSMatch` engine-side). Stamped by the HTTP
  engine in `fetchOnce` whenever `cfg.TLSMatch != ""`. Empty
  on every other engine path so default-mode JSONL stays
  byte-identical.
- `engine.ValidateTLSPreset` is the single source of truth for
  "is this a known preset name." `applyEvasion` calls it before
  touching any config field so a typo fails the command.

**HTTP/2 over forged TLS — SHIPPED 2026-04-12.**
The original limitation (HTTP/1.1 only, ALPN forced to
`["http/1.1"]`) is resolved. The uTLS transport now uses a
dual-transport `utlsRoundTripper` that wraps both a
`golang.org/x/net/http2.Transport` (for h2) and a stdlib
`http.Transport` (for h1 fallback). Both share the same uTLS
dial function with Chrome's real ALPN list `["h2", "http/1.1"]`
preserved. After the TLS handshake, the negotiated protocol
determines routing: h2 → x/net/http2.Transport, h1 → stdlib.
Each transport manages its own connection pool, so the routing
decision is per-host and amortized after the first request.

The forged JA4 is now `t13d1516h2_...` — indistinguishable from
real Chrome on every dimension: cipher list, extensions, signature
algorithms, supported groups, AND ALPN protocol marker.

For h1-only servers, the first request burns one extra TCP+TLS
handshake (the h2 dialer dials, sees ALPN says h1, returns
`errNotH2`, the h1 dialer dials again). Subsequent requests to
the same host hit the h1 pool with zero waste. Most modern sites
negotiate h2, so the fallback is rare.

**HTTP/2 SETTINGS frame forging is still deferred** per §8.3 —
Go's `x/net/http2` sends its own SETTINGS values, which differ
from Chrome's. This matters only for detectors that combine
TLS+SETTINGS (e.g., Akamai, Cloudflare's most aggressive mode).

**Cipher-list verification:** an end-to-end test in
`internal/engine/tls_utls_test.go` stands up a `tls.Listen` on a
self-signed cert, captures `ClientHelloInfo` via
`GetConfigForClient`, runs the same fetch with `TLSMatch: "chrome"`
and `TLSMatch: ""` against fresh listener fixtures, and asserts
the two cipher lists are NOT equal. This is the cheapest unit
proof that something different is on the wire without committing
the test to a specific cipher list (which would break each time
uTLS rolled HelloChrome_Auto forward).

**Manual smoke procedure:**

```bash
trawl scrape https://tls.peet.ws/api/all --tls-match chrome \
  --format html --tiers http -o /tmp/peet.jsonl
jq -r '.body' /tmp/peet.jsonl | jq '{ja3_hash:.tls.ja3_hash, ja4:.tls.ja4}'
```

Compare to a baseline run without `--tls-match`. A real forgery
shows different `ja3_hash` and `ja4` values; the chrome path's
JA4 should start with `t13d1516h2_` (16-cipher Chrome with h2
ALPN — matching real Chrome exactly). Quarterly verification
against this is the maintenance commitment recorded in
DECISIONS.md.

What remains deferred:

- **Safari / Firefox / iOS / Android presets.** uTLS supports
  them; we ship only Chrome until a second consumer asks.
- **HTTP/2 SETTINGS frame forging.** Separate decision rule;
  haven't seen evidence it's needed.
- **Header order forging.** Pointless on h2 (HPACK has no
  ordering); on h1 it's a rabbit hole with dubious ROI.
- **`--tls-match` for chromium tier.** Chromium has its own
  real Chrome TLS stack — the flag deliberately does nothing
  there. Documented in the flag help.

### 5.4 Tier 4 — Ship in coordination with `docs/PROXIES.md`

No separate evasion implementation. Proxy rotation is the P2
phase described in PROXIES.md; evasion contributes design input
but doesn't ship parallel infrastructure.

---

## 6. What trawl explicitly will NOT do

These aren't deferred — they're **refused**. If a consumer asks
for them, the answer is "that's not what trawl is." A future
contributor who proposes adding one of these should be redirected
to this section before their PR discussion starts.

### 6.1 CAPTCHA-solving services

No built-in integration with 2Captcha, CapSolver, Anti-Captcha,
or similar paid solver APIs. Reasons:
- It turns trawl from a scraper into a "bypass tool" —
  reputationally and legally different categories.
- The solver-service business model exists specifically to beat
  anti-abuse controls that sites put up against exactly the
  behavior trawl would be doing in that mode. Integrating against
  them means trawl is explicitly tooling for the thing the target
  is trying to prevent.
- The dependency story is bad — your crawl now requires a
  third-party account, an API key, and credits. Single-binary
  deploy story gone.
- If a site is serving CAPTCHAs at you, it has already decided it
  doesn't want your traffic. Respect that, or escalate to a
  human-driven flow — don't automate past the "you lost" state.

**What a consumer should do instead:** scrape a different source,
use an authenticated account if they own one, or accept that the
target is off-limits.

### 6.2 Credential-based bypass

No scraping of authenticated areas by loading credentials from a
config file, replaying stolen session cookies, or bypassing auth
walls. The one exception: a consumer who owns the account and
wants to persist THEIR OWN session cookies for scraping THEIR OWN
data is acceptable, but the tooling for that is "use curl/chromium
to log in manually, export the cookies, point trawl at them."
Trawl will NOT ship an auth-management feature, a login flow, or
a credential store.

**Why refuse even for legitimate cases:** the same cookie-import
pipeline that lets you scrape your own data also lets you scrape
a stolen session. Building a first-class "credentials" flag in
trawl encourages ambiguity about which mode you're in. The "use
external tooling to get the cookie, feed it through --extra-headers"
path keeps the responsibility for auth posture with the operator.

### 6.3 DoS-level rate patterns

No `--rate unlimited`, no removal of per-host concurrency caps,
no feature that lets a single trawl process hammer a target fast
enough to affect its availability. The politeness layer will
always be present, and `--rate` will always have a hard ceiling
appropriate to "polite visitor," even if that ceiling is
higher than the current default.

**Why refuse:** the identity of trawl as polite-by-default is
load-bearing for its reputation. A single high-profile incident
of trawl being used in a DoS changes what the tool IS in a way
that can't be un-done, and that incident becomes much more
likely if the default configuration allows it.

### 6.4 Distributed evasion / coordination

No feature that lets multiple trawl processes coordinate their
request patterns to evade rate limits (by sharing IP pools,
rotating across proxies to stay under per-IP caps, etc.).
SPEC §13 already excluded distributed mode as a whole, and
distributed evasion is the worst version of that.

### 6.5 Session theft tooling

No cookie-extraction-from-other-browsers feature, no
credential-stealer-adjacent utilities, no account-takeover-
assistive tooling. Obviously.

---

## 7. Legal and ethical framing

Trawl's authors don't give legal advice. But trawl's design
choices have to assume a legal and ethical posture that's
defensible, because that posture shapes the feature set.

**What the case law roughly supports in the US (2026):**
- hiQ Labs v. LinkedIn (9th Circuit, 2019; reaffirmed 2022):
  scraping public data that anyone can access without logging in
  is generally not a CFAA violation. The case was fact-specific
  and the 9th Circuit's holding isn't universal, but it's the
  strongest precedent for "public scraping is legal."
- Meta v. Bright Data (2024): similarly held that scraping
  public LinkedIn data didn't breach LinkedIn's ToS in a way
  that created liability.
- Van Buren v. United States (Supreme Court, 2021): narrowed
  CFAA's "exceeds authorized access" to mean "you accessed data
  you weren't supposed to," which makes pure-public scraping
  even harder to prosecute under that statute.

**What none of that blesses:**
- Scraping private / authenticated data without owning the
  account.
- Violating GDPR or CCPA by collecting personal data without
  a lawful basis.
- Redistributing copyrighted content outside fair-use limits.
- Bypassing paywalls via cookie replay or bot-detection evasion
  for content that requires payment.
- State-by-state and international variation — what's legal in
  the 9th Circuit may not be legal in France.

**How this shapes trawl:**
- The default configuration is always defensible — polite,
  declared, public-only.
- Every evasion feature is opt-in and the operator takes
  responsibility by flipping the flag. Trawl's logs clearly
  record when evasion was active so a post-hoc audit is
  straightforward.
- Features that would make the tool usable primarily for
  illegal scraping (credential bypass, CAPTCHA solvers, paywall
  defeat) are refused at the product-design level — not just
  undocumented, but actively not built.

---

## 8. Implementation notes

Where each tier would plug into the current architecture:

### Tier 1 — Honest browser mimicry

- `internal/engine/http.go` grows a `RequestBuilder` concept that
  picks headers and User-Agent based on `HTTPConfig.UserAgentStrategy`.
- Cookie jar: add an `http.CookieJar` to the shared `http.Client`.
  Persisted per-job in `$TRAWL_HOME/jobs/<id>/cookies.jsonl`?
  Or in-memory only? Design call — in-memory is simpler and most
  consumers don't need cross-run persistence.
- Jitter: extend `politeness.Gate.Acquire` to add ±20% random
  delay on top of the rate limit. Gate is already the right place.
- `--browser-like` flag on scrape/batch/crawl/map. Also adds
  itself to the JobConfig for resumability.

### Tier 2 — Chromium stealth

- `internal/engine/chromium.go` runs an optional stealth init
  script before `chromedp.Navigate`. The script lives in
  `internal/engine/stealth.js` and is embedded at compile time
  via `//go:embed`.
- `--stealth` flag toggles it. When off (default), no change
  to chromium behavior.
- Upstream dependency: track a maintained fork of
  `chromedp-undetected` or write our own patches. The script
  is short — ~100 lines — so maintaining our own is realistic.

### Tier 3 — TLS fingerprint forgery

- `internal/engine/http.go` grows a `Transport` selector. Default
  is `http.DefaultTransport`. `--tls-match chrome` swaps in a
  `utls.UTransport` wrapped around a `utls.HelloChrome_Auto`
  spec.
- New optional dependency: `github.com/refraction-networking/utls`.
  This is a sizable addition and forces a `go.mod` change; the
  decision rule in §5.3 is what gates it.
- Testing story: harder — can't use `httptest.Server` for TLS
  fingerprint assertions, need a mock server that can inspect
  ClientHello. Use `utls` in the test harness too, or write a
  small ClientHello parser and capture the handshake.

### Tier 4 — Proxy rotation

Out of scope for this doc. See `docs/PROXIES.md`.

### Cross-cutting: observability

Every evasion flag that's active during a fetch should appear in
the record's `metadata.evasion` map so post-hoc audits can see
what was used. Example:

```json
{
  "metadata": {
    "evasion": {
      "browser_like": true,
      "stealth": false,
      "tls_match": "chrome"
    }
  }
}
```

This is load-bearing for the "opt-in, auditable" framing.
Without it, there's no way to prove a given crawl was or wasn't
done in evasion mode.

---

## 9. Open questions for the first implementation PR

These are the decisions the first evasion-related PR will have to
resolve. Not pre-committed here — just surfaced so the
implementation starts from a list of real questions.

1. **Cookie jar persistence** — in-memory only, per-job on disk,
   or cross-job like the tier-learning cache? Leaning in-memory.
2. **UA rotation strategy** — random per request, sticky per host,
   or sticky per job? Leaning sticky per host (humans don't change
   browsers mid-session).
3. **Jitter shape** — uniform ±20% or something more biased toward
   longer delays? Empirical — run the SEP corpus with jitter on and
   off and see which looks more human to a logged detector.
4. **Stealth init script source** — maintain our own or vendor an
   upstream? Upstream is lower-effort but higher drift risk.
5. **TLS fingerprint catalog** — how many presets (Chrome, Firefox,
   Safari, iOS Safari, Android Chrome)? Leaning just Chrome until
   there's a second consumer.
6. **Telemetry for blocked requests** — should the stats aggregator
   track "requests that looked like they got soft-blocked even
   though the HTTP status was 200"? This is the inverse of the
   validity check (which already handles SPA stubs) — it would
   need signals like "body contains 'challenge'" or "content-type
   is text/html but body is <5KB and contains CAPTCHA markers."

   **SHIPPED 2026-04-16.** `internal/validity` now scans small
   (≤50 KB) `text/html` bodies for anti-bot challenge markers
   (Cloudflare `cf-chl`/Turnstile/"Just a moment", Akamai
   "Pardon Our Interruption", DataDome, Incapsula, PerimeterX,
   top-level recaptcha/hcaptcha, generic "Access denied" walls)
   and returns `Valid: false, Escalate: true` with a structured
   `SoftBlock` detection attached. The router propagates every
   per-tier detection through `Attempt.SoftBlock`, and
   `internal/job` aggregates them into `metadata.soft_block` on
   the output record — preserved even when a later tier succeeds,
   so post-hoc analysis can see "this host walled on HTTP but
   chromium got through." `internal/failure` gains a
   `CatSoftBlock` category that fires when every tier walled.
   Marker list is in `internal/validity/validity.go#softBlockMarkers`;
   extend conservatively (each marker is a substring match, so
   over-broad additions will false-positive on legitimate content).

---

## 10. Review cadence

This document should be revisited whenever:

1. A tier is shipped — append a "SHIPPED" subsection with the
   real implementation choices and any deviations from §5.
2. A consumer asks for a refused feature — record the ask in
   §6 as a "we said no to X because Y" entry, so the refusal
   has context for the next consumer who asks.
3. A legal development changes the posture — new case law, new
   regulatory guidance, a major ToS change at a target site.
   Update §7 accordingly.
4. A detection technique becomes prevalent enough that the
   landscape in §2 is out of date.

Stale evasion docs are worse than no evasion doc. If this file
contradicts what the code actually does, fix the file same-day.
