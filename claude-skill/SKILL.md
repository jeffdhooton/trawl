---
name: trawl
version: 0.7.1
description: |
  Tiered web scraping for AI agents. HTTP → Chromium routing with persistent
  frontier, resumable batch jobs, BFS crawl, sitemap discovery, URL mapping,
  clean markdown/JSON extraction, page metadata, CSS selectors, YAML schema
  extraction (v2: fallback selectors + transforms), interactive pre-scrape
  actions (click, scroll, wait, type, evaluate), PDF extraction with optional
  OCR for scans, proxy support (single + rotating pool), and anti-detection
  tiers (browser-like headers, chromium stealth, Chrome TLS fingerprint
  forgery). Feature-parity with Firecrawl on in-scope items — runs as a local
  static binary, no API key, no runtime dependency. Use when asked to
  "scrape", "crawl", "extract pages", "get markdown from a site", "enumerate
  URLs", "scrape a list of companies", "map a site", "extract text from a
  PDF", or anytime the task is bulk content-in / clean-JSONL-out.
allowed-tools:
  - Bash
  - Read
  - Write
  - AskUserQuestion
---

# /trawl: Tiered Web Scraping

Trawl is a single Go binary that routes each URL through the cheapest engine
that returns valid content (HTTP → Chromium), remembers which tier worked per
host, persists the frontier so long crawls survive crashes, and produces clean
markdown + metadata ready for downstream pipelines.

**One binary. No API key. No Docker. No runtime dependency.**

Think of it as "Firecrawl's CLI surface, but local." Same command shape
(`scrape`, `crawl`, `map`, `sitemap`), different runtime cost (free), and
composable with Unix pipes.

---

## When to use trawl vs other tools

| Task | Use | Why |
|---|---|---|
| Bulk scraping 100+ URLs | **`trawl batch`** | Persistent frontier, resumable, per-domain rate limiting, tier learning |
| Scrape one URL → clean markdown | **`trawl scrape`** | HTML→markdown + readability + page metadata + selectors in one call |
| Walk a whole site | **`trawl crawl`** | BFS link discovery, same-domain filter, depth cap, one JSONL file out |
| Enumerate a site's URLs without scraping | **`trawl map`** | Sitemap + HTML crawl union, URL list to stdout |
| Find sitemaps for a domain | **`trawl sitemap`** | robots.txt + well-known paths + recursive sitemap-index |
| Interact with a single page (click, fill, assert) | **`/browse`** | Trawl doesn't interact -- it extracts. `/browse` is for stateful QA. |
| Read one page inline for a one-off reference | **`WebFetch`** | Trawl is overkill for single casual lookups -- use WebFetch for "just tell me what this page says". |
| Scrape with LLM extraction | **`trawl scrape --format markdown` → pipe to LLM** | Trawl explicitly stays out of the LLM business. Clean markdown out, LLM downstream. |
| Extract a PDF to markdown | **`trawl scrape <pdf-url>`** (auto) | Content-type dispatch — PDFs are transformed in place when the server returns `application/pdf`. Add `--ocr` for scanned PDFs. |
| Crawl hostile / bot-protected sites | **`trawl batch --browser-like --stealth --tls-match chrome`** | Opt-in evasion tiers (see [`docs/EVASION.md`](https://github.com/jeffdhooton/trawl/blob/main/docs/EVASION.md)). Polite-by-default otherwise. |
| Validate a proxy before a long job | **`trawl proxy-test --proxy-file proxies.txt`** | Checks each proxy's exit IP, latency, and diff vs your direct IP before you commit to a multi-hour crawl. |

**Rule of thumb:** if the output you want is a JSONL file with multiple
records, trawl is the right tool. If the output is "did the click work?",
use `/browse`. If the output is "summarize this one page", use WebFetch.

---

## Setup check

Before running any command, verify the binary is on PATH:

```bash
if ! command -v trawl >/dev/null 2>&1; then
  echo "trawl not installed"
  echo "  one-liner: curl -fsSL https://raw.githubusercontent.com/jeffdhooton/trawl/main/scripts/install.sh | sh"
  echo "  or via Go: go install github.com/jeffdhooton/trawl/cmd/trawl@latest"
  exit 1
fi
trawl version
```

If `trawl` is not present, offer to install it via the command above. Don't
proceed with scraping work until the binary exists.

**Optional system dependencies** — check these only when the user's task
involves PDFs or scans:

```bash
# pdftotext (Tier 1/2 PDF extraction)
command -v pdftotext >/dev/null 2>&1 || echo "pdftotext missing — install poppler-utils for PDF support"

# tesseract + pdftoppm (Tier 3 OCR — only needed for --ocr)
if ! command -v tesseract >/dev/null 2>&1 || ! command -v pdftoppm >/dev/null 2>&1; then
  echo "OCR toolchain missing — install tesseract + poppler-utils"
fi
```

Missing `pdftotext` is a soft-fail (HTML still works); missing
`tesseract` only matters when `--ocr` is requested.

---

## Command decision tree

```
Need to scrape content from the web
│
├── One URL? ────────────────────────▶ trawl scrape <url>
│       └── Is it a PDF? ────────────▶ (auto — add --ocr if scanned)
│
├── List of URLs (file / CSV)? ──────▶ trawl batch urls.txt
│
├── Whole site from a seed page? ────▶ trawl crawl <seed-url>
│
├── Just need URLs (not content)? ───▶ trawl map <url>
│       └── Or only from sitemaps? ──▶ trawl sitemap <url>
│
├── Validate proxies before a job? ──▶ trawl proxy-test --proxy-file <f>
│
└── Interrupted job to resume? ──────▶ trawl resume <job-id>
```

**Content-type dispatch**: trawl inspects the response Content-Type on
every fetch. `application/pdf` is transformed to markdown via
`pdftotext` automatically — same command surface as HTML, different
downstream pipeline. No separate command, no flag needed in the default
case; add `--ocr` only when targets include scanned/image-only PDFs.

---

## Core recipes

### 1. Scrape a single page to clean markdown

```bash
trawl scrape https://linear.app \
  --format markdown \
  --readability \
  -o page.jsonl
```

What you get: one JSONL record with `body` (markdown), `metadata.page` (title,
description, canonical, OG, JSON-LD), `canonical_url`, `fetched_at`, `tier`,
`status_code`, `duration_ms`, `content_hash`, `failure_category`.

**Flag cheat sheet:**
- `--format markdown` -- HTML→markdown in the `body` field. Also `html` or `json` (JSON-serialized extracted map). Omit to skip body entirely.
- `--readability` -- strip nav/footer/ads before conversion or CSS extraction.
- `--no-metadata` -- skip page metadata extraction (faster, smaller records).
- `-o file.jsonl` -- output file (default `-` = stdout).
- `--timeout 30s` -- HTTP request timeout.
- `--action "click:.btn"` -- pre-scrape interaction (repeatable, chromium only). Verbs: click, wait, scroll, type, sleep, evaluate.
- `--actions file.yaml` -- YAML/JSON file with a sequence of pre-scrape actions (chromium only).

### 2. Extract structured fields with CSS selectors

```bash
trawl scrape https://stripe.com/pricing \
  --selector "headline=h1" \
  --selector "plans=.pricing-card h3" \
  --selector "prices=.pricing-card .price" \
  --format markdown \
  -o stripe.jsonl
```

Selector syntax:
- `name=css` -- grab first match, text content
- `name=css[]` -- grab all matches, array of text
- `name=css@attr` -- grab attribute value (e.g. `link=a@href`)
- `name=css[]@attr` -- array of attribute values

Selectors land in the `extracted` field on the output record.

### 3. Extract with a YAML schema (complex / nested)

For structured extraction with nested objects or repeated groups, use
`--schema` with a YAML file instead of stacking `--selector` flags:

```yaml
# article.yaml
version: 1
fields:
  title:
    selector: "h1"
  authors:
    selector: ".byline a"
    multiple: true
    fields:
      name:
        selector: ""
      profile:
        selector: ""
        attr: "href"
  body:
    selector: "article"
```

```bash
trawl scrape https://example.com/post \
  --schema article.yaml \
  --readability \
  -o post.jsonl
```

Reference examples in the trawl repo:
- `docs/examples/sep-article.yaml` -- v1, Stanford Encyclopedia of Philosophy (nested TOC, related entries, author)
- `docs/examples/hn-frontpage.yaml` -- v2, Hacker News front page (fallback selectors + regex transforms)

**v2 schema features** (use `version: 2`):
- **Fallback selectors**: `selector: ["#primary", "#fallback"]` -- first match wins
- **Transforms**: post-extraction pipeline on leaf values: `trim`, `regex` (first capture group), `lowercase`, `uppercase`, `split`

### 4. Batch-scrape a URL list (resumable)

```bash
trawl batch urls.txt \
  --format markdown \
  --readability \
  --concurrency 20 \
  --rate 1 \
  -o results.jsonl
```

What you get: one JSONL line per URL. On SIGINT/SIGTERM, trawl shuts down
gracefully and prints a resume command with the job ID.

**Flag cheat sheet:**
- `-c, --concurrency 20` -- max in-flight requests across all domains (default 20).
- `--rate 1` -- requests per second per domain (default 1, polite).
- `--job-id my-job` -- name the job so resume is human-readable.
- `-o results.jsonl` -- output file.

### 5. Batch from CSV with hybrid fallback discovery

When your seed list is a CSV with both a primary URL and a homepage fallback,
trawl can re-route through the fallback on `http_4xx` or `dns_failure`:

```bash
trawl batch companies.csv \
  --url-column pricing_url \
  --fallback-column homepage \
  --fallback-selector 'a[href*="pricing"]' \
  --format markdown \
  -o companies.jsonl
```

Trawl tries `pricing_url` first. If that fetch fails with a trigger category
(`http_4xx`, `dns_failure`), it loads `homepage`, runs the `fallback-selector`
to find a pricing link, and re-scrapes from there. Both the original failure
and the fallback attempt are recorded.

### 6. BFS-crawl a whole site from a seed page

```bash
trawl crawl https://docs.example.com \
  --depth 3 \
  --same-domain \
  --limit 500 \
  --format markdown \
  --readability \
  -o docs-corpus.jsonl
```

Walks the site breadth-first, routing every discovered page through the same
tiered pipeline as scrape/batch. Produces a markdown corpus suitable for
feeding into an LLM index.

**Flag cheat sheet:**
- `--depth 3` -- BFS depth (seed is depth 0, default 2).
- `--limit 500` -- hard cap on URLs enqueued (default 1000, `0` = unlimited).
- `--same-domain` -- restrict to seed's host and sibling subdomains (default on).
- `--format markdown --readability` -- clean corpus, not raw HTML dumps.

### 7. Enumerate URLs without scraping

```bash
# Both sources (sitemap + HTML crawl)
trawl map https://example.com --depth 2 > urls.txt

# Sitemap only (fastest, publisher-declared)
trawl map https://example.com --sources sitemap > urls.txt

# HTML crawl only (no sitemap trust)
trawl map https://example.com --sources crawl --depth 3 > urls.txt
```

`map` is the URL-list equivalent of `crawl` -- no extraction, no body writes,
just a deduped URL list to stdout. Compose with `batch`:

```bash
trawl map https://example.com > urls.txt
trawl batch urls.txt --format markdown -o scraped.jsonl
```

### 8. Discover sitemaps for a domain

```bash
trawl sitemap https://stripe.com --verbose
```

Checks robots.txt for `Sitemap:` directives, falls back to `/sitemap.xml` and
`/sitemap_index.xml`, recursively expands sitemap-index files (up to
`--max-depth`, default 3), handles gzip, dedupes URLs. Stream-friendly for
large sites (up to `--max-urls`, default 50000).

### 9. Resume an interrupted job

```bash
# Trawl printed "resume with: trawl resume abc123" on SIGINT
trawl resume abc123
```

Trawl reopens the frontier, re-queues any in-flight URLs that didn't complete,
and drains the remaining work using the same configuration saved at job
creation time. No flags needed -- the job's own state has everything.

### 10. Extract PDFs (text-layer or scanned)

PDFs work out of the box — when the server returns `application/pdf`, trawl
transforms the body to markdown via `pdftotext` and populates
`metadata.pdf` with `page_count`, `title`, `author`, `has_text_layer`,
`used_ocr`, `extractor_tier`, and per-page text.

```bash
# Text-layer PDF (research paper, government form, etc.)
trawl scrape https://arxiv.org/pdf/1706.03762 --format markdown -o paper.jsonl

# Scanned PDF (older declassified doc, historical archive)
trawl scrape https://example.com/scanned.pdf \
  --format markdown \
  --ocr \
  --pdf-max-pages 20 \
  -o scan.jsonl

# Batch mixed PDFs + HTML — trawl dispatches per-record by Content-Type
trawl batch mixed-urls.txt --format markdown --ocr -o mixed.jsonl
```

**Dependencies** (install separately; trawl shells out):

- `poppler-utils` for Tier 1/2 (`pdftotext`). Missing → soft-fail per
  row with `failure_category: pdf_tooling_missing` and a clear install
  hint. HTML crawls unaffected.
- `tesseract` + `pdftoppm` for Tier 3 OCR (`--ocr`). Missing → hard-fail
  at command start, before any URLs are fetched. Install via
  `brew install tesseract` or `apt install tesseract-ocr`.

**Tier ladder inside the PDF engine** (same validity → escalate pattern
as the main HTTP → Chromium router):

1. `pdftotext` plain — ~100ms, handles the vast majority of modern PDFs.
2. `pdftotext -layout` — preserves columns/tables, escalated when Tier 1
   output is empty.
3. `tesseract` OCR — only fires with `--ocr` and only when Tier 1+2
   both returned empty. Caps pages at `--pdf-max-pages` (default 50).

**Non-goals**: form fields, image extraction, password-protected PDFs,
CSS-selector `--schema` extraction against PDFs. See
[`docs/PDF.md`](https://github.com/jeffdhooton/trawl/blob/main/docs/PDF.md)
for the full scope.

### 11. Route through a proxy

```bash
# Single gateway proxy — all requests route through it
trawl batch urls.txt \
  --proxy http://user:pass@gateway.proxy.com:7000 \
  -o results.jsonl

# Rotating pool — per-domain sticky assignment (same domain → same proxy
# for the job's lifetime, different domains spread across pool)
trawl batch urls.txt \
  --proxy-file proxies.txt \
  -o results.jsonl

# Rotate proxies on block status codes (403/429/503) and retry same tier
trawl batch urls.txt \
  --proxy-file proxies.txt \
  --rotate-on-status 403,429,503 \
  --rotate-retries 2 \
  -o results.jsonl

# Pre-flight check proxies before a long run
trawl proxy-test --proxy-file proxies.txt
```

Proxied records are stamped with `metadata.evasion.proxy: true`. Works
on HTTP, uTLS (`--tls-match chrome`), and chromium tiers. Chromium uses
the first proxy in the pool (Chrome's `--proxy-server` flag is
process-wide).

**`proxy-test` output**:

```
proxy: http://user:***@gate.proxy.com:7000  exit=203.0.113.42  latency=284ms  OK
proxy: http://user:***@gate2.proxy.com:7000 exit=203.0.113.43  latency=310ms  OK
```

Use it to catch "proxy's exit IP equals your direct IP" misconfigurations
before they cost you a full crawl.

### 12. Opt-in evasion tiers (hostile / bot-protected sites)

Trawl is polite-by-default: declared User-Agent, robots-respecting,
rate-limited. For hostile targets (fingerprint-based bot detection), three
opt-in tiers are available — each layer is additive.

```bash
# Tier 1: browser-like HTTP (rotating UAs, full Chrome header set,
# in-memory cookie jar, ±20% timing jitter)
trawl scrape https://hostile.example.com \
  --browser-like \
  --format markdown

# Tier 2: chromium stealth (navigator.webdriver patch, WebGL fingerprint
# mask, plugins spoofing — requires chromium tier)
trawl scrape https://hostile.example.com \
  --browser-like \
  --stealth \
  --tiers chromium \
  --format markdown

# Tier 3: Chrome TLS fingerprint forgery (JA3/JA4 match via uTLS with
# full HTTP/2 support). Affects the HTTP tier only — chromium uses
# its own real Chrome TLS stack.
trawl scrape https://hostile.example.com \
  --browser-like \
  --tls-match chrome \
  --format markdown
```

**When to reach for each tier** (see
[`docs/EVASION.md`](https://github.com/jeffdhooton/trawl/blob/main/docs/EVASION.md)
for the full decision matrix):

- `--browser-like` — start here. Solves UA- and header-gated blocks.
- `--stealth` — add when a site serves content but JS fingerprinting
  returns empty/degraded bodies.
- `--tls-match chrome` — add when HTTP returns 403/429 at the TLS layer
  (before any request body inspection). Rarely needed as of 2026-04.

**Hard rule — trawl does not defeat CAPTCHAs.** If a site serves a
challenge page (Cloudflare, reCAPTCHA, Turnstile), that's a "you lost"
state. Stop evading, ask the site for API access, or pay a
CAPTCHA-solving service — not trawl's job.

### 13. Tune retries and per-host politeness

```bash
# HTTP retries on transient failures (429, 5xx, connection resets) —
# exponential backoff with ±25% jitter, capped at 10s including jitter.
trawl batch urls.txt --retries 3 --retry-delay 500ms

# Per-host overrides via YAML (exact host or *.suffix wildcard)
# See docs/examples/politeness.yaml
trawl batch mixed-urls.txt --politeness politeness.yaml
```

Example `politeness.yaml`:

```yaml
default:
  rate: 1
  concurrency: 4
hosts:
  - host: plato.stanford.edu
    rate: 0.5        # SEP is academic, slow-crawl
    concurrency: 2
  - host: "*.gov"
    rate: 0.5        # slow-crawl all government TLDs
```

First-match-wins top-to-bottom; `*.suffix` doesn't match the bare TLD.

---

## Output shape

Every record is one line of JSONL:

```json
{
  "url": "https://example.com/pricing",
  "canonical_url": "https://example.com/pricing",
  "fetched_at": "2026-04-10T22:49:41Z",
  "tier": "http",
  "status_code": 200,
  "duration_ms": 384,
  "content_hash": "sha256:...",
  "extracted": { "headline": "Simple pricing", "plans": ["Free", "Pro"] },
  "body": "# Pricing\n\n...",
  "body_format": "markdown",
  "metadata": {
    "content_type": "text/html; charset=utf-8",
    "body_bytes": 24815,
    "final_url": "https://example.com/pricing",
    "page": {
      "title": "Pricing | Example",
      "description": "...",
      "canonical": "https://example.com/pricing",
      "language": "en",
      "published_at": "2026-02-14T09:30:00Z",
      "open_graph": { "title": "...", "image": "..." },
      "twitter": { "card": "summary_large_image" },
      "json_ld": [ { "@type": "Article", "headline": "..." } ]
    },
    "evasion": { "browser_like": true, "user_agent": "Mozilla/5.0 ...", "proxy": true },
    "pdf": null
  },
  "failure_category": "success"
}
```

**PDF records** look the same but with `content_type: "text/markdown"`
(swapped after transform), `body` as markdown, and `metadata.pdf`
populated:

```json
{
  "metadata": {
    "content_type": "text/markdown",
    "pdf": {
      "page_count": 15,
      "title": "Attention Is All You Need",
      "author": "Ashish Vaswani et al.",
      "created_at": "2024-04-10T17:11:43-04:00",
      "has_text_layer": true,
      "used_ocr": false,
      "extractor_tier": "pdftotext",
      "pages": [ { "number": 1, "text": "..." }, { "number": 2, "text": "..." } ]
    }
  }
}
```

**Key fields:**
- `body` -- only populated when `--format` is set (`markdown` or `html`).
- `body_format` -- matches `--format`, omitted when body is.
- `metadata.page` -- always populated unless `--no-metadata`.
- `extracted` -- only populated when `--selector` or `--schema` is set.
- `failure_category` -- `success`, or one of the classified buckets below.

**Failure categories** (filter with `jq`):
- `success` -- fetched and parsed cleanly
- `http_4xx` -- 400-class HTTP error (page not found, auth required, etc.)
- `http_5xx` -- 500-class HTTP error (server broken)
- `dns_failure` -- DNS lookup failed (domain doesn't exist or unreachable)
- `tls_error` -- certificate or handshake failed
- `connection_refused` -- TCP connection actively rejected
- `timeout` -- fetch timed out before completing
- `spa_shell` -- HTTP tier returned a content-free SPA shell (Chromium should handle)
- `robots_blocked` -- blocked by robots.txt (unless `--ignore-robots`)
- `cloudflare_block` -- Cloudflare challenge/firewall rejection (status 1020 or cf-chl text)
- `soft_block` -- every tier returned a 200 OK whose body was an anti-bot challenge wall (CF "Just a moment", Akamai, DataDome, Incapsula, PerimeterX, captcha walls). Per-tier detections also surface under `metadata.soft_block` even on successful routes.
- `parked_domain` -- domain is parked (generally via WPEngine signature)
- `all_tiers_exhausted` -- every tier was attempted, none succeeded
- `extraction_failed` -- fetch worked, post-fetch parsing failed (CSS / schema / PDF)
- `pdf_tooling_missing` -- PDF response fetched OK but `pdftotext` isn't installed; raw bytes preserved in `body`
- `follow_failed` -- hybrid fallback couldn't resolve the fallback selector against the homepage

**Per-job stats:** every batch/crawl job writes a `stats.json` to the job
directory with reachable/unreachable counts, per-category failure breakdown,
per-tier latency, chromium escalation rate, and fallback yield. Read it with
`jq` for quick health checks.

---

## Composing with Unix pipes

### Filter to just successful records with markdown bodies

```bash
jq -c 'select(.failure_category == "success" and .body != null)' results.jsonl
```

### Extract just the markdown into per-URL files

```bash
jq -r '.url + "\t" + (.body // "")' results.jsonl \
  | while IFS=$'\t' read -r url body; do
      slug=$(echo "$url" | sed 's|[^a-zA-Z0-9]|_|g')
      echo "$body" > "pages/$slug.md"
    done
```

### Pipe clean markdown into an LLM for extraction

Trawl deliberately stops at clean content. For LLM-based extraction, compose:

```bash
trawl scrape https://example.com/pricing --format markdown --readability \
  | jq -r '.body' \
  | claude -p "Extract the pricing tiers and monthly costs as JSON"
```

### Find failures for retry

```bash
jq -c 'select(.failure_category != "success")' results.jsonl > failures.jsonl
jq -r '.url' failures.jsonl > retry-urls.txt
trawl batch retry-urls.txt --force-tier chromium -o retry-results.jsonl
```

---

## Failure modes and how to handle them

### SPA shell returned (empty body, meaningful JS)

If a page loads real content only after JavaScript runs, the HTTP tier will
return a `spa_shell` failure category. Trawl auto-escalates to Chromium on the
next run, but you can force it immediately:

```bash
trawl scrape https://some-spa.com --force-tier chromium --format markdown
```

After the first successful chromium fetch, trawl's **tier cache** remembers
that host needs chromium and skips the HTTP tier on subsequent runs.

### Rate limiting / 429s

If you're getting `http_4xx` with 429 status, lower `--rate`:

```bash
trawl batch urls.txt --rate 0.5 --concurrency 5
```

`--rate` is per-domain. For a list spanning many domains, you can keep
concurrency high without hitting any single host hard.

### robots.txt blocking everything

Trawl respects robots.txt by default. If you have authorization to bypass:

```bash
trawl batch urls.txt --ignore-robots  # logs a warning
```

Only use `--ignore-robots` when you have explicit permission (your own site,
client engagement, API-less partner integration).

### Long-running crawl interrupted

Ctrl-C shuts workers down gracefully and prints:

```
resume with: trawl resume <job-id>
```

Save that job ID and come back whenever. The persistent frontier is at
`$TRAWL_HOME/frontier` (defaults to `~/.trawl/frontier`).

---

## Advanced

### Content cache (deduplicate across jobs)

For repeated scraping of the same URLs (daily monitoring, incremental crawls),
enable the cross-job content cache:

```bash
trawl batch urls.txt --cache --cache-ttl 24h
```

Cache hits short-circuit the tier loop and reuse the stored body. Set
`--cache-ttl 0` for no expiration (manual invalidation only).

### Tier pinning (bypass tier learning)

```bash
# Force chromium for this run (ignore tier cache)
trawl scrape https://example.com --force-tier chromium

# Try only HTTP, don't escalate
trawl batch urls.txt --tiers http
```

### Screenshots (chromium tier only)

```bash
trawl crawl https://example.com \
  --screenshot-dir /tmp/shots \
  --format markdown \
  -o crawled.jsonl
```

Full-page PNG per chromium-served page. HTTP-tier responses don't produce
files (the HTTP tier never loads a visual DOM).

### Disable tier learning (fresh-start every run)

```bash
trawl batch urls.txt --no-tier-learning
```

Forces every URL to start at the cheapest tier regardless of prior results.
Useful for benchmarking or when the cache is stale.

---

## Full flag reference

Each command's full flag surface is in its `--help`:

```bash
trawl --help
trawl scrape --help
trawl batch --help
trawl crawl --help
trawl map --help
trawl sitemap --help
trawl resume --help
```

Upstream docs: `github.com/jeffdhooton/trawl` -- `README.md`, `docs/SPEC.md`,
`docs/ROADMAP.md`, and `docs/examples/` for schema examples.
