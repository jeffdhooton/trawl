# trawl — roadmap

**Current phase:** Open — **v0.6.0 shipped 2026-04-13**. Proxy hardening: `--rotate-on-status` (retry through different proxy on block codes) and `trawl proxy-test` (pre-run proxy validation). Next targets are consumer-driven.
**Last updated:** 2026-04-13

This doc is the single source of truth for "what's next and why." The
decision log in `docs/DECISIONS.md` captures one-off architectural
calls; this file captures direction across phases. CLAUDE.md points
here instead of maintaining its own priority list.

---

## Positioning: what trawl is, and isn't

Trawl is a **general-purpose tiered web scraping CLI**. The thesis is
that most URLs can be served by a cheap HTTP fetch, a minority need a
full browser, and the tool should figure out which is which — once —
and remember. Trawl's differentiators:

- **Single static binary.** No CGO, no runtime daemon, no Docker
  required. `go install` or a copied binary is the whole deploy.
- **No API key, no service dependency.** Everything runs locally.
- **Persistent, resumable frontier.** Long crawls survive SIGINT and
  machine restarts. BadgerDB under the hood.
- **Intelligent tiered routing with cross-job learning.** The router
  remembers which engine served each host last and reorders the
  ladder on subsequent fetches — shipped 2026-04-10.
- **Polite by default.** robots.txt, per-domain rate limits, and
  concurrency caps are opt-out, not opt-in.
- **JSONL output.** Composes with grep, jq, and the rest of Unix
  without custom parsers.

Trawl is **not** a managed service, an LLM framework, an anti-bot
proxy rotation toolkit, or a distributed crawler. SPEC §13 lists the
hard exclusions. The comparable project in the SaaS world is
Firecrawl; trawl overlaps with its "clean content from a URL" use
case but has no intention of matching its LLM/search/webhook
surface area.

**Hard scope reminder:** the 7000-company seed CSV is a **test
dataset** for measuring engine efficacy, not product data.
Domain-specific heuristics (pricing-aware selectors, SaaS-shaped
logic, anything that treats the seed as real input) belong in the
caller, not trawl. If a proposed feature couldn't be justified to a
non-SaaS user scraping something else, it doesn't belong here.

---

## Gap analysis vs Firecrawl (as of 2026-04-12)

Not all gaps are worth closing. The table maps Firecrawl's features
against trawl's current state, scope judgment, and rough cost.

| Feature                                           | Trawl today | In scope | Cost    |
| ------------------------------------------------- | ----------- | -------- | ------- |
| HTML → clean markdown output                       | ✅           | yes      | shipped |
| Metadata extraction (title, OG, canonical, lang)   | ✅           | yes      | shipped |
| Boilerplate/readability stripping                  | ✅           | yes      | shipped |
| BFS crawl mode (`--depth N --same-domain`)         | ✅           | yes      | shipped |
| URL mapping (fast link discovery, no fetch)        | ✅           | yes      | shipped |
| Screenshot output                                  | ✅           | yes      | shipped |
| Schema-based structured extraction (v1 + v2)      | ✅           | yes      | shipped |
| CSV / TSV output                                   | ✅           | yes      | shipped |
| HTTP retries with backoff                          | ✅           | yes      | shipped |
| Per-host politeness overrides                      | ✅           | yes      | shipped |
| Interactive actions (click, scroll, wait, execJS)  | ✅           | yes      | shipped |
| JSON body output (`--format json`)                 | ✅           | yes      | shipped |
| Content caching                                    | ✅           | yes      | shipped |
| LLM extraction                                     | ❌           | **no**   | —       |
| Proxy support (single + rotating pool)              | ✅           | yes      | shipped |
| Search integration                                 | ❌           | **no**   | —       |
| Webhooks / async API                               | ❌           | **no**   | —       |

**What trawl has that Firecrawl doesn't:** single static binary,
no API key, persistent resumable frontier, tiered routing with
learning, JSONL composability, robots-polite-by-default. These are
real differentiators — do not throw them away chasing parity.

---

## Phasing

Phases are ordered by marginal value on already-built infrastructure,
not by feature glamour. Each phase should feel like it unlocks a new
use case, not like it sprinkles polish.

### Phase: content extraction — SHIPPED 2026-04-10

**What landed:**
1. HTML → clean markdown via `github.com/JohannesKaufmann/html-to-markdown`
   (pure-Go). New `--format html|markdown` flag on scrape/batch, default
   empty (no body emitted) so existing JSONL consumers stay
   backward-compatible.
2. Automatic page metadata on every record via hand-rolled goquery
   extractor: title, description, canonical URL, language, Open Graph,
   Twitter cards, JSON-LD structured data, published_at with a chain
   of timestamp formats. Lives at `metadata.page`. On by default;
   `--no-metadata` escape hatch for the rare opt-out.
3. Boilerplate removal via `codeberg.org/readeck/go-readability/v2`
   (pure-Go, actively maintained fork of the deprecated go-shiori
   original). `--readability` flag strips nav/footer/ads before CSS
   extraction and markdown conversion. Falls back to raw body on
   failure — never hard errors.

Metadata extraction runs on the ORIGINAL body (pre-readability) so
og:*, canonical, and JSON-LD in <head> survive even when readability
strips everything else.

Smoke-tested against linear.app — returns clean markdown with all
metadata populated, including og:image, twitter cards, and lang attr.

### Phase: BFS crawl mode — SHIPPED 2026-04-10

**What landed:**
1. New `trawl crawl <seed-url>` command with `--depth N`,
   `--same-domain` (default true), and `--limit N` (default 1000,
   0 = unlimited). Inherits every content/routing flag from
   `scrape`/`batch` (`--format`, `--readability`, `--selector`,
   `--tiers`, `--concurrency`, `--rate`, etc.), so a crawl composes
   trivially with the content-extraction phase to become "give me
   clean markdown from an entire site."
2. Frontier gained a `Depth` field, `EnqueueWithDepth`, and a
   `BlockingNext(ctx)` claim path backed by a `sync.Cond`. Workers
   block on an empty queue instead of exiting; the frontier signals
   `ErrEmpty` only when the queue is empty AND no worker is still
   in-flight (quiescence detection), so no extra termination
   protocol is needed at the job layer.
3. Link discovery runs on the raw engine body after a successful
   fetch. `extract.AllLinks` returns every `<a href>` resolved
   against the final URL, applies same-domain filtering, strips
   fragments, and dedupes. Failed fetches skip discovery — no body,
   no links.
4. `--limit` caps total URLs **enqueued** (seed + children), not
   successful fetches. Budget is tracked via an atomic counter shared
   across workers; dedup-rejected children release their slot so a
   cycle-heavy graph doesn't burn the budget on duplicates.

Resume works unchanged: the crawl config is persisted in `config.json`,
and on `trawl resume <job>` the seed is already in the frontier so the
workers just drain whatever remained queued when the previous run
stopped. The `enqueued` counter is re-seeded from `frontier.Stats().Total`
on resume so the `--limit` cap still holds across restarts.

### Phase: URL mapping — SHIPPED 2026-04-10

**What landed:**
1. New `trawl map <seed-url>` command. Plain-text URL list to
   stdout, one per line — drop-in substitute for `trawl sitemap`
   in existing pipelines, plus HTML link discovery for sites that
   don't publish a sitemap.
2. Two combined sources via `--sources sitemap|crawl|both`
   (default both). Sitemap URLs land first so they win first-seen
   dedup priority; crawl fills the gaps. Sitemap failures are
   non-fatal in "both" mode — the crawl source still runs.
3. Lightweight in-memory BFS crawler in `cmd/trawl/map.go`: HTTP
   tier only (no chromium escalation), no frontier DB, no routing
   ladder, no record writes. Just `engine.HTTP` + politeness gate +
   `extract.AllLinks` + a sync.Cond-based queue. Depth cap skips
   the fetch entirely at leaves so leaf pages don't cost an HTTP
   round-trip.
4. Flags: `--depth N` (default 2), `--same-domain` (default true),
   `--limit N` (default 10000, 0 = unlimited), `--timeout`,
   `--concurrency`, `--rate`, `--ignore-robots`, `--verbose`,
   `-o/--output`. The `--limit` cap is applied uniformly across
   both sources via a shared emit callback.

**Relationship to `trawl sitemap`:** sitemap stays as the focused
"just sitemaps" tool for users who don't want the crawl cost.
`trawl map` is the broader "every URL I can find" tool. Both share
`internal/sitemap`. Users who need SPA nav discovery should reach
for `trawl crawl` instead — map is deliberately single-tier.

### Phase: screenshot output — SHIPPED 2026-04-10

**What landed:**
1. `engine.Request.WantScreenshot` opts in per-fetch; the chromium
   engine captures a full-page PNG via `page.CaptureScreenshot` with
   `captureBeyondViewport=true`. (Note: `chromedp.FullScreenshot`
   hardcodes JPEG via its quality arg — we call the cdproto action
   directly to get PNG.) The HTTP engine ignores the flag and leaves
   `Result.Screenshot` nil, so there's zero cost on the happy path.
2. `--screenshot-dir <dir>` flag on scrape/batch/crawl. When set, a
   successful fetch whose engine returned PNG bytes writes them to
   `<dir>/<sha256-of-canonical-url>.png`. Deterministic filenames so
   re-runs overwrite rather than accumulate duplicates.
3. `metadata.screenshot_path` stamped on the output record when a
   screenshot was written. Absent on HTTP-served rows, so JSONL
   consumers can cheaply `jq 'select(.metadata.screenshot_path)'`.
4. I/O errors on the write are logged and dropped — screenshots are
   best-effort, they must never fail the row.

### Phase: content caching — SHIPPED 2026-04-10

**What landed:**
1. New `internal/cache` package: BadgerDB-backed persistent cache
   keyed by `(canonical URL, tier name)`. Value is a serialized
   `engine.Result` — body, headers, status, content-type, duration,
   redirects — so a cache hit reconstructs the exact record shape a
   live fetch would produce. TTL applied on Get (0 = never expire).
   NopCache fallback when disabled or on open failure.
2. Router integration: `Router.WithContentCache(cache.Cache)`
   attaches a cache. Inside the tier loop the router checks the
   cache BEFORE each engine's Fetch. On hit the cached body still
   runs through validity, so a stale stub gets escalated past
   exactly as if the live fetch had returned it. Cache puts only
   happen on successful LIVE fetches — cache-replay successes are
   not re-put.
3. Opt-in via `--cache` on scrape/batch/crawl. Default TTL 24h via
   `--cache-ttl` (0 = never expire). Custom path via `--cache-path`,
   default `$TRAWL_HOME/content-cache`. Map intentionally does NOT
   expose cache flags — it's already designed to be ephemeral and
   stdout-first.
4. `metadata.from_cache: true` stamped on records served from the
   cache. Downstream consumers can distinguish live-fetch rows from
   replays with `jq 'select(.metadata.from_cache)'`.
5. Cache puts do NOT update the tier-learning cache (tierlearn).
   The learning signal is "what served this host LIVE," and a cache
   replay isn't new information — we don't want one successful
   cache hit to lock a host's preference in place.

**Why opt-in:** default behavior stays "fresh fetch every time" so
a routine `trawl scrape` never surprises a user with stale data
from last week. Iterating-on-selectors is the explicit workflow;
users opt in via `--cache` when they want it.

### Phase: schema-based structured extraction — SHIPPED 2026-04-10

**Unblocking consumer:** Stanford Encyclopedia of Philosophy articles.
Jeff's partner was running Firecrawl against plato.stanford.edu/entries/
pages to pull structured data (title, pubinfo, TOC, related entries,
author, copyright) out of the SEP DOM. This gave us a concrete,
complex-enough schema shape to design against — flat fields, arrays of
objects with attribute extraction, and a real self-reference case.

**What landed:**
1. New `internal/schema` package: `Schema` (version + fields map),
   `Field` (selector, attr, multiple, nested fields). `Load(path)`
   parses YAML (via `gopkg.in/yaml.v3`) or JSON by extension and
   validates: version must be 1, top-level selectors non-empty,
   unknown fields rejected (catches typos). `Extract(body, base, s)`
   walks the schema with goquery and returns `map[string]any`.
2. **Empty selector = "self"** inside a nested `fields` context —
   required for the "array of objects where each object is the
   iterated element's text and an attribute" case. The SEP TOC and
   related-entries extractions both need this. Top-level empty
   selectors are rejected at Load time so the ambiguity can't leak in.
3. **Missing matches are omitted from output**, not present as `""`
   or `null`. Consumers `jq 'select(.extracted.foo)'` to filter on
   presence.
4. **Relative URLs stay relative** (e.g. `../aesthetics/`). Trawl
   does not resolve against the base URL — consumers join against
   `record.canonical_url` if they need absolute form. Decision: this
   lets SEP consumers cheaply distinguish intra-encyclopedia links
   from external ones.
5. `--schema <path>` flag on scrape/batch/crawl. Schema is loaded
   once at command start (bad schema fails loudly with a parse error)
   and merged into `record.extracted` alongside any `--selector`
   flat extractions. Schema keys win on collision — schema is more
   specific than ad-hoc CLI selectors.
6. Example schema at `docs/examples/sep-article.yaml`, verified
   against a real plato.stanford.edu/entries/kant/ fetch during
   development (11 TOC entries, 24 related entries, author
   "Michael Rohlf"). **Validated against the full 1857-entry SEP
   corpus on 2026-04-11** — 100% reach, 0 failures, 2h34m wall clock,
   0 chromium escalations. See the v2 schema features bucket below
   for the one real gap the production run surfaced.

**What v1 does NOT do** (explicit non-goals, easy to extend when a
real consumer asks):
- No transforms (trim, lowercase, regex). Users post-process in jq.
- No type coercion (numbers stay strings).
- No `required` / `optional` validation — missing fields are silently
  omitted.
- No sub-schema reuse via reference / include.
- No conditional logic or index modifiers beyond `multiple`.

The `version: 1` field is mandatory so a future breaking change can
ship without inventing a second schema format.

#### v2 schema features (pending consumer signal)

The SEP production run on 2026-04-11 surfaced exactly one real gap:
authors in older/simpler entries are rendered as plain text rather
than `<a href="http://...">`, so the `author` selector misses ~34%
of the corpus (~635 of 1857 entries). The consumer backfilled offline
with a regex script against the markdown body. That workaround is
fine for one consumer but re-discovering it cold is a papercut.

Two v2 schema extensions would eliminate the need:

1. **Fallback selectors.** `selector: ["#primary", "#fallback"]`
   returns the first selector that matches, or a list of selectors
   with a declared primary and fallback shape. Maps directly to the
   SEP case: primary = link-wrapped author, fallback = plain-text
   after `<br/>`.
2. **Transform step.** A post-extraction regex/trim/lowercase/split
   pipeline on the extracted value. Would let a consumer express
   "extract author from the copyright text with regex `by\s+(.+?)<`"
   without a second script.

Both are deferred until a SECOND consumer hits a similar gap — the
SEP case alone is solvable with offline post-processing and the
"ship when someone actually has the shape" rule that unblocked
schema v1 still applies.

### Phase: CSV output — SHIPPED 2026-04-10

Originally off-roadmap; `internal/output/output.go` carried a comment
noting "P0 ships JSONL; CSV/Parquet/SQLite land in P1/P2." Landed as
part of a three-feature batch (CSV, HTTP retries, per-host politeness)
because all three were small, complementary quality-of-life wins.

**What landed:**
1. New `internal/output/csv.go` implementing the `Sink` interface.
   Streaming writes, one CSV row per record. `.tsv` paths get tab
   delimiters via path sniffing so users can change the separator
   by renaming the output file.
2. `output.NewFile(path, columns)` dispatcher picks CSV or JSONL by
   extension. `.csv`/`.tsv` → CSV, everything else (and stdout) →
   JSONL. Dead-letter queue stays JSONL regardless of the primary
   sink format because benchmark scripts depend on its shape.
3. **Column resolution**: explicit `--csv-columns col1,col2.nested`
   wins verbatim. When unset, a base set (`url, canonical_url, tier,
   status_code, duration_ms, content_type, failure_category, error`)
   is augmented with every top-level key from the first record's
   `extracted` map. Column set LOCKS at the first write — records
   that later introduce new `extracted` keys have those keys
   silently dropped (with a debug log via `DroppedKeys()`).
4. **Flattening** via JSON round-trip: each record is marshaled to
   `map[string]any` then walked with dot-paths. Non-scalar values
   (arrays, maps from schemas) are JSON-encoded inline so CSV cells
   stay single-valued — ugly but honest about CSV's limitations.
5. `--csv-columns` on scrape/batch/crawl. Validation error if set
   when the output path is not `.csv`/`.tsv`.

### Phase: HTTP retries with backoff — SHIPPED 2026-04-10

Off-roadmap before this batch. Classic papercut for long batches: a
transient 503 or connection reset cost the whole row because the
HTTP tier only tried once. Now retries happen inside the engine.

**What landed:**
1. `HTTPConfig` gained `MaxRetries` (default 2 = 3 total attempts)
   and `RetryBaseDelay` (default 500ms). `HTTP.Fetch` wraps a
   refactored `fetchOnce` in a retry loop.
2. **Retry classification** (`isRetryableError` / `isRetryableStatus`):
   network transients retry (timeouts, ECONNRESET, ECONNREFUSED,
   ENETUNREACH, EHOSTUNREACH, io.EOF on Client.Do, net.ErrClosed,
   any wrapped `*net.OpError`). HTTP status codes 429, 502, 503, 504
   retry. Permanent errors return immediately: context cancel/deadline,
   TLS certificate failures (`*tls.CertificateVerificationError`,
   `x509.UnknownAuthorityError`, `x509.HostnameError`), plus any 4xx
   except 429.
3. **Backoff**: exponential with ±25% jitter, capped at 10 seconds
   INCLUDING jitter (so "max 10s" means observed delay is never
   more than 10s). Each attempt checks `ctx.Err()` before running;
   each backoff sleep checks whether it would overshoot the
   caller's deadline and bails out early if so.
4. **Stub bodies are NOT retried** — only network and status-code
   transients. Stub-body escalation stays with the router's
   validity → next-tier path (the design call from the proposal).
5. `--retries N` (default 2) and `--retry-delay` (default 500ms)
   on scrape/batch/crawl. Chromium doesn't get retries in v1 —
   chromedp's timeout model is different and chromium fetches
   fail less frequently from network transients.

### Phase: Tier 1 + Tier 2 evasion — SHIPPED 2026-04-11

Built speculatively (without a consumer ask) on the principle
that the next hostile target should hit a tool that's already
ready. The full design + decision rules live in `docs/EVASION.md`;
see §5.1 / §5.2 SHIPPED subsections for implementation deviations.

**What landed:**
1. Four new flags wired across scrape/batch/crawl/map via the
   shared `cmd/trawl/evasion.go` helper:
   - `--browser-like` enables Tier 1 (rotating UA, full Chrome
     header set, in-memory cookie jar, ±20% jitter on the rate
     limiter).
   - `--user-agent <strategy>` overrides UA picking:
     `declared` (default trawl/<ver>), `rotating`, or
     `fixed:<string>`.
   - `--stealth` enables Tier 2: chromium injects an init script
     before navigation that patches `navigator.webdriver`,
     `navigator.plugins`, `navigator.languages`, the WebGL
     vendor/renderer strings, and the `Permissions.query`
     notification shim.
   - `--no-jitter` escape hatch for deterministic pacing even
     when `--browser-like` is on (reproducible benchmarks).
2. Cookie jar is **in-memory per job** — `net/http/cookiejar.New`
   on the shared http.Client, lifetime is the command run, no
   `$TRAWL_HOME` files. Resolves the EVASION.md §9 question in
   favor of the simpler option.
3. UA picker (`internal/engine/useragent.go`) is **sticky per
   host** with a ~10-entry pool of recent Chromium-family browser
   UAs. The matching `Sec-CH-UA{,-Mobile,-Platform}` headers are
   bundled with each pool entry so the emitted header block is
   internally consistent. Firefox/Safari are deliberately
   excluded from the pool because their client-hint headers
   differ.
4. Jitter lives in `politeness.Gate.Acquire`. When
   `Config.JitterFraction > 0`, after the rate limiter grants a
   token the gate sleeps for a uniformly-random delay in
   `[0, fraction × baseInterval]`. Context-aware: a tight ctx
   deadline cuts through. New `HostRule.Jitter` field lets
   `--politeness` YAML pin individual hosts to non-default
   jitter.
5. Stealth script (`internal/engine/stealth.js`) is
   **maintained in-tree**, embedded via `//go:embed`, and
   prepended to chromium's action list as a
   `page.AddScriptToEvaluateOnNewDocument` call before
   `chromedp.Navigate`. Single static binary deploy story holds.
6. Per-record audit trail: `metadata.evasion =
   {browser_like, stealth, user_agent, jitter_ms}` populated
   when any evasion was active, fully omitted otherwise. Engine
   stamps `Result.Evasion`; cmd-layer combines with `Acquire`'s
   `jitterMS` return in `cmd/trawl/scrape.go:stampEvasion`.
   Default-mode JSONL output is byte-identical to pre-evasion.
7. End-to-end test in `internal/engine/chromium_test.go`
   (`TestChromiumStealthHidesWebdriver`) proves the script
   actually patches `navigator.webdriver` from `true` →
   `undefined` against an httptest server. Plus 7 other unit
   tests covering the picker, the browser-like header set,
   ExtraHeaders override precedence, cookie jar replay, jitter
   add/zero/ctx semantics, and per-host jitter overrides.

**Decision rules waived:** EVASION.md §5.1 and §5.2 specify
"ship when a consumer reports failure mode X." Both were
waived for this PR on the principled-readiness argument. The
§5.3 (Tier 3 / uTLS) and §5.4 (Tier 4 / proxies) decision
rules **remain in force** — those have real maintenance costs
and deserve evidence.

### Phase: Per-host politeness overrides — SHIPPED 2026-04-10

Off-roadmap before this batch. Global rate/concurrency worked fine
for uniform crawls but couldn't express "slow-crawl SEP to one
request every 2s but hammer internal APIs at 10/s in the same job."

**What landed:**
1. New `internal/politeness/hostrules.go`: `HostRules` (version +
   hosts list), `HostRule` (match, rate, concurrency). `LoadHostRules`
   parses strict YAML (`KnownFields=true` so typos fail loudly).
   Validation: version must be 1, at least one rule, non-empty match,
   at least one of rate/concurrency set per rule.
2. **Match syntax**: exact host (`example.com`) OR suffix wildcard
   (`*.gov`, matches subdomains but not the bare TLD — `*.gov` does
   NOT match "gov"). Case-insensitive. First rule wins, so specific
   rules go before catch-alls.
3. `Gate.WithHostRules` attaches rules; `stateFor(host)` consults
   `effectiveHostConfig` before building a domainState. Per-host
   rate and per-host concurrency both supported; burst stays global
   for v1.
4. `--politeness <file>` on scrape/batch/crawl/map. Map is included
   because polite slow-crawling of a gentle host matters more than
   map's ephemerality. File is loaded once at command start.
5. Example at `docs/examples/politeness.yaml` showing SEP and `*.gov`
   slow-crawl rules.

### Phase: Proxy support — SHIPPED 2026-04-12

**What landed:**
1. `--proxy <url>` flag on scrape/batch/crawl/map. Routes all HTTP
   and chromium requests through a single gateway proxy. HTTPS
   targets use CONNECT tunneling automatically.
2. `--proxy-file <path>` flag for a pool of proxies (one URL per
   line, blank lines and `#` comments skipped). Per-domain-sticky
   rotation: each target domain is hashed to a fixed pool index so
   the same domain always exits through the same proxy IP. Thread-safe.
3. Chromium tier receives the proxy via `chromedp.ProxyServer` launch
   flag. For `--proxy-file`, chromium is pinned to the first proxy
   in the pool (per-domain rotation requires recycling the browser
   allocator, which is too expensive for v1).
4. uTLS h1 fallback transport respects the proxy. h2 transport has
   no proxy support (documented limitation — falls back to h1 through
   the proxy automatically).
5. `metadata.evasion.proxy: true` stamped on every proxied record.
6. Proxy config persisted in JobConfig for resume. `--proxy` and
   `--proxy-file` are mutually exclusive (error if both set).
7. See `docs/PROXIES.md` for the full design doc including provider
   recommendations, rotation strategies, and operational gotchas.

**What was deferred (future P2):**
- `{{session}}` template substitution for provider-specific rotation
- `proxy:` YAML config block with per-tier overrides
- BadgerDB session persistence for cross-run sticky sessions
- SOCKS proxy support

### Phase: Proxy hardening — SHIPPED 2026-04-13

Two features that close the biggest operational gaps in the v0.5.0
proxy support: automatic retry through a different proxy when a
target returns a block code, and a pre-run validation subcommand.

**What landed:**

1. **`--rotate-on-status <codes>`** flag on scrape/batch/crawl/map.
   Comma-separated HTTP status codes (e.g. `403,429,503`) that
   trigger proxy rotation and same-tier retry when `--proxy-file`
   is active. `--rotate-retries N` (default 2) caps how many proxy
   swaps are attempted per tier before proceeding.

   - Only fires with `--proxy-file` (pool mode). Single `--proxy`
     has nothing to rotate to — the flag is accepted but is a no-op.
   - After rotation retries exhaust, the router forces
     `Escalate=true` so the next tier still gets a chance. This is
     critical for 403 which is normally non-escalatable: without
     forced escalation, a proxy-blocked 403 would kill the row
     even though chromium (which may use a different proxy) might
     succeed.
   - Each proxy rotation is logged at debug level
     (`rotating proxy and retrying`) with the tier, host, status
     code, and attempt counter. The output record's `Attempt` list
     shows one entry per tier (not per rotation), keeping JSONL
     consumers simple.
   - Both flags are persisted in `config.json` (`rotate_on_status`,
     `rotate_retries`) so resumed jobs keep the same behavior.

2. **`trawl proxy-test`** subcommand. Validates proxy connectivity
   before a long run.

   ```bash
   trawl proxy-test --proxy http://user:pass@gate.proxy.com:7000
   trawl proxy-test --proxy-file proxies.txt
   ```

   Steps:
   - Fetches your direct IP via an echo service (default
     `httpbin.org/ip`, override with `--echo-url`).
   - Tests each proxy: connects through it, verifies the exit IP
     differs from your direct IP, reports latency.
   - Warns when a proxy's exit IP matches your direct IP (common
     with local/misconfigured proxies).
   - Emits structured JSON to stdout for scripting, human-readable
     progress to stderr.
   - `--timeout` (default 15s) caps each individual test request.
   - Supports both `--proxy` (single) and `--proxy-file` (pool).
   - Password-redacted proxy labels in output (`user:***@host`).

**Design decisions:**

- **`ProxyRotator` interface** added to `internal/router`. The
  router calls `Rotate(domain)` on the configured rotator when a
  rotate-worthy status is seen. The `domainStickyRotator` in
  `cmd/trawl/proxy.go` implements it by incrementing the pool
  index for that domain (modular wrap). Single-proxy mode returns
  `false` from `Rotate`, signaling the router to stop retrying.
- **Forced escalation after rotation exhaustion.** A 403 through
  every proxy in the pool doesn't mean "stop" — it means "this
  tier can't get through, try the next." The router overrides the
  validity checker's `Escalate=false` for rotate-worthy codes
  when all retries are spent. This is the one place the rotation
  logic intentionally overrides validity semantics.

---

## Explicitly deferred or out of scope

These get written down so future sessions (including agent sessions)
don't burn cycles relitigating them.

### Not in scope — ever

- **LLM-based extraction.** Contradicts "no runtime deps" and the
  single-binary deploy story. The Unix answer is "pipe trawl's
  markdown output into a separate LLM tool." Firecrawl's LLM extract
  is the feature they monetize; trawl doesn't need to be that.
- **Search integration.** Not a scraper's job. Use a search API and
  pipe URLs into `trawl batch`.
- **Webhooks / async API.** SPEC §13 excludes the daemon. Long crawls
  use the persistent frontier + resume workflow.
- **Web UI.** SPEC §13.
- **Scheduler.** SPEC §13. Use cron or systemd timers on top of
  `trawl batch`.
- **Plugin DSL.** SPEC §13. Call trawl as a library from Go code if
  you need programmatic control.
- **Distributed mode.** SPEC §13. Run multiple trawl processes behind
  a shared queue if you need to scale horizontally.

### Deferred until a concrete need arises

- **HTTP/2 SETTINGS frame forging.** Go's `x/net/http2` sends its
  own SETTINGS values (INITIAL_WINDOW_SIZE, MAX_CONCURRENT_STREAMS,
  etc.) which differ from Chrome's. Matters only for detectors that
  combine TLS + SETTINGS (Akamai, Cloudflare aggressive). See
  EVASION.md §8.3.

### Not a trawl concern at all

- **Pricing-specific selector libraries.** Domain logic belongs in
  the caller. Trawl accepts arbitrary CSS via `--fallback-selector`;
  whoever runs the benchmark harness decides what to pass. See the
  Run C data in `docs/DECISIONS.md` for why this matters (the
  selector-quality finding was misread as a trawl concern in one
  early session and had to be rescoped out).

---

## Review cadence

Update this file when:

1. A phase ships — mark it done, move to next.
2. A new priority emerges from benchmark data — add it to the phase
   list with the data that drove the decision.
3. The positioning changes — rare, but if trawl's thesis shifts
   (e.g. distributed mode becomes in scope), rewrite the top section.

Stale roadmaps are worse than no roadmap. If this file disagrees with
what's actually being built, fix the file.
