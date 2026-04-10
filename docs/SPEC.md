# Trawl — Intelligent Tiered Web Scraping Tool

**Status:** Spec / PRD draft
**Audience:** Fresh Claude Code instance building this from scratch
**Working name:** `trawl` (alternatives: `forage`, `harvest`, `dredge`, `gather`. Pick one and commit before writing code.)

---

## 1. Pitch

Most scraping tools force you to pick one engine — `requests`, Playwright, Scrapy — and live with its tradeoffs. The result: you either burn 100x the time running headless Chromium on pages that are plain HTML, or you ship a fast HTTP scraper that silently returns empty bodies on every SPA.

**Trawl** is a Go-based scraping tool that automatically routes each URL to the cheapest engine that returns valid content:

```
HTTP (net/http)  →  Lightpanda (fast headless)  →  Chromium (chromedp)
   ~5ms              ~150ms                          ~2000ms
```

The router learns per-domain which tier works, persists the frontier so crashes don't lose work, handles politeness/robots/rate-limits, and produces structured output in whatever format the consumer wants (JSONL, Parquet, SQLite, CSV).

It is built as a **standalone CLI + Go library**, with a thin gstack skill wrapper for natural-language invocation. The tool is the product; the skill is a UI.

---

## 2. Goals & Non-Goals

### Goals
- **Speed via intelligence**, not via brute force. Use the cheapest engine that works.
- **Correctness above all.** A fast scraper that returns empty DOMs is worse than a slow one that returns content.
- **Resumable.** A 6-hour crawl must survive a crash, a SIGTERM, a network blip.
- **Polite by default.** robots.txt, per-domain rate limits, concurrent caps. Opt-out, not opt-in.
- **Boring deploy.** Single static binary. Runs on a $5 VPS. No runtime, no daemon, no Docker required.
- **Multiple task shapes.** Single-page scrape, URL-list batch, BFS crawl, sitemap crawl, structured extraction.
- **Observable.** Structured logs, optional Prometheus metrics, per-job stats.

### Non-Goals (v1)
- **Distributed mode.** A single box with goroutines handles enormous workloads. Don't ship coordination until someone hits the wall.
- **Browser-fingerprint stealth (uTLS, navigator.webdriver patches, etc.).** When a site fights back, route to Chromium and accept the cost. Stealth is a rabbit hole.
- **CAPTCHA solving.** Not our problem. Surface the failure and move on.
- **A scraping DSL.** Go code + config files are enough. Don't invent a YAML programming language.
- **Real-time scraping / streaming.** This is batch dataset collection, not a live feed.

---

## 3. Core Concepts

### 3.1 The Tiered Router

Every URL passes through a router that escalates engines until one returns valid content:

```
                ┌──────────────┐
                │  URL in      │
                └──────┬───────┘
                       │
                       ▼
                ┌──────────────┐    valid    ┌──────────┐
                │  Tier 1      │ ─────────▶  │  Done    │
                │  net/http    │              └──────────┘
                └──────┬───────┘
                       │ invalid
                       ▼
                ┌──────────────┐    valid    ┌──────────┐
                │  Tier 2      │ ─────────▶  │  Done    │
                │  Lightpanda  │              └──────────┘
                └──────┬───────┘
                       │ invalid
                       ▼
                ┌──────────────┐    valid    ┌──────────┐
                │  Tier 3      │ ─────────▶  │  Done    │
                │  Chromium    │              └──────────┘
                └──────┬───────┘
                       │ invalid
                       ▼
                ┌──────────────┐
                │  Failure     │
                │  (logged)    │
                └──────────────┘
```

### 3.2 What "valid" means

The hard problem. Heuristics, in order of cost:

1. **HTTP status:** 2xx → continue. 4xx → fail (don't escalate, the URL is bad). 5xx → escalate or retry.
2. **Content-Type:** `text/html` or `application/xhtml+xml` → continue. JSON/XML → handle directly, skip browser tiers.
3. **Body size:** < 1KB usually means a stub. Configurable threshold.
4. **SPA shell detection:** Look for `<div id="root"></div>`, `<div id="__next"></div>`, `<app-root></app-root>` with no children. These are "hydrate me" markers.
5. **Selector contract:** If the user passed extraction selectors (`--require .product-title`), those selectors must match. If they don't, the page didn't render — escalate.
6. **Anti-bot signatures:** Cloudflare challenge page (`cf-challenge`), PerimeterX (`px-captcha`), Akamai bot-manager. Detect → escalate to Chromium (which won't help against modern Cloudflare, but at least we've tried). Mark domain as "hostile" in the frontier.

The validity check is pluggable. Default heuristics ship with sensible defaults; users can pass their own validator.

### 3.3 Per-domain tier learning

Track per-domain success rates per tier in the frontier DB. After N consecutive escalations from tier 1 → tier 2 on the same domain, **start at tier 2** for that domain. Same for tier 2 → tier 3. Decay the learning over time so a fixed site can drop back down.

This is the difference between "fast on the first page" and "fast across a 100k-URL crawl."

### 3.4 The Frontier

A persistent URL queue with three concerns:
- **What URLs to fetch next** (priority queue, FIFO by default)
- **What URLs we've already seen** (Bloom filter + exact-match KV for collisions)
- **What state each URL is in** (queued, in-flight, done, failed, retry-after-N)

Backed by **BadgerDB** (embedded LSM-tree KV store). Survives crashes. Resumable via `trawl resume <job-id>`.

URL canonicalization before insertion:
- Lowercase host
- Sort query params alphabetically
- Strip fragments
- Strip tracking params (`utm_*`, `fbclid`, `gclid`, etc.) — configurable list
- Normalize trailing slash per scheme

Content-hash dedup (after fetch): if two URLs return identical content, only keep one. Useful for sites where `/product/123` and `/product/123?ref=foo` are aliases.

### 3.5 Politeness

- **robots.txt:** fetched once per domain, cached, respected by default. `--ignore-robots` flag exists but logs a warning.
- **Per-domain rate limit:** token bucket, default 1 req/sec/domain. Configurable.
- **Per-domain concurrency cap:** max in-flight requests to a single host. Default 4.
- **Global concurrency cap:** max in-flight requests total. Default 200.
- **Adaptive backoff:** on 429/503, exponential backoff with jitter, respect `Retry-After`.
- **User agent:** identifies as `trawl/<version> (+https://github.com/...)` by default. Can be overridden or rotated from a list.

---

## 4. Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                          CLI (cobra)                            │
│   trawl crawl | scrape | batch | extract | resume | status      │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                       Job Coordinator                           │
│   - Loads/creates job from BadgerDB                             │
│   - Spawns N worker goroutines                                  │
│   - Manages graceful shutdown (SIGINT/SIGTERM)                  │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                          Frontier                               │
│   - Priority queue (BadgerDB)                                   │
│   - Bloom filter for fast "seen" check                          │
│   - URL canonicalization                                        │
│   - Per-domain rate limiter (token bucket)                      │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                       Tiered Router                             │
│   - Per-domain tier preference (learned)                        │
│   - Validity checker (pluggable)                                │
│   - Engine pool (HTTP / Lightpanda / Chromium)                  │
└──┬───────────────────┬────────────────────────┬────────────────┘
   │                   │                        │
   ▼                   ▼                        ▼
┌──────────┐    ┌──────────────┐         ┌──────────────┐
│  HTTP    │    │  Lightpanda  │         │  Chromium    │
│ net/http │    │  (subproc)   │         │  (chromedp)  │
│ goquery  │    │  via CDP     │         │  via CDP     │
└────┬─────┘    └──────┬───────┘         └──────┬───────┘
     │                 │                        │
     └─────────────────┴────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│                        Extractor                                │
│   - CSS (goquery) | XPath (htmlquery) | JSONPath (gjson)        │
│   - Regex                                                       │
│   - Optional: LLM extraction fallback (v2)                      │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                      Output Sinks                               │
│   JSONL | JSON | CSV | Parquet | SQLite | Custom template       │
└─────────────────────────────────────────────────────────────────┘
```

### Engine pools

Each browser tier maintains a pool of warm instances:
- **Lightpanda pool:** N subprocesses, each driven over CDP. Recycled every M pages (memory bloat). Default N=4.
- **Chromium pool:** N chromedp browsers, contexts isolated per page. Default N=2 (chromium is heavy).

Pool sizing is configurable and should adapt to host RAM.

---

## 5. CLI Surface

Designed for ergonomic single-command use AND complex config-file jobs.

```bash
# Single page, print extracted data to stdout
trawl scrape https://example.com --selector ".price=.product-price" --selector ".title=h1"

# Scrape a list of URLs from a file, write JSONL
trawl batch urls.txt --output results.jsonl --concurrency 50

# BFS crawl from a seed, depth 3, same-domain only
trawl crawl https://example.com --depth 3 --same-domain --output crawl.jsonl

# Sitemap-driven crawl
trawl crawl --sitemap https://example.com/sitemap.xml --output sitemap-crawl.jsonl

# Use a config file for complex jobs (recommended for production)
trawl run job.yaml

# Resume a crashed/paused job
trawl resume <job-id>

# List jobs and their state
trawl status

# Show metrics for a job
trawl stats <job-id>

# Force a specific tier (debugging / benchmarking)
trawl scrape https://example.com --force-tier chromium

# Probe-only: report which tier each URL needs, don't extract
trawl probe urls.txt
```

### Job config file (YAML)

```yaml
name: example-product-crawl
seeds:
  - https://example.com/products
crawl:
  depth: 5
  same_domain: true
  follow: ["a.product-link", "a.next-page"]
filters:
  include: ["/products/", "/category/"]
  exclude: ["/admin/", "/cart"]
extract:
  title: "h1.product-title"
  price: ".price-current"
  sku: { selector: "[data-sku]", attr: "data-sku" }
  description: { selector: ".description", text: true }
  images: { selector: "img.product-image", attr: "src", multiple: true }
output:
  format: jsonl
  path: ./products.jsonl
politeness:
  rate_per_domain: 2/s
  concurrent_per_domain: 4
  global_concurrent: 100
  obey_robots: true
router:
  validity:
    min_body_bytes: 2048
    require_selectors: ["h1.product-title", ".price-current"]
  tier_preference: auto   # or: http | lightpanda | chromium
proxy:
  enabled: false
  list: ./proxies.txt
  rotation: per_request   # or: per_domain | sticky
```

---

## 6. Data Model

### 6.1 Frontier (BadgerDB)

Key prefixes:
- `url:<canonical_url>` → URL state record (status, attempts, last_tier, last_error, content_hash)
- `domain:<host>` → domain state (tier_preference, success_count_by_tier, robots_cache, last_request_at)
- `seen:<bloom>` → bloom filter for fast dedup
- `job:<job_id>` → job metadata (config, started_at, status, stats)
- `result:<job_id>:<seq>` → result records (if not streaming to external sink)

### 6.2 Result record (JSONL line)

```json
{
  "url": "https://example.com/products/123",
  "canonical_url": "https://example.com/products/123",
  "fetched_at": "2026-04-10T15:23:11Z",
  "tier": "lightpanda",
  "status_code": 200,
  "duration_ms": 142,
  "content_hash": "sha256:...",
  "extracted": {
    "title": "Widget",
    "price": "$19.99"
  },
  "metadata": {
    "content_type": "text/html",
    "body_bytes": 48291,
    "redirects": []
  }
}
```

### 6.3 Output formats

| Format  | Use case                                |
|---------|------------------------------------------|
| JSONL   | Default. Streaming, append-only, durable |
| JSON    | Small jobs, single array                 |
| CSV     | Spreadsheet/Excel handoff                |
| Parquet | Analytics workloads (DuckDB, Polars)     |
| SQLite  | Joinable, queryable, single file         |
| Template | text/template for arbitrary output      |

---

## 7. Tech Stack (Decided — don't relitigate)

| Concern              | Choice                      | Why                                              |
|----------------------|----------------------------|--------------------------------------------------|
| Language             | Go 1.23+                   | Concurrency, single binary, scraping is its home |
| CLI framework        | `spf13/cobra`              | Standard, mature                                 |
| Config               | `spf13/viper`              | YAML/TOML/env, integrates with cobra             |
| HTML parsing         | `PuerkitoBio/goquery`      | jQuery-like API on Go's net/html                 |
| XPath                | `antchfx/htmlquery`        | XPath when CSS isn't enough                      |
| HTTP                 | `net/http` + custom transport | Full control, no surprises                   |
| Headless Chromium    | `chromedp/chromedp`        | CDP, no Selenium nonsense                        |
| Lightpanda driver    | `chromedp` against Lightpanda subprocess | Lightpanda speaks CDP             |
| Persistent KV        | `dgraph-io/badger/v4`      | Embedded LSM, fast, single dir                   |
| robots.txt           | `temoto/robotstxt`         | Battle-tested                                    |
| Logging              | `rs/zerolog`               | Structured, fast, zero-alloc                     |
| Metrics              | `prometheus/client_golang` | Standard                                         |
| Bloom filter         | `bits-and-blooms/bloom`    | Standard                                         |
| Rate limiting        | `golang.org/x/time/rate`   | stdlib-adjacent token bucket                     |
| Parquet output       | `parquet-go/parquet-go`    | Pure Go Parquet                                  |
| SQLite output        | `modernc.org/sqlite`       | Pure Go, no CGO                                  |

**Hard constraint: NO CGO.** Single static binary, cross-compile freely. This eliminates `mattn/go-sqlite3` and forces `modernc.org/sqlite`.

---

## 8. Lightpanda Integration Details

Lightpanda runs as a subprocess that exposes a CDP endpoint. Drive it the same way as Chromium:

```go
// Pseudocode
lp := exec.Command("lightpanda", "--cdp-port", "9222")
lp.Start()
// Wait for CDP to be ready (poll /json/version)
ctx, cancel := chromedp.NewRemoteAllocator(ctx, "ws://localhost:9222")
browser, _ := chromedp.NewContext(ctx)
chromedp.Run(browser, chromedp.Navigate(url), chromedp.OuterHTML("html", &html))
```

A reusable `Engine` interface wraps both Lightpanda and Chromium:

```go
type Engine interface {
    Fetch(ctx context.Context, url string) (*FetchResult, error)
    Close() error
    Name() string
}
```

Lightpanda is a moving target — pin a version and document the binary install path. The build agent should add a `trawl install lightpanda` subcommand that downloads the right binary for the host OS.

---

## 9. gstack Integration

Trawl ships as a standalone binary. The gstack skill is a thin wrapper.

**Skill name:** `/dataset` or `/scrape`. (Not both.)

**Skill responsibilities:**
1. Detect that the user wants a scrape/dataset job from natural language.
2. Ask clarifying questions via `AskUserQuestion` (target site, depth, what to extract, output format).
3. Generate a `job.yaml` and run `trawl run job.yaml`.
4. Stream progress to the user.
5. Hand off the output file when done.

**Skill does NOT:**
- Reimplement scraping logic
- Drive browsers directly
- Make HTTP requests itself

The skill is a UI on top of the binary, the same way `/browse` is a UI on top of the `browse` binary.

---

## 10. Build Phases

### P0 — MVP (target: 1-2 weeks)
The thinnest version that's actually useful.

- [ ] Project scaffold (cobra, viper, zerolog)
- [ ] HTTP tier only (`net/http` + `goquery`)
- [ ] BadgerDB frontier with URL canonicalization
- [ ] Single-URL scrape (`trawl scrape`)
- [ ] URL-list batch (`trawl batch`)
- [ ] CSS selector extraction
- [ ] JSONL output sink
- [ ] robots.txt + per-domain rate limit
- [ ] Per-domain + global concurrency caps
- [ ] Graceful shutdown (SIGINT writes frontier state, exits clean)
- [ ] `trawl resume`
- [ ] Tests for: canonicalization, frontier dedup, extraction, validity heuristics

**Done when:** you can scrape a 1000-URL list of static-HTML sites end-to-end, kill it mid-run, resume it, and get correct output.

### P1 — Tiered routing (target: 2-4 weeks)
The whole point of the tool.

- [ ] `Engine` interface + HTTP engine refactor
- [ ] Lightpanda engine (subprocess + CDP via chromedp)
- [ ] Chromium engine (chromedp)
- [ ] `trawl install lightpanda` (download + verify binary)
- [ ] Validity checker (status, content-type, body size, SPA shell detection, selector contract)
- [ ] Tiered router with per-domain learning (persisted in BadgerDB)
- [ ] Engine pools with warm instances + recycling
- [ ] BFS crawl mode (`trawl crawl --depth N --same-domain`)
- [ ] Sitemap crawl mode
- [ ] `trawl probe` (report tier needed without extracting)
- [ ] Content-hash dedup
- [ ] XPath + JSONPath extractors
- [ ] CSV + SQLite output sinks

**Done when:** you can point trawl at a mixed list (static sites, SPAs, and one Cloudflare-protected site) and it routes correctly without manual configuration.

### P2 — Production rig (target: 4+ weeks)
The features that turn it into something you'd actually run on a server.

- [ ] Proxy support (HTTP, SOCKS5, rotation strategies)
- [ ] Cookie jar persistence per domain
- [ ] Anti-bot detection (Cloudflare/PerimeterX/Akamai signatures → mark + escalate)
- [ ] Parquet output sink
- [ ] Prometheus metrics endpoint (`--metrics-port 9090`)
- [ ] Structured logging with job_id/url/tier fields
- [ ] `trawl status` and `trawl stats` for live job inspection
- [ ] Job config file (YAML) — full schema validation
- [ ] gstack skill (`/dataset` or `/scrape`) wrapper
- [ ] Documentation site (or comprehensive README)

**Done when:** you can run a multi-day crawl on a Hetzner box with proxy rotation, monitor it via Prometheus, and the gstack skill drives it from natural language.

### P3 — Intelligence (ongoing, post-v1)
The features that justify the "intelligent" claim beyond tier routing.

- [ ] LLM extraction fallback (when selectors fail, ask Claude to extract structured data)
- [ ] Schema inference from sample pages
- [ ] Auto-pagination detection ("Next" link discovery, infinite scroll detection)
- [ ] Per-domain politeness auto-tuning (slow down on 429s, speed up on consistent 200s)
- [ ] Distributed mode (only if a single box hits the wall — probably not for a long time)

---

## 11. Success Criteria

A v1 release of trawl is successful if:

1. **Correctness:** On a benchmark of 100 mixed URLs (static, SSR, SPA, Cloudflare), trawl extracts correct content from ≥95% without per-URL configuration.
2. **Speed:** On the same benchmark, trawl finishes in <30% of the time of "always Chromium" by routing simpler pages to faster tiers.
3. **Resumability:** Killing trawl mid-run and resuming produces identical final output to an uninterrupted run (modulo timing).
4. **Resource use:** A 100k-URL crawl runs in <2GB RAM on a single box.
5. **Deploy story:** `curl -L .../trawl -o trawl && chmod +x trawl && ./trawl --version` works on Linux x86_64, Linux arm64, macOS arm64, macOS x86_64. No runtime install required.

---

## 12. Open Questions for the Build Agent

These are the decisions I deliberately did NOT make. The build agent should resolve them, document the choice, and move on.

1. **Project layout.** Standard Go layout (`cmd/`, `internal/`, `pkg/`)? Or flatter? Pick whichever the team is comfortable with — this is taste, not architecture.
2. **Job ID format.** UUID? Timestamp + slug? Both work; pick one.
3. **Where does BadgerDB live by default?** `~/.trawl/jobs/<job_id>/` is fine. Make it overridable.
4. **Lightpanda binary management.** Bundle? Download on first run? Require user install? Recommendation: download on first run via `trawl install lightpanda`, cache in `~/.trawl/bin/`.
5. **Test strategy.** Table-driven unit tests for parsers/canonicalization/validity; integration tests against a local httptest server with a few representative HTML fixtures (static, SPA shell, Cloudflare-mock, redirect chain). Avoid hitting the public internet in CI.
6. **Logging defaults.** Pretty console output for TTY, JSON for non-TTY. Standard zerolog pattern.
7. **What to do on persistent failures.** Dead-letter queue in BadgerDB? Retry with exponential backoff up to N attempts? Both, configurable. Default: 3 attempts with backoff, then dead-letter.

---

## 13. What This Spec Deliberately Excludes

If the build agent is tempted to add any of these in v1, **don't**:

- A web UI (CLI is enough)
- A scheduler (cron + `trawl run` is enough)
- Multi-tenant job isolation (single-user tool)
- Plugin system / scripting language (Go code is the extension point)
- Browser fingerprint stealth (rabbit hole)
- CAPTCHA solving (out of scope)
- Distributed coordination (premature)
- A query language for the output (jq exists, DuckDB exists)
- Real-time/streaming dataset feeds (this is batch)

These are all reasonable to add later. None of them belong in v1.

---

## 14. First Commit Checklist

When the build agent picks this up, the first PR should include:

1. `go.mod` with the dependencies from §7
2. `cmd/trawl/main.go` with cobra root + version subcommand
3. `internal/frontier/` package with BadgerDB-backed URL queue + tests
4. `internal/canonical/` package with URL canonicalization + tests
5. `internal/engine/http.go` with the HTTP tier
6. `internal/extract/css.go` with goquery-based extraction
7. `internal/output/jsonl.go` with the JSONL sink
8. `trawl scrape <url>` working end-to-end
9. `trawl batch <file>` working end-to-end with concurrency
10. README with the pitch from §1 and a quick-start example

Everything else builds on this foundation.

---

## Appendix A: Why Go (decision log)

Considered and rejected:
- **Python/Scrapy:** most feature-complete framework but GIL hurts at extreme concurrency, packaging is painful, deploy story is bad in 2026.
- **Rust:** fastest but overkill for an I/O-bound workload, ecosystem thinner, build times slower.
- **TypeScript/Bun + Crawlee:** great for a gstack-internal tool but constrains the audience to JS folks and won't match Go's concurrency at the high end.

Go wins because:
- Goroutines + channels are unmatched for fan-out crawlers (500-2000 in-flight on one box)
- Single static binary deploys to any cheap VM
- Mature scraping ecosystem (Colly, chromedp, goquery)
- The browser engines are subprocesses anyway, so language-level browser bindings don't matter

## Appendix B: Why not just use Colly?

Colly is great. The reason trawl isn't a Colly wrapper:
- Colly's design assumes one engine. The tiered router is a fundamentally different abstraction — the engine is dynamic per-URL.
- Colly's frontier is in-memory by default and resumability is bolted on. Trawl needs persistent-first.
- Building on Colly means inheriting its config surface, which is large and constrains the CLI design.

Trawl can borrow patterns from Colly (and chromedp, and Scrapy) without depending on it. The build agent is encouraged to read Colly's source for ideas, not import it.
