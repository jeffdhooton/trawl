---
name: trawl
version: 0.4.0
description: |
  Tiered web scraping for AI agents. HTTP → Chromium routing with persistent
  frontier, resumable batch jobs, BFS crawl, sitemap discovery, URL mapping,
  clean markdown/JSON extraction, page metadata, CSS selectors, YAML schema
  extraction (v2: fallback selectors + transforms), and interactive pre-scrape
  actions (click, scroll, wait, type, evaluate). Feature-parity with Firecrawl
  on in-scope items — runs as a local static binary, no API key, no runtime
  dependency. Use when asked to "scrape", "crawl", "extract pages", "get
  markdown from a site", "enumerate URLs", "scrape a list of companies",
  "map a site", or anytime the task is bulk content-in / clean-JSONL-out.
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

**Rule of thumb:** if the output you want is a JSONL file with multiple
records, trawl is the right tool. If the output is "did the click work?",
use `/browse`. If the output is "summarize this one page", use WebFetch.

---

## Setup check

Before running any command, verify the binary is on PATH:

```bash
if ! command -v trawl >/dev/null 2>&1; then
  echo "trawl not installed"
  echo "  install: go install github.com/jeffdhooton/trawl/cmd/trawl@latest"
  echo "  or clone the repo and run: ./install.sh"
  exit 1
fi
trawl version
```

If `trawl` is not present, offer to install it via the command above. Don't
proceed with scraping work until the binary exists.

---

## Command decision tree

```
Need to scrape content from the web
│
├── One URL? ────────────────────────▶ trawl scrape <url>
│
├── List of URLs (file / CSV)? ──────▶ trawl batch urls.txt
│
├── Whole site from a seed page? ────▶ trawl crawl <seed-url>
│
├── Just need URLs (not content)? ───▶ trawl map <url>
│       └── Or only from sitemaps? ──▶ trawl sitemap <url>
│
└── Interrupted job to resume? ──────▶ trawl resume <job-id>
```

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
    }
  },
  "failure_category": "success"
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
- `timeout` -- fetch timed out before completing
- `spa_shell` -- HTTP tier returned a content-free SPA shell (Chromium should handle)
- `robots_disallowed` -- blocked by robots.txt (unless `--ignore-robots`)

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
