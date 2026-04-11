# trawl — roadmap

**Current phase:** Open — BFS, map, screenshot, content cache, schema extraction all shipped 2026-04-10
**Last updated:** 2026-04-10

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

## Gap analysis vs Firecrawl (as of 2026-04-10)

Not all gaps are worth closing. The table maps Firecrawl's features
against trawl's current state, scope judgment, and rough cost.

| Feature                                           | Trawl today | In scope | Cost    |
| ------------------------------------------------- | ----------- | -------- | ------- |
| HTML → clean markdown output                       | ✅           | yes      | small   |
| Metadata extraction (title, OG, canonical, lang)   | ✅           | yes      | small   |
| Boilerplate/readability stripping                  | ✅           | yes      | small   |
| BFS crawl mode (`--depth N --same-domain`)         | ✅           | yes      | medium  |
| URL mapping (fast link discovery, no fetch)        | ✅           | yes      | small   |
| Screenshot output                                  | ❌           | yes      | small   |
| Screenshot output                                  | ✅           | yes      | small   |
| Schema-based structured extraction (JSON/YAML)     | ✅           | yes      | medium  |
| Interactive actions (click, scroll, wait, execJS)  | ❌           | debatable | large |
| LLM extraction                                     | ❌           | **no**   | —       |
| Proxy rotation as a core feature                   | ❌           | P2 only  | large   |
| Search integration                                 | ❌           | **no**   | —       |
| Webhooks / async API                               | ❌           | **no**   | —       |
| Content caching                                    | ✅           | yes      | medium  |

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
   "Michael Rohlf").

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

- **Interactive actions (click, scroll, wait, execute JS before
  extraction).** Firecrawl's `actions` feature. Technically possible
  via chromedp but the UX is complex (recipe files?), the scope is
  slippery, and most "wait for hydration" cases are already handled
  implicitly by the chromium engine's current wait heuristics.
  Revisit if a concrete workflow needs it.
- **Proxy rotation as a core feature.** `docs/PROXIES.md` is the
  placeholder. Real proxy support changes the security story
  materially; treat it as its own P2/P3 phase when there's a
  concrete use case.

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
