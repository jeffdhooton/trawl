# trawl — decision log

Architectural and scope calls that deserve a durable written record. One
entry per decision. Newest at the top. Each entry must answer: what, why,
what the data said, and what would change our minds.

---

## 2026-04-17 — `--viewport WxH` flag for chromium window sizing

**Decision:** Add a `--viewport` flag to scrape/batch/crawl that sets
the chromium window size via `chromedp.WindowSize`. Format is `WxH`
(e.g. `1440x900`). Both-zero default preserves existing behavior.

**Context:** Full-page screenshots were capturing at chromedp's tiny
default (~756×556), which gave misleading results for responsive
sites. The immediate use case was capturing desktop (1440×900) and
mobile (390×844) screenshots of a personal site; the general case
is any workflow where the operator needs to control which CSS
breakpoints fire or wants consistent screenshot widths across runs.

**Why `WxH` string format:** Matches browser conventions (Chrome's
`--window-size=W,H`, CSS `WxH` viewport shorthand). A single flag
is simpler than `--viewport-width` + `--viewport-height` pairs, and
the parser rejects malformed input with clear error messages.

**Relationship to outerWidth/outerHeight deferral:** `--viewport`
sets the real chromium window size, which affects layout and
screenshots. It does NOT address the deferred
`Emulation.setDeviceMetricsOverride` concern from v0.8.2's stealth
entry — JS-level fingerprint checks that read `window.outerWidth`
still see chromium's native values, not spoofed ones. These are
separate concerns: viewport is a layout tool, CDP emulation is
an anti-detection tool. The deferral stands.

**What would change our minds:** If a consumer needs per-page
viewport switching within a single batch (e.g. mobile for some
URLs, desktop for others), we'd need to move viewport from a
launch flag to per-navigation `Emulation.setDeviceMetricsOverride`
— which would also close the fingerprint gap as a side effect.

---

## 2026-04-16 — chromium stealth depth: canvas/audio fingerprint noise + rich platform shims

**Decision:** Extend the in-tree stealth init script from the v0.5.0-
era 7-patch set (webdriver, plugins, languages, `window.chrome`
stub, Permissions shim, WebGL strings, iframe chrome re-attach) to
a ~13-patch set that also covers canvas fingerprint noise, audio
fingerprint noise, rich `window.chrome` (`.app`/`.csi`/`.loadTimes`),
expanded `Permissions.query` beyond notifications, `Notification.permission`
default-state spoof, `Navigator.prototype.webdriver` defense, and
`deviceMemory`/`hardwareConcurrency` defaults. Keep it in-tree
(reject the vendor-puppeteer-extra-plugin-stealth option from
EVASION.md §9 item 4). Skip `window.outerWidth`/`outerHeight`
realism — Chromium makes those JS-unreachable and the real fix is
CDP `Emulation.setDeviceMetricsOverride`, deferred.

**Context:** After v0.8.1 shipped soft-block detection, a live probe
against real walled sites (g2, glassdoor, fiverr, ticketmaster)
showed exactly which walls chromium+stealth was beating and which
were catching trawl. Headline:
- Cloudflare IUAM → defeated.
- DataDome and PerimeterX → still detecting trawl on the chromium
  path, with clear soft-block vendor attribution.

The gap wasn't on the "trivial tells" (webdriver, plugins) the old
patches covered. It was on canvas/audio fingerprinting and device-
attribute profiling, which modern anti-bot stacks weight heavily.
Shipping the observability in v0.8.1 made it cheap to know which
patches were worth adding.

**Why in-tree over vendoring puppeteer-extra-plugin-stealth:**

- **Package-manager cost.** puppeteer-extra-plugin-stealth is a Node
  package with its own transitive deps. Trawl ships as a single
  static Go binary; introducing a `node_modules` or a JS
  build-step just to grab one file per patch would break the
  deploy story.
- **Drift risk.** The upstream lib changes frequently in response
  to detector arms-race moves, but not always in ways that benefit
  our narrow use case. A vendored copy means we track their
  churn; an in-tree rewrite means we track detector reality.
- **Audit surface.** Our ~280-line file is readable by a single
  human in one sitting. Every patch has a labeled block with
  what-it-defends-against commentary. A vendored multi-file lib
  with its own patch taxonomy is strictly worse to reason about.
- **Scope match.** We don't need Sannysoft/CreepJS parity; we need
  to handle the five or six detectors that actually ship in
  production (CF, DataDome, PerimeterX/HUMAN, Imperva, Akamai).
  Cherry-picking from the upstream is a bigger maintenance burden
  than a fresh 280-line file covering our threat model directly.

**Technical notes that mattered:**

- **Canvas noise needs a per-canvas `WeakSet`.** The naive approach
  (perturb on every `toDataURL` call) is non-idempotent:
  `ctx.getImageData → XOR-perturb → putImageData` flips the SAME
  LSBs on the second call, cancelling the first. WeakSet tracks
  "already perturbed this canvas" so two `toDataURL` calls on one
  canvas return identical hashes (real-browser behavior).
  Discovered this the hard way during test writing.
- **`toDataURL` must NOT route through the patched `getImageData`.**
  The patched `getImageData` adds its own LSB noise. If
  `toDataURL`'s internal read-back goes through the patched
  version, and then we perturb again, XOR cancels. Fix: capture
  originals (`origGetImageData`, `origPutImageData`,
  `origToDataURL`) before patching anything, and route
  `toDataURL`'s internal reads through the originals.
- **`window.outerWidth` is JS-unforgeable in Chromium.** Neither
  `Object.defineProperty(window, 'outerWidth', …)` nor
  `window.__defineGetter__('outerWidth', …)` works — Chromium's
  binding layer silently rejects both. Real fix is CDP
  Emulation from the Go side; deferred.
- **Per-document seed must NOT be exposed as a global.** An earlier
  draft set `window.__trawlDocSeed = docSeed` for test diagnostic.
  That's itself a detection signal — a real Chrome has no such
  global. Removed; test now compares canvas PNG bytes directly.

**What would change our minds (add more patches):**

- A consumer reports a specific walled site that soft-block
  telemetry attributes to a new vendor we don't currently cover.
- Detection on a site we DO cover moves to a signal we don't patch
  (e.g. font enumeration via `measureText`).
- DataDome or PerimeterX release a detection update we can
  reverse-engineer from their client-side script.

**What would change our minds (remove a patch):**

- False-positive reports: a legitimate site breaks because our
  canvas/audio noise drifts rendering or audio playback enough
  to be noticed. Noise levels are tuned conservatively (0.1%
  pixels, sub-integer audio samples) but revisit if reports land.
- A vendor stops checking a specific signal (e.g. deprecates a
  `window.chrome.csi` check); removing the shim cuts code surface.

**Live results:**

| Site | Vendor | `--tiers chromium --stealth` v0.8.2 | Notes |
|---|---|---|---|
| glassdoor.com | CF IUAM | ✅ bypass | 344 KB content |
| fiverr.com | PerimeterX | ❌ caught | Still wallable on chromium; HTTP+full evasion still bypasses |
| g2.com | DataDome | ❌ caught | DataDome checks beyond our patches |
| ticketmaster.com | none | ✅ passthrough | No wall from our IP |

Soft-block metadata cleanly attributed every failure to the right
vendor — v0.8.1's telemetry continues to earn its keep.

---

## 2026-04-16 — soft-block detection as an escalation signal, aggregated across tiers

**Decision:** A 200 OK response whose body matches an anti-bot challenge
marker (Cloudflare "Just a moment", Akamai "Pardon Our Interruption",
DataDome captcha, Incapsula, PerimeterX, top-level recaptcha/hcaptcha,
generic "Access denied" walls) is classified as `Valid: false,
Escalate: true` inside `internal/validity`. The router tries the next
tier, exactly as it does for an SPA shell or a 5xx. A per-tier
structured detection (`{Vendor, Marker}`) travels through
`router.Attempt.SoftBlock` and is aggregated into `metadata.soft_block`
on every output record — populated **even when a later tier
succeeded**, so a consumer can see "this host walled HTTP but chromium
got through" without re-deriving it from the router outcome.
`failure.CatSoftBlock` is the final classification when every tier
walled.

**Context:** Trawl's validity heuristic handled SPA stubs (empty
`<div id="root">`) but treated a Cloudflare "Just a moment" page as
success — the status code was 200 and the body was >512 B. That's the
worst possible failure mode: it silently records a challenge wall as
content and signals nothing downstream. Before shipping more evasion
tiers, trawl needs a *better signal* — the observability to know a
wall was hit, separate from the decision to fight through it.

This is EVASION.md §9 item 6 — the only item from the original open
questions that was about *signal*, not *technique*. Every other open
item was an arms-race step (more TLS presets, SETTINGS forging,
stealth script depth). This one is polite-first in shape: it doesn't
change what trawl sends, only what it notices.

**Why aggregate across tiers, not just "final outcome":** When tier 1
walls and tier 2 succeeds, the record is a success — but the host's
behavior ("walls non-browser traffic") is durable information that
will inform the next fetch from the same host. Surfacing it as forensic
metadata costs nothing (a few bytes of JSON) and lets downstream
analysis pin chromium on known-walling hosts, feed a future
rotate-on-soft-block trigger, or prioritize which sites are worth
investing Tier 3 TLS forgery on. Dropping the signal the moment a
later tier saved us would throw that away.

**Marker list conservatism:** Each marker is a case-insensitive
substring match over the body. Over-broad markers (e.g. just
`"cloudflare"`) would false-positive on any page that mentions the
vendor in article text. The shipped list is vendor-specific tokens
that only appear inside real challenge HTML (`cf-chl`, `cf-turnstile`,
`_Incapsula_Resource`, `px-captcha`, etc.) and a short list of generic
block strings gated by a 50 KB body-size cap — real challenge pages
are small; a 100 KB article mentioning "access denied" in prose is
almost certainly not a wall.

**What would change our minds:** If soft-block telemetry shows a
vendor consistently under-detected (e.g. Akamai variants missing from
hosts that are clearly walling), extend the marker list. If false
positives surface on legitimate content, tighten a marker or add a
negative filter. The list lives in
`internal/validity/validity.go#softBlockMarkers` and is deliberately
data-driven — not a taxonomy to defend.

**Not shipped:** CLI flag to disable (`DetectSoftBlock` is a Config
bool only; no callers toggle it today); soft-block as a trigger for
`--rotate-on-status`-style proxy rotation (adding it is one lookup in
`router.fetchWithProxyRotation`, hold for a consumer ask). Per-page
stats aggregation in `internal/stats` already handles soft-block
automatically via `failure.CatSoftBlock` being in `AllCategories()`.

---

## 2026-04-16 — MCP server, official `modelcontextprotocol/go-sdk`, stdio only

**Decision:** Ship `trawl mcp` — a Model Context Protocol server over
stdio that exposes the existing CLI surface (scrape, batch, crawl,
map, sitemap) as agent tools. Built on the official
`github.com/modelcontextprotocol/go-sdk` (v1.5.0). Stdio transport
only for v1 — no HTTP/SSE. Hard caps on batch (50 URLs), crawl (500
URLs), and map (5000 URLs) per call; above the cap the error message
points at the corresponding CLI subcommand.

This required extracting `cmd/trawl`'s orchestration into a new
`internal/job` package so the MCP handlers can call `job.Run` /
`job.RunOne` / `job.RunMap` directly, without subprocess-spawning
themselves. CLI commands shrank to flag-binding + cobra wiring.

**Context:** Trawl's positioning has been "designed for AI agents to
call directly" since the POSITIONING.md doc. Until this PR, that
manifested as a CLI-with-JSON-output that agents shell into via Bash.
MCP closes the loop: the agent sees trawl as native tools with typed
args, structured results, and per-tool when-to-use guidance written
for LLMs. This is the most positioning-amplifying feature trawl can
ship — every other on-roadmap candidate is a feature add; MCP is a
shape change.

**SDK choice — official over community:** Two viable Go SDKs:

| | mark3labs/mcp-go (community) | modelcontextprotocol/go-sdk (official) |
|---|---|---|
| Version | v0.48.0 (still v0.x) | **v1.5.0 (stable)** |
| Stars | 8.6k | 4.4k |
| Production users | community | **Google Cloud, Docker, Datadog, Anthropic, gopls** |
| API style | option-based DSL | generic `AddTool[Args]` from struct |
| Spec versioning | informal | **published compatibility table** |
| Extra deps | minimal | oauth2/jwt (unused by us) |

Picked official for: stable v1.x semver (mark3labs has shipped
breaking minors), maintained spec-compatibility commitment, and
struct-based tool definitions that fit trawl's existing
JobConfig-with-JSON-tags pattern naturally. Tradeoff: slightly more
verbose API and a few oauth2/jwt deps in `go.sum` we don't use. Worth
it for the stability story trawl's "polite, predictable, single
static binary" positioning demands.

**stdio only for v1:** The SDK supports HTTP/SSE but adding it brings
auth, CORS, deployment, and a daemon lifecycle — none of which has a
clear consumer ask. Stdio matches trawl's "binary spawned by agent"
model exactly. Revisit if a remote-trawl use case appears.

**Hard caps:** A tool call should be a coherent unit of work. 50
URLs in a batch fits in seconds; 500 in a crawl fits in minutes;
5000 in a map fits comfortably. Beyond that, you want the persistent
frontier + resume + per-job stats that the CLI gives. The MCP error
message points at the CLI alternative explicitly.

**Per-call temp dirs:** `trawl_batch` and `trawl_crawl` create a
fresh `os.MkdirTemp` job dir per call, run there, read JSONL output
back into memory, and `os.RemoveAll` on return. No cross-call state.
The persistent tier-learning cache at `$TRAWL_HOME/tier-cache` is
still consulted (and updated) so MCP and CLI calls share learning.

**internal/job extraction:** The orchestration refactor was the bulk
of the work — `cmd/trawl/{job,runner,router,contentcache,tiercache}.go`
and parts of scrape/batch/crawl/map/proxy/evasion/pdf_flags moved
into a new `internal/job` package with three entry points: `Run`
(frontier-driven batch/crawl), `RunOne` (single URL), `RunMap` (map
crawl source). cmd/trawl now contains only cobra wiring + flag
binding. JSON tags on `Config` (was `JobConfig`) preserved exactly
so existing job dirs still resume. ~3700 lines reorganized; tests
all green; CLI behavior byte-identical (smoke-tested scrape/batch/map
on example.com).

**What would change our minds:**
- A remote-trawl consumer (e.g. a hosted agent platform) wanting
  HTTP transport.
- Real evidence that the per-call caps are wrong — feedback from
  agents getting cut off at 50 URLs that should have been allowed
  more, OR resource exhaustion from agents trying to crawl 500 URLs
  inside a single MCP call.
- An async-progress consumer use case that justifies adding MCP
  progress notifications + a `resume` tool.

---

## 2026-04-14 — PDF engine: shell out to poppler, decline the standalone Rust competitor

**Decision:** Build a PDF engine inside trawl that shells out to
user-installed binaries (`pdftotext`, optionally `tesseract`) rather
than (a) parsing PDFs in-process with a pure-Go library, (b) enabling
CGO to bind `pdfium`/`mupdf`, or (c) building a standalone Rust PDF
tool alongside trawl. The engine is a parallel content-type branch
off the HTTP engine, not a new tier in the HTTP → Chromium ladder.
Full shape is in `docs/PDF.md`.

**Context:** Firecrawl shipped "Fire-PDF" in April 2026 — a Rust-
based PDF parser with layout awareness and OCR. It's the last
content-extraction capability trawl lacks per the ROADMAP gap
analysis. Every trawl crawl of a research, government, or policy
site today produces wrong output when it hits a PDF (raw bytes in
`body`), so the floor for shipping *any* PDF handling is "better
than current." The question was architectural, not whether.

**What the data said:** No benchmark run — this is a design-phase
call, not a performance call. The relevant facts were structural:

- Go's native PDF libraries (`ledongthuc/pdf`, `pdfcpu`, `unipdf`)
  are weak on multi-column and tables, have no native OCR, and
  `unipdf` is commercial. None of them are in the same quality tier
  as `pdftotext`/`marker`/`docling`.
- Serious in-process PDF parsing with OCR needs `pdfium` + `tesseract`
  under the hood. Go bindings (`klippa-app/go-pdfium`, `gosseract`)
  are CGO wrappers. Enabling CGO violates SPEC §7 and breaks the
  single-static-binary deploy story that's already paid off in
  `v0.6.0` for proxy support and elsewhere.
- A standalone Rust PDF tool is architecturally clean but the OSS
  landscape is already crowded (`marker`, `docling`, `ocrs`, MinerU,
  `pdf-extract`). Fire-PDF's "5x faster" claim benchmarks against
  those mature tools with a team behind it. Building a new Rust tool
  from scratch is a fun project but a bad allocation — trawl moves
  forward this month with a shell-out engine, and an in-process Rust
  parser can swap into the shell-out interface if it ever becomes
  justified.
- Shelling out to `pdftotext` keeps trawl pure-Go, pure-static-binary,
  and lets users opt into more sophisticated tooling (marker,
  docling) as a future Tier 3 without trawl itself growing any new
  dependencies.

**Why a parallel branch rather than a new tier:** trawl's existing
tier ladder models "same content type, different rendering cost."
PDF is a different content type entirely. Mixing PDF tiers into the
HTML ladder would require every HTML tier to know what to do with
bytes it can't render, and the routing decision is naturally driven
by `Content-Type: application/pdf` from the HTTP response — a single
header check, not a validity heuristic.

**Scope calls locked in via `docs/PDF.md`:**

1. **Text-layer + basic layout in v1** (Tiers 1 and 2 via
   `pdftotext` and `pdftotext -layout`). OCR ships in v1 gated
   behind `--ocr`, off by default. Scope-cutting item if schedule
   slips: OCR moves to v1.1.
2. **Soft-fail on missing `pdftotext`**, hard-fail at command-start
   on missing `tesseract` when `--ocr` was passed. Reasoning: the
   soft-fail keeps trawl useful for HTML on machines without poppler,
   but OCR is an explicit opt-in and failing 100 URLs in before
   discovering tesseract is missing is unfriendly.
3. **Markdown default output + `metadata.pdf` struct** matching how
   `metadata.page` is structured for HTML. Consumers using
   `jq '.metadata.page.title'` work uniformly across HTML and PDF
   inputs because the PDF engine copies title into `metadata.page`
   as well.
4. **Explicit non-goals:** in-process parsing, image extraction,
   form-field extraction, password-protected PDFs, marker/docling
   integration, PDF-shaped `--schema` extraction. All deferred
   pending a concrete consumer ask.

**What would change our minds (= redesign required):**

- **A consumer shows `pdftotext` producing garbage that `marker` or
  `docling` handles cleanly on the same document.** Tier 3 (Marker
  integration) moves from "deferred" to "build" — the shell-out
  interface is designed so this is a contained addition, but marker
  has a Python/PyTorch dependency surface that we're not taking on
  speculatively.
- **A consumer needs structured PDF extraction** (form fields,
  tables-as-cells, figure bounding boxes). That's a different
  feature shape; parallel design doc, not a mutation of PDF.md.
- **OCR becomes a hot path rather than an opt-in niche** (>30% of
  PDFs in real use are scanned). If so, the default-off stance is
  wrong and OCR moves into the main escalation ladder. Unlikely
  for general-purpose scraping, plausible for specific verticals.
- **`pdftotext` becomes the bottleneck at scale.** The fix would be
  batching (one invocation over many docs) or a long-lived worker
  process. Both are non-trivial and would be explicit additions to
  the engine design, not silent changes.
- **Someone credibly argues for CGO.** The SPEC §7 rule is durable,
  but it's not a religious commitment — if a CGO-enabled build
  variant (`trawl-cgo`) could ship alongside the pure-Go binary and
  the maintenance cost is bounded, that's a future DECISIONS.md
  entry. Nothing about the shell-out engine forecloses it.

**What this does NOT change:**

- SPEC §7's no-CGO rule stands. This decision is *compatible* with
  no-CGO by design, not a precedent for waiving it.
- The existing tier ladder (HTTP → Chromium) is untouched.
- Nothing about trawl's politeness model, robots.txt handling, or
  tier-learning needs to change to accommodate PDFs — the content-
  type branch inherits all of it.

**Authored during session:** 2026-04-14. Design doc
`docs/PDF.md` written in the same session. Implementation pending.

---

## 2026-04-11 — Tier 3 evasion (Chrome JA4 forgery): also speculative ship, narrow scope, accept maintenance commitment

**Decision:** Ship `--tls-match chrome` (Tier 3 ClientHello forgery
via `github.com/refraction-networking/utls`) without waiting for a
consumer JA3/JA4-blocking report. Same speculative-ship logic as
the Tier 1+2 decision below, with two extra constraints that the
load-bearing nature of the §5.3 decision rule deserved:

1. **Narrow scope.** Ship Chrome only — no Safari/Firefox/iOS
   presets, no HTTP/2 SETTINGS frame forging, no header order
   forging. Each new preset is a future maintenance burden, and
   shipping all of them speculatively multiplies the cost without
   evidence any of them are needed.
2. **Maintenance commitment recorded.** uTLS's `HelloChrome_Auto`
   tracks the latest Chrome the *uTLS library* has seen, NOT the
   latest Chrome that's actually deployed. Quarterly verification
   against `tls.peet.ws` is the durable commitment that makes
   "speculative ship" honest — without it the feature decays from
   "Tier 3 evasion" to "Tier 3 fingerprint that no real Chrome
   matches anymore."

**Context:** `docs/EVASION.md` §5.3 set the strictest gate of any
tier: a consumer report with a packet capture showing JA3/JA4
blocking AND demonstrable Tier 1+2 failure. The bench at
`bench/evasion/run.sh` from the Tier 1+2 PR found 0/12 reachable
hostile targets where Tier 1+2 was insufficient (G2 was a
DataDome CAPTCHA, refused per §6.1). So the strict reading of the
rule says "wait." But the same speculative-ship logic that won
for Tier 1+2 — "the next hostile target should hit a tool that's
ready" — applies here too, as long as the maintenance cost is
acknowledged and bounded.

**What the data said:** Mixed, recorded honestly.

- **The forgery is real.** Smoke against `tls.peet.ws` confirms
  the wire-level signature changes: forged JA4
  `t13d1516h1_8daaf6152771_d8a2da3f94cd` (16 ciphers, Chrome
  cipher list, h1 ALPN) vs baseline `t13d1312h2_f57a46bbacb6_ab7e3b40a677`
  (13 ciphers, Go stdlib, h2 ALPN). Different JA3 hash, different
  JA4, different cipher count. The forgery is observable from
  the server side, which is the necessary precondition for it
  to be useful.
- **The forgery does not unblock anything in our 13-target bench.**
  `bench/evasion/run.sh` was extended with a `tier3` mode
  (`--browser-like --tls-match chrome --tiers http`, forced to
  http to isolate the ClientHello from chromium's real Chrome
  TLS) and re-run on the same `targets.txt` corpus. **0 of 13
  targets** flipped from blocked-at-tier1 to ok-at-tier3. The
  four targets where the http path was blocked at tier1
  (glassdoor, crunchbase, g2, walmart) returned the **same**
  403/404 response under tier3, with body sizes within ~100
  bytes of the tier1 responses. The TLS handshake completed;
  the blocks are at the HTTP/application layer (UA/header
  gating, JS fingerprinting, CAPTCHA challenges) — none of
  which TLS forgery touches.

**What this means for the speculative-ship decision:** the bench
**confirms** the strict reading of §5.3 was correct in the
narrow sense that Tier 3 doesn't unlock anything in our current
corpus. It does not **refute** the speculative-ship rationale —
that rationale was always "be ready for the next hostile target,"
not "fix something we have today." The cost of being ready
turned out to be ~400 LOC plus the maintenance commitment below.
Future-us should know:

- If a consumer ever shows up with a JA3/JA4-blocking target,
  Tier 3 is sitting in trawl ready to use, and DECISIONS.md
  records *why* it was built without a consumer ask.
- If 6 months pass and no consumer ever uses `--tls-match`,
  the right call is to keep it (cost is bounded by the quarterly
  check) but not invest further in Tier 3 follow-ups (h2 over
  uTLS, additional presets, SETTINGS frame forging) until the
  decision rule is properly satisfied.
- The bench is the empirical floor: anyone proposing "we should
  add `--tls-match safari`" or "we should ship h2 over uTLS"
  must first show a target the current Tier 3 fails on AND
  point at a consumer that needs it. Speculative-build is a
  one-time waiver, not a precedent.

**Known limitation accepted:** HTTP/1.1 only over the forged
transport. Stdlib `http.Transport`'s auto-h2 upgrade requires
`DialTLSContext` to return `*tls.Conn`; uTLS UConn isn't one. We
override the parrot's ALPN extension to advertise http/1.1 only
so the wire protocol matches what ALPN claims. The cost is that
forged JA4 deviates from real Chrome on the ALPN dimension only
(`h1` vs `h2`). Detectors that match on ja4_a + ja4_b (cipher +
extension hashes) still see "Chrome." Detectors that match on
the full JA4 string see "Chrome but http/1.1-only." Lifting the
limitation requires routing h2 traffic through
`golang.org/x/net/http2.Transport` with a custom DialTLS — its
own follow-up.

**What would change our minds (= revert to deferred or rewrite):**

- A consumer reports Tier 3 broke a target that Tier 1+2 was
  reaching, in a way that fingerprint changes alone can't fix.
  That would mean we encoded an assumption about *how* JA3/JA4
  detection works that turned out wrong.
- The quarterly verification reveals that real Chrome's
  fingerprint has drifted enough from `HelloChrome_Auto` that
  forging it is no better than not forging — at which point we
  either bump the uTLS pin or accept that the feature is stale
  and document it.
- HTTP/1.1-only forced fallback turns out to be a problem in
  practice (h2-only servers exist and break under our forgery).
  In that case the "ship h2 over uTLS via http2.Transport"
  follow-up moves from "would be nice" to "blocking."

**Maintenance commitment (the load-bearing part):**

- **Quarterly:** run the smoke against `tls.peet.ws` with
  `--tls-match chrome` and confirm the JA4 still matches a
  real Chrome (modulo the documented `h1`/`h2` ALPN difference).
  If it drifts, bump `github.com/refraction-networking/utls` and
  re-verify.
- **On Chrome major release:** spot-check that `HelloChrome_Auto`
  in our pinned uTLS version still parrots a recent Chrome. If
  the gap exceeds two majors (~3 months), bump.
- **On any consumer report of Tier 3 failure:** the smoke and
  the comparison against the consumer's packet capture are the
  diagnostic — fix the gap or escalate per the "what would
  change our minds" rules above.

The pin is `github.com/refraction-networking/utls v1.8.2`
(installed 2026-04-11). When this entry is read in the future,
diff against current Chrome and decide.

---

## 2026-04-11 — Tier 1 + Tier 2 evasion: build speculatively, waive the consumer-ask rule

**Decision:** Ship `--browser-like` (Tier 1: rotating UAs, Chrome
header set, in-memory cookie jar, ±20% jitter) and `--stealth`
(Tier 2: chromium init script patching navigator.webdriver and
friends) without waiting for a consumer to report a hostile-target
failure. Tier 3 (uTLS fingerprint forgery) and Tier 4 (proxy
rotation) **stay deferred** behind their existing decision rules.

**Context:** `docs/EVASION.md` §5.1 and §5.2 each specified a
"ship when a consumer reports failure mode X" gate. The principled
argument for those gates was that speculative evasion features
guarantee an ad-hoc shape that embeds the first consumer's specific
bypass. The counter-argument that won this round: Jeff has decided
the next hostile target should hit a tool that's already ready,
not discover the gap mid-incident. The shape was already designed
in EVASION.md before any consumer pressure existed, so the original
"ad-hoc shape" risk is mitigated — the doc was the design exercise,
this PR is the build exercise.

**What the data said:** N/A — explicitly speculative. The justification
is "the design doc is mature and the cost of building Tier 1+2 is
small." Tier 3's maintenance cost (uTLS fingerprints rotate) and
Tier 4's scope (proxies are their own phase) keep them deferred —
those gates are still in force.

**What would change our minds (= revert to deferred):**

- A consumer reports that Tier 1+2 broke against a real target in a
  way the doc didn't anticipate, AND fixing it requires changing the
  shape we built (not just adding a new patch to stealth.js). That's
  evidence the speculative build encoded a wrong assumption.
- The maintenance burden of stealth.js patches grows beyond ~200
  lines or starts requiring per-target customization. At that point
  we either vendor an upstream lib or refuse to chase the patch
  treadmill (per the §5.2 "we don't fight a sophisticated arms race"
  framing).

**Implementation deviations from EVASION.md (recorded inline in
the §5.1 / §5.2 SHIPPED subsections, summarized here):**

- Cookie jar is in-memory per-job, not on-disk per-host (resolved
  the §9 "leaning in-memory" question in favor of in-memory).
- UA rotation is sticky-per-host (matched the §9 lean).
- The pool excludes Firefox/Safari because their `Sec-CH-UA` headers
  differ from Chromium's — mixing them in would create inconsistent
  header blocks. The doc was silent on this.
- `metadata.evasion` is a typed nested struct, not a `map[string]any`
  (because `output.Metadata` is already a typed struct everywhere
  else and the consistency mattered more than the doc's example
  shape).
- Stealth script is in-tree (~110 lines), embedded via `//go:embed`,
  rather than vendored from `chromedp-undetected` (resolved the §9
  question against vendoring).

## 2026-04-10 — Feature batch: crawl, map, screenshots, cache, schema, CSV, retries, politeness

**Decision:** Ship eight features in a single session as a Firecrawl
parity pass and quality-of-life improvements, with specific non-obvious
shape calls recorded here so future maintainers understand the
semantics without re-reading the commits.

**Context:** SPEC §13 deliberately excluded LLM extraction, webhooks,
and a web UI — but left many smaller features unspecified. The
ROADMAP's gap analysis against Firecrawl flagged seven of these as
in-scope and small-to-medium cost. All were built in one session
2026-04-10 and pushed as commits `112c713` and `b77d81d`. The phase
writeups in `docs/ROADMAP.md` have the full detail; this entry
captures the architectural calls that weren't otherwise obvious from
the code.

### Schema extraction (`--schema`) — v1 shape

**Unblocking consumer:** Jeff's business partner scraping Stanford
Encyclopedia of Philosophy articles via Firecrawl. SEP gave us a
concrete, non-trivial schema shape (flat fields + arrays-of-objects
with attribute extraction) to design against.

**Design calls worth remembering:**

1. **Empty selector = "self"** inside a nested `fields` context. The
   SEP case proves this is necessary: every array-of-objects
   extraction wants to grab text/attr from the iterated element
   itself, not a child. Alternative syntaxes considered: `"."`,
   `":self"`, `"&"` (Sass). Empty was picked because it's the least
   noisy and parse-unambiguous. Top-level empty selectors are
   rejected at Load time so the "self" meaning can't leak up.
2. **Relative URLs stay relative.** `extracted.link = "../foo/"` in
   the output, not `"https://host/foo/"`. Reason: SEP consumers need
   to cheaply distinguish intra-encyclopedia links from external
   ones. They already have `record.canonical_url` if they want to
   resolve. What would change our minds: a concrete consumer that
   needs absolute URLs by default AND whose consumers can't join
   against canonical_url.
3. **Missing matches are omitted from output**, not present as `""`
   or `null`. Consumers can `jq 'select(.extracted.foo)'` to filter
   on presence. Explicit `required` fields are v1-excluded —
   silent omission is simpler and sufficient for known consumers.
4. **`version: 1` is mandatory.** Future breaking changes can ship
   without inventing a second format.

### Content cache (`--cache`) — semantic subtleties

1. **Opt-in, not opt-out.** A fresh `trawl scrape` must never
   surprise the user with stale data from a week ago. Users who
   want caching ask for it with `--cache`. What would change our
   minds: a prominent-enough "served from cache" log line that
   surprise is no longer a risk.
2. **Key is `(canonical URL, tier name)`, not URL alone.** Different
   tiers can produce different bodies for the same URL (HTTP vs
   chromium), so a forced-tier re-run should miss the cache entry
   that a different tier populated. Inside the router tier loop,
   cache lookup happens per-tier before each Fetch.
3. **Cached results still run through validity.** A stale stub
   body in the cache escalates past exactly as if the live fetch
   had returned it — the cache doesn't trap a user in bad content.
4. **Puts only on successful LIVE fetches.** Cache-replay successes
   are NOT re-put. The stored body's timestamp is fixed at the
   moment of the original live fetch; a replay does not "refresh"
   the TTL.
5. **Tier-learning is NOT updated from cache hits.** The learning
   signal is "what served this host LIVE." A single successful
   replay shouldn't lock a host's tier preference.

### HTTP retries (`--retries`) — scope boundaries

1. **Network layer only.** Retries fire on connection-level
   transients (timeouts, ECONNRESET/REFUSED, truncated EOF,
   io.EOF at Client.Do, HTTP 429/502/503/504). They do NOT fire
   on stub-body responses — those stay with the router's
   validity → escalate path. A stub body is a successful fetch
   from TCP's point of view; retrying it would waste budget on a
   server that's serving exactly what it meant to.
2. **Permanent failures return immediately**: ctx cancel/deadline,
   TLS cert verification errors (`*tls.CertificateVerificationError`,
   `x509.UnknownAuthorityError`, `x509.HostnameError`), all 4xx
   except 429. No amount of retrying fixes an expired cert or a
   404.
3. **Chromium does NOT get retries in v1.** chromedp's timeout
   model is different and chromium fetches fail much less often
   from network transients (they fail from JS hangs and memory
   pressure, which retries don't help). Keeping retries HTTP-only
   means one reliable retry path instead of two partially-overlapping
   ones. What would change our minds: a chromium-heavy workload
   with measurable transient failures.
4. **Exponential backoff with jitter, capped at 10s INCLUDING
   jitter.** An earlier draft capped before applying ±25% jitter,
   which meant the observed delay could exceed the cap by up to
   25%. Fixed so "max 10s" means observed max is 10s.

### CSV output (`-o results.csv`) — column strategy

1. **Extension sniffing, no flag.** `.csv`/`.tsv` → CSV sink,
   everything else (and stdout) → JSONL. This matches how most
   Unix tools work and keeps the CLI simple.
2. **Auto-discovered columns from the first record**, not from
   the user's `--selector` list. Reason: a user running
   `trawl batch --selector 'title=h1' --selector 'price=.price'
   -o out.csv` expects `url, canonical_url, ..., title, price`
   to "just work" without repeating themselves in `--csv-columns`.
3. **Column set LOCKS at first write.** Later records with new
   `extracted` keys silently drop those keys. No way to rewrite
   the header once downstream tooling has read it, so we'd rather
   be honest about the tradeoff than pretend we can stream-append.
   `DroppedKeys()` tracks the skipped keys so a post-run summary
   is possible.
4. **Non-scalar values get JSON-encoded inline.** Ugly, but CSV
   is single-valued per cell by definition. `jq` / pandas can
   re-parse. The alternative — dropping non-scalar values silently
   — would lose schema-extracted data without warning.
5. **Dead-letter queue stays JSONL regardless of primary sink
   format.** Benchmark scripts depend on its shape and nobody
   wants a dead-letter CSV that drops half the fields.

### Per-host politeness (`--politeness`) — match rules

1. **Exact host OR `*.suffix` wildcard.** No regex. Regex is a
   rabbit hole of "does this mean the whole string or a substring"
   questions and the overwhelming majority of real overrides are
   "slow-crawl this specific host" or "slow-crawl this TLD." What
   would change our minds: a consumer with a legitimate need for
   regex (not just a preference).
2. **`*.suffix` does NOT match the bare TLD.** `*.gov` matches
   `irs.gov` but not `gov`. This is the Python `fnmatch` "at least
   one label" rule.
3. **Rate AND concurrency overridable, burst stays global.** Burst
   matters less than sustained rate for polite crawling, and
   per-host burst adds a third knob to reason about.
4. **First-match-wins, top-to-bottom.** Users put specific rules
   before catch-alls. Alphabetical sorting would be less
   predictable.
5. **Loaded once at command start.** File changes mid-run don't
   take effect — restart the crawl. Runtime reload would be a lot
   of plumbing for a feature nobody has asked for.

### BFS crawl (`trawl crawl`) — termination protocol

Worth recording because `sync.Cond`-based worker pools are subtle:

- **`BlockingNext` signals `ErrEmpty` only when queue is empty AND
  `inFlight == 0`.** Either condition alone isn't enough — an
  empty queue with workers still processing could yield new
  children any moment.
- **`inFlight` is an in-memory counter**, not derived from
  `StateInFlight` in the frontier DB. It's reconstructed as zero
  on startup because `Recover` drains all in_flight records back
  to queued before workers spin up.
- **Link discovery happens BEFORE `MarkDone`.** If the order were
  reversed, a sibling worker could wake on the empty-queue
  broadcast, see `inFlight == 0`, and terminate the crawl
  prematurely — right before the children get enqueued.
- **`limit` caps URLs ENQUEUED, not fetched.** Deterministic from
  the frontier's natural unit. Dedup-rejected children release
  their budget slot so a cycle-heavy graph doesn't burn the limit
  on duplicates.

### What did NOT change

- No new measurement data on Lightpanda. The rule in the previous
  entry still stands; none of these features unblock its reopening.
  BFS crawl shipped, but nobody has re-run Phase 0 against the
  richer selector library yet.
- No changes to the existing tier router, politeness default
  rates, or URL canonicalization rules.
- SPEC §13 exclusions (LLM extract, search, webhooks, web UI,
  scheduler, distributed mode) all still stand.

**Authored during session:** 2026-04-10.
**Commit references:** `112c713` (five phases), `b77d81d` (three-feature batch).

---

## 2026-04-10 — Defer Lightpanda pending better data

**Decision:** Skip building the Lightpanda engine for now. Ship the
HTTP→Chromium two-tier ladder as v1's default. Revisit after crawl mode
lands and we can re-run Phase 0 against a sample that isn't dominated
by seed rot.

**Context:** SPEC §7 and the original P1 stage 2 plan called for
Lightpanda as a middle tier between HTTP and Chromium, based on a
guessed 60/30/10 HTTP/Lightpanda/Chromium distribution. The decision
rule in `docs/BENCHMARK.md` committed to measuring before building.

**Phase 0 run, 2026-04-10, n=500 pricing URLs from `seed/companies.csv`:**

```
total           500
reachable       158   (31.6%)
unreachable     342   (68.4%)

successes by tier:
  http            140   avg 765ms
  chromium         18   avg 2.93s  (3.83x slower than http)

failures by category:
  http_4xx        269   (53.8%)   ← dominant failure mode
  dns_failure      26   ( 5.2%)
  timeout          14   ( 2.8%)
  robots_blocked   10   ( 2.0%)
  tls_error         9   ( 1.8%)
  all_tiers_exhausted  6
  http_5xx          3
  parked_domain     2
  connection_refused 2
  spa_shell         1

chromium_escalation_rate = 18 / 158 = 11.4%
```

**Why skip Lightpanda despite 11.4% being in the judgment-call zone:**

1. **Rot dominates the signal.** 54% of pricing URLs are 4xx. The 2019
   seed data has restructured pricing paths. The actual tier decision
   sample (n=158) is too small to drive a Lightpanda build commitment
   — a single outlier moves the rate 0.6 percentage points. The 95% CI
   on 11.4% with n=158 is roughly [7%, 16%], which straddles the skip
   threshold.

2. **Total latency savings are small at this escalation rate.** Scaling
   to the full 7065 benchmark: ~254 chromium pages × 2.93s ≈ 12.4 min
   of chromium engine time. Lightpanda at ~500ms cuts that to ~2 min.
   With 15-worker concurrency, wall-clock savings are ~2-3 minutes on
   a ~15 minute benchmark. Material but not transformative.

3. **The real bottleneck is data quality, not engine speed.** Crawl
   mode (follow homepage → pricing link) could recover many of the
   269 http_4xx rows and dramatically improve reachable count. That's
   a much bigger leverage point than a 2-3 minute engine speedup.

4. **Lightpanda build cost (4-6 hours: binary management subcommand,
   subprocess lifecycle, per-tier proxy plumbing, engine pool
   recycling) is disproportionate to the measured savings.** The
   "premature optimization" warning applies directly.

5. **Skipping is reversible.** The Engine interface, router, stats
   collector, and tier-preference machinery are all in place. Adding
   Lightpanda later is a contained addition, not a refactor. Nothing
   about the current architecture forecloses the option.

**What would change our minds (trigger Lightpanda build):**

- Re-running Phase 0 after crawl mode lands shows the chromium
  escalation rate jumps above **15%** on a significantly larger
  reachable sample (n ≥ 500).
- OR a user-facing benchmark (full 7065 run) shows chromium wall
  clock > 25% of total run time.
- OR a production deployment hits a consistent >20% chromium rate
  on a non-rot-dominated dataset.

**Next steps:**

1. Build crawl mode (homepage → pricing link resolution).
2. Re-run 500-row Phase 0 with crawl enabled.
3. Append a follow-up entry to this decision with the new data.
4. If the rate is <15%, this decision stands and Lightpanda
   stays out of v1. If ≥15%, reopen.

**Authored during session:** 2026-04-10, Claude Code with Jeff.
**Commit reference for data:** `cf01017` + `/Users/jhoot/.trawl/jobs/phase0-500/`.

### Addendum — 2026-04-10: second data point from follow-link run

After building `--follow-link` (commit `ed9eac0`), re-ran the same 500-row
seed against the `homepage` column with CSS selector
`a[href*="pricing"], a[href*="/plans"], a[href*="/price"]`. The prefetch
routes through the full tier router, so SPA homepages can escalate to
chromium for nav DOM discovery.

```
Run B — homepage + follow-link + router-prefetch

total           500
reachable        91   (18.2%)
unreachable     409

successes by tier:
  http            87   avg 183ms
  chromium         4   avg 2.69s

failures by category:
  follow_failed   325   ← 65% of input has no <a href> match for
                          /pricing, /plans, or /price even when the
                          homepage renders successfully
  dns_failure      27
  timeout          14
  other            12   ← homepage 4xx that router propagated
  tls_error         9
  all_tiers_exhausted 8
  robots_blocked    7
  spa_shell         3
  parked_domain     2
  connection_refused 2

chromium_escalation_rate = 4 / 91 = 4.4%
```

**Both data points fall below the 15% reopen trigger:**
- Run A direct pricing_url: 11.4% on n=158
- Run B homepage + follow-link: 4.4% on n=91

The Lightpanda skip decision is reinforced. The follow-link run revealed
a distinct, larger problem: **pricing-page discovery from homepages is
~65% miss rate** with a simple href substring selector. That's a
product/data problem, not an engine problem — no middle tier would
change the numerator because the failures are structural (`<button>`
nav, client-side routing, non-matching terms like `/upgrade`).

**Next steps (not yet decided):**

1. Build a richer pricing-link selector library (10+ patterns including
   `/subscribe`, `/upgrade`, `/buy`, `a:contains("Pricing")`).
2. Implement hybrid discovery: try `pricing_url` from CSV, fall back to
   homepage+follow-link on 4xx.
3. Parse `/sitemap.xml` where available.
4. Accept the ~158 reachable pricing pages as the benchmark's baseline
   and document the 70% rot as a seed-quality finding rather than an
   engineering gap.

None of these open the Lightpanda question. This decision is closed
pending a meaningfully different reachable sample (n ≥ 500 with a rate
above 15%).

**Caveat the next session must know:** both measurements were taken on
the discoverable subset of pricing pages (~35% of input). The 65% that
missed are exactly the pages hidden behind interactive widgets,
`<button>`-only navigation, and JS-rendered pricing tables — which are
also the pages most likely to need a JS engine to render. The sample
is biased toward the easy half. **If discovery improves to >70% reach
AND re-measurement on the expanded population shows the escalation
rate climbing past 15%, the rule auto-reopens.** This isn't a flaw in
the decision (the rule was followed correctly given the data
available); it's a footnote that the "skip" verdict is conditional on
the measurable population at the time of measurement.

### Addendum — 2026-04-10: third data point from n=1000 hybrid run

After shipping hybrid discovery (commit `72a25b6` — `--fallback-column`
plus trigger-gated retry on http_4xx / dns_failure), re-ran Phase 0
against the first 1000 rows of `seed/companies.csv` with:

```
--url-column pricing_url
--fallback-column homepage
--fallback-selector 'a[href*="pricing"], a[href*="/plans"], a[href*="/price"]'
--tiers http,chromium --concurrency 15 --rate 1 --timeout 30s
```

```
Run C — hybrid discovery, n=999 (1 row had blank pricing_url)
wall clock        4m35s

total             999
reachable         355   (35.5%)
unreachable       644

successes by tier:
  http            305   avg 661ms
  chromium         50   avg 3.06s   (4.6x slower than http)

chromium_escalation_rate = 50 / 355 = 14.08%

fallback:
  attempted       579   ← rows whose primary failed with http_4xx or dns_failure
  succeeded        24   ← fallback path recovered them
  no_link         464   ← homepage fetched OK, selector matched nothing
  unreachable      91   ← homepage fetch itself failed

failures by category (after hybrid recovery applied):
  http_4xx        511   (down from ~535 pre-hybrid)
  dns_failure      44
  timeout          24
  tls_error        24
  robots_blocked   15
  all_tiers_exhausted 9
  http_5xx          6
  connection_refused 5
  spa_shell         4
  parked_domain     2
```

**Rule evaluation — does this reopen Lightpanda?** No, but it's the
closest call yet:

- Rate: 14.08% vs 15% threshold → **miss by 0.9 points**
- Sample: n=355 reachable vs n≥500 threshold → **miss by 145 rows**

Both thresholds fail. The rule stays formally closed.

**But the trend is the signal.** Across three data points, the
escalation rate has moved exactly as the caveat in the previous
addendum predicted:

```
Run A (n=500, pricing_url only):     11.4%  on n=158 reachable
Run B (n=500, homepage+follow):       4.4%  on n=91 reachable
Run C (n=999, hybrid):               14.08% on n=355 reachable
```

As the measurable population expanded, the escalation rate climbed
toward the threshold. Run B looks like an outlier because it was
follow-only — which selected for the easy homepages without trying
the direct pricing URLs — so the population was the JS-light subset.
Run C's hybrid combines both paths and is the most representative
measurement so far.

**Scaling comparison — is hybrid actually helping?** Yes, modestly:

- Run A scaled linearly to n=1000: ~316 reachable
- Run C hybrid actual: 355 reachable
- **Net: +39 rows, +12% reach improvement** attributable to hybrid

But the fallback path's internal yield is poor: **24 successes out of
579 attempts (4.1%)**. The dominant failure mode is `no_link` at 464
— the homepage loaded fine, the 3-pattern selector just didn't match.
This is exactly the "65% pricing-page discovery miss rate" from the
earlier addendum, re-confirmed on a larger sample. The hybrid machinery
is working; the selector library is the bottleneck it's waiting on.

**Next steps (decided):**

1. Build the richer pricing-link selector library (priority #3 in
   CLAUDE.md). Target the 464 no_link rows with patterns like
   `/subscribe`, `/upgrade`, `/buy`, footer selectors, and text-based
   matches. Ordering matters — cheap href substring matches first,
   expensive text-content matches second.
2. Re-run Phase 0 with the new selector set against the same
   `/tmp/seed-1000.csv` so Run D is apples-to-apples with Run C.
3. If Run D pushes reach past n=500 AND chromium rate past 15%, the
   Lightpanda rule **auto-reopens** on its own terms. If reach crosses
   but rate doesn't, the skip decision is reinforced with real data.

**Sitemap parsing (priority #2) is deferred to after Run D** because
the hybrid pipeline we already built is sitting idle on 464 homepages
waiting for better selectors. Highest marginal value is unlocking
that existing infrastructure, not building new discovery paths.

**Authored during session:** 2026-04-10.
**Commit reference for data:** `72a25b6` + `~/.trawl/jobs/phase0-1000-hybrid/`.

### Addendum — 2026-04-12: Run D closes the Lightpanda question with n≥500

Re-ran Phase 0 with two discovery improvements, targeting the n≥500
reachable threshold that Run C missed by 145 rows:

1. **Expanded fallback selector** — 18 patterns (vs Run C's 3):
   pricing, /plans, /price, /subscribe, /subscription, /upgrade,
   /buy, /packages, /billing, /pro, /premium, /enterprise,
   /features, /cost, /rates, /tiers, /get-started, /signup, /order.
2. **Map-based discovery** — for rows where even the expanded
   selector failed, `trawl map --depth 1` crawled 649 homepages
   (HTTP-only BFS), discovered 7803 URLs, filtered 74 pricing-like
   URLs not already fetched, and batched them through the tier
   cascade.

Same first 1000 rows as Run C. `--browser-like --no-tier-learning
--ignore-robots --tiers http,chromium`. Script at
`bench/phase0/run.sh`.

```
Run D — expanded selectors + map discovery, n=1000 input

total             1073  (999 primary + 74 map-discovered)
reachable          579  (53.9%)
unreachable        494

successes by tier:
  http             518   avg ~700ms
  chromium          61   avg ~3.0s

chromium_escalation_rate = 61 / 579 = 10.54%

failures by category:
  http_4xx         367
  dns_failure       44
  timeout           24
  tls_error         23
  spa_shell         12
  all_tiers_exhausted 8
  http_5xx           6
  connection_refused  5
  parked_domain      2
```

**Rule evaluation:**

| Threshold | Required | Run D | Verdict |
|-----------|----------|-------|---------|
| n ≥ 500 reachable | ≥ 500 | **579** | **PASS** (first time) |
| Escalation rate ≥ 15% | ≥ 15% | **10.54%** | FAIL |

**The Lightpanda question is now closed with proper statistical
power.** Run D is the first measurement that satisfies the n≥500
sample size requirement, and the rate moved *away* from the
threshold, not toward it.

**Trend across all four runs:**

```
Run A (pricing_url only):     11.4%  on n=158
Run B (homepage+follow):       4.4%  on n=91  (outlier — easy subset)
Run C (hybrid, 3 selectors):  14.08% on n=355
Run D (hybrid, 18 selectors): 10.54% on n=579
```

Run C's 14.08% was elevated by a small-sample effect: the 355
reachable pages were disproportionately the hard-to-render subset
because the 3-pattern selector missed most of the easy pages with
non-standard pricing paths. Run D's expanded selectors recovered
224 more pages, and **213 of 224 were HTTP-tier** — exactly the
easy pages the caveat warned about. Expanding discovery diluted
the chromium share rather than inflating it.

**The Run C caveat resolved in the opposite direction of what was
feared.** The caveat said "the 65% that missed are likely the hard
pages that need chromium." In reality, the missed pages were
overwhelmingly easy (HTTP-served, static pricing pages with
non-standard URL paths like /subscribe, /pro, /get-started). The
hard pages were already in the measurable population.

**This decision is now durable.** The "auto-reopen" condition
(>70% reach AND ≥15% rate) is no longer plausible given that
reach improved from 35.5% → 53.9% while the rate dropped from
14.08% → 10.54%. Further discovery improvements would recover
even more HTTP-easy pages, pushing the rate further below 15%.

**What would still reopen Lightpanda:**

- A production workload (not the seed dataset) showing >20%
  chromium escalation on n≥500, where the chromium wall clock
  is a material fraction of total run time.
- A user-facing request with latency evidence.

Neither of these exists today. Lightpanda is out of scope for v1.

**Authored during session:** 2026-04-12.
**Benchmark script:** `bench/phase0/run.sh`.
**Result artifacts:** `bench/phase0/results-20260412-213849/`.
