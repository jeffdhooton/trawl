# trawl

```
████████╗██████╗  █████╗ ██╗    ██╗██╗
╚══██╔══╝██╔══██╗██╔══██╗██║    ██║██║
   ██║   ██████╔╝███████║██║ █╗ ██║██║
   ██║   ██╔══██╗██╔══██║██║███╗██║██║
   ██║   ██║  ██║██║  ██║╚███╔███╔╝███████╗
   ╚═╝   ╚═╝  ╚═╝╚═╝  ╚═╝ ╚══╝╚══╝ ╚══════╝
   intelligent tiered web scraping
```

**A local-first web scraping CLI.** Single static Go binary that routes each
URL through the cheapest engine that returns valid content — HTTP on easy
pages, headless Chromium on the SPAs that need it, `pdftotext` (+ optional
OCR) on PDFs — then emits clean markdown, structured extraction, and
per-page metadata as JSONL.

No API key. No runtime daemon. No Docker. `curl | sh` and you're done.

### Why trawl

- **Tiered routing with learning.** Each URL finds the cheapest engine
  that returns valid content; the router remembers per-host and reorders
  the ladder on the next run.
- **Single static binary, zero runtime deps.** `go install`, `curl | sh`,
  or drop the binary on a `$5` VPS. No CGO anywhere in the tree.
- **Persistent, resumable frontier.** SIGINT-safe BadgerDB-backed queue.
  `trawl resume <job-id>` picks up where crashes left off.
- **Polite by default.** robots.txt, per-domain rate limits, concurrency
  caps. Opt-out, not opt-in. Per-host overrides via YAML.
- **Content-type aware.** HTML → clean markdown (readability-stripped
  if you want), PDF → markdown via `pdftotext` (+ Tier 3 OCR for scans),
  schema extraction with fallback selectors and transforms.
- **JSONL everything.** Composes with `jq`, `grep`, and the rest of Unix
  without a custom parser.

### vs Firecrawl

trawl has feature parity with Firecrawl on the "clean content from a URL"
use case: scrape, crawl, map, markdown output, schema extraction, caching,
proxies, PDF + OCR.

- **What Firecrawl has that trawl doesn't:** LLM extraction, search
  integration, managed webhooks, hosted API.
- **What trawl has that Firecrawl doesn't:** single static binary, no
  API key, persistent resumable frontier, tiered routing with cross-job
  learning, robots-polite-by-default, JSONL composability.

If you want a managed service, use Firecrawl. If you want the tool running
locally on your laptop or a `$5` VPS, piping clean output into whatever
pipeline you already have, use trawl.

---

## Quickstart

```sh
# One URL, clean markdown out, page metadata auto-extracted
trawl scrape https://linear.app --format markdown --readability

# Batch of URLs from a file, resumable, CSV output
trawl batch urls.txt \
  --selector "title=h1" --selector "price=.price" \
  --output results.csv

# BFS-crawl an entire site, clean markdown from every page
trawl crawl https://example.com \
  --depth 2 --same-domain --limit 500 \
  --format markdown --readability \
  --output site.jsonl

# Enumerate every URL a site publishes (sitemap + HTML-crawl, deduped)
trawl map https://example.com > urls.txt
trawl batch urls.txt --output results.jsonl

# Structured extraction via YAML schema (nested fields, arrays of objects)
trawl scrape https://plato.stanford.edu/entries/kant/ \
  --schema docs/examples/sep-article.yaml \
  --format markdown --readability

# Iterate on selectors cheaply: opt-in content cache, 24h TTL
trawl batch urls.txt --selector "title=h1" --cache -o results.jsonl

# Full-page PNG screenshots for chromium-served pages
trawl scrape https://linear.app --tiers chromium --screenshot-dir shots/

# Desktop + mobile screenshots with explicit viewport sizing
trawl batch urls.txt --force-tier chromium --viewport 1440x900 --screenshot-dir shots/desktop/
trawl batch urls.txt --force-tier chromium --viewport 390x844 --screenshot-dir shots/mobile/

# Per-host politeness: slow-crawl SEP, normal speed elsewhere
trawl batch mixed-urls.txt --politeness docs/examples/politeness.yaml

# CSV with hybrid discovery: try pricing_url first, fall back to
# homepage + link follow on http_4xx / dns_failure
trawl batch companies.csv \
  --url-column pricing_url \
  --fallback-column homepage \
  --fallback-selector 'a[href*="pricing"]'

# Resume a job that was SIGINT'd mid-crawl
trawl resume <job-id>

# Run as an MCP server (Claude Code, Cursor, Codex, etc. — see docs/MCP.md)
trawl mcp
```

## MCP server

Trawl ships an MCP (Model Context Protocol) server so AI agents call
trawl as a first-class tool — typed args, structured results, no
shell-out parsing.

```jsonc
// .mcp.json (Claude Code) or ~/.cursor/mcp.json
{
  "mcpServers": {
    "trawl": { "command": "trawl", "args": ["mcp"] }
  }
}
```

Five tools: `trawl_scrape`, `trawl_batch` (≤50 URLs/call),
`trawl_crawl` (limit ≤500/call), `trawl_map` (≤5000/call),
`trawl_sitemap`. Above the per-call caps, the error message points
at the corresponding CLI subcommand. See [`docs/MCP.md`](docs/MCP.md)
for the design and full registration recipes.

## Install

**One-liner** (darwin/linux, amd64/arm64 — installs to `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/jeffdhooton/trawl/main/scripts/install.sh | sh
```

The installer pulls the latest tagged release, verifies the SHA256
checksum, extracts the binary to `INSTALL_DIR` (default
`~/.local/bin`), and prints a PATH advisory if needed. Customize via
`TRAWL_VERSION=vX.Y.Z` to pin, `INSTALL_DIR=/usr/local/bin` to
relocate, or `TRAWL_REPO=your/fork` to install from a fork.

**Via Go toolchain** (builds from source, keeps the binary's version
string as `dev`):

```sh
go install github.com/jeffdhooton/trawl/cmd/trawl@latest
```

**From a clone** (for development):

```sh
git clone https://github.com/jeffdhooton/trawl.git
cd trawl
go build ./cmd/trawl
```

Trawl has no CGO dependencies and produces a single static binary
suitable for copying onto a $5 VPS.

**Optional dependencies** (not bundled; trawl works without them):

- `poppler-utils` enables PDF → markdown extraction (see
  `docs/PDF.md`). Install via `brew install poppler` (macOS) or
  `apt install poppler-utils` (Debian/Ubuntu). Missing poppler =
  PDFs soft-fail with a clear install hint; HTML crawls are
  unaffected.
- `tesseract` enables `--ocr` for scanned PDFs. Install via
  `brew install tesseract` (macOS) or `apt install tesseract-ocr`
  (Debian/Ubuntu). Required only when `--ocr` is passed.

**Using [Claude Code](https://claude.com/claude-code)?** Drop the
vendored skill into your user skills directory so Claude routes
scrape-like requests to trawl automatically:

```sh
mkdir -p ~/.claude/skills/trawl
cp claude-skill/SKILL.md ~/.claude/skills/trawl/SKILL.md
```

See [`claude-skill/README.md`](claude-skill/README.md) for details.

## What it does

- **Tiered routing.** Each URL starts at HTTP (`net/http` + `goquery`);
  if the response is invalid (SPA shell, timeout, etc.) the router
  escalates to Chromium (`chromedp`). Invalid-final responses (404,
  DNS failure) short-circuit — chromium can't help there either.
- **HTTP retries with backoff.** Transient failures (429, 5xx, connection
  resets, timeouts) retry automatically with exponential backoff and
  ±25% jitter, capped at 10s. Permanent failures (4xx except 429, TLS
  cert errors, ctx cancellation) return immediately. `--retries` and
  `--retry-delay` tune the policy; chromium does not retry in v1.
- **Persistent frontier.** BadgerDB-backed queue survives SIGINT, machine
  restarts, and mid-crawl crashes. `trawl resume <job-id>` picks up
  where it left off.
- **Tier learning.** After the first run, trawl remembers which tier
  served each host successfully. Subsequent crawls skip the HTTP tier
  on known-SPA hosts, saving wasted work. Cache lives at
  `$TRAWL_HOME/tier-cache` and is cross-job by design.
- **BFS crawl mode.** `trawl crawl <seed> --depth N --same-domain
  --limit N` walks a site breadth-first, respecting a depth cap and a
  hard cap on URLs enqueued. Link discovery uses the same tiered
  pipeline as scrape, so SPA pages get chromium-rendered before their
  links are extracted. Composes with `--format markdown` to become
  "give me clean content from an entire site."
- **URL mapping.** `trawl map <url>` combines sitemap parsing with
  lightweight HTML link discovery (HTTP tier only) to produce a
  deduped list of URLs to stdout. Fast, composable with `trawl batch`.
- **Content extraction.** `--format markdown` runs an HTML→markdown
  converter; `--readability` strips nav/footer/ads before conversion
  (or before CSS extraction). Every record gets automatic page
  metadata: title, description, canonical URL, language, Open Graph,
  Twitter cards, JSON-LD structured data, published date.
- **Schema extraction.** `--schema sep-article.yaml` runs a nested,
  declarative extraction against the page. Supports `selector`, `attr`,
  `multiple` (arrays of strings or objects), and empty-selector
  self-reference inside nested contexts (needed for "array of objects
  where each object is a link's text + href"). YAML or JSON, strict
  parser catches typos. See `docs/examples/sep-article.yaml` for a
  working Stanford Encyclopedia of Philosophy schema.
- **Screenshots.** `--screenshot-dir dir/` captures a full-page PNG
  for every chromium-served row via chromedp's `captureBeyondViewport`.
  `--viewport WxH` (e.g. `1440x900`) controls the chromium window
  size, affecting responsive breakpoints and screenshot width.
  Deterministic filenames (`<sha256-of-url>.png`) so re-runs overwrite
  rather than accumulate. HTTP-served rows leave `metadata.screenshot_path`
  empty — zero cost on the happy path.
- **Content cache.** `--cache` opts into a persistent content cache
  keyed by canonical URL + tier. A cache hit short-circuits the tier
  loop without touching the live site. Default TTL 24h (tunable via
  `--cache-ttl`). `metadata.from_cache: true` on replay rows so
  downstream can distinguish live from cached. Lives at
  `$TRAWL_HOME/content-cache`.
- **Hybrid discovery.** CSV seeds can declare a primary URL and a
  fallback URL per row. When the primary fails with `http_4xx` or
  `dns_failure`, trawl re-routes through the fallback URL + a CSS
  selector to resolve the target, recovering rows that neither path
  alone would get.
- **Sitemap discovery.** `trawl sitemap <url>` fetches robots.txt,
  extracts `Sitemap:` directives, falls back to well-known paths,
  recursively walks `<sitemapindex>` files, handles gzip, dedupes
  URLs — stream-friendly for large sites.
- **Polite by default.** robots.txt, per-domain rate limits, and
  concurrency caps are opt-out, not opt-in. `--politeness <file.yaml>`
  overrides rate and concurrency for specific hosts via exact or
  `*.suffix` wildcard match (see `docs/examples/politeness.yaml`).
  `--ignore-robots` exists but logs a warning.
- **CSV / TSV output.** Extension-sniffed: `-o results.csv` or
  `-o results.tsv` writes flattened rows instead of JSONL. Default
  columns are a stable base set plus auto-discovered `extracted.*`
  keys from the first record; `--csv-columns` overrides with explicit
  dot-paths. Nested values get JSON-encoded inline so cells stay
  single-valued.
- **Proxy support.** `--proxy http://user:pass@host:port` routes all
  requests through a gateway proxy. `--proxy-file proxies.txt` loads
  a pool and assigns each target domain to a fixed proxy (per-domain-
  sticky rotation). Works on HTTP, uTLS, and chromium tiers. Proxied
  records are stamped with `metadata.evasion.proxy: true`.
- **PDF extraction.** PDFs served by a crawl target automatically get
  extracted to markdown via `pdftotext` (tried plain, escalated to
  `-layout` mode when the plain output is empty). Full `metadata.pdf`
  struct with page count, title, author, and per-page text. Opt-in
  OCR for scanned PDFs via `--ocr` (requires `tesseract` +
  `pdftoppm`, off by default). No CGO — shells out to the user's
  installed poppler-utils; soft-fails with install hint if missing.
  See `docs/PDF.md`.
- **Failure classification + stats.** Every job emits a `stats.json`
  with reachable/unreachable counts, per-category failure breakdown,
  per-tier latency, chromium escalation rate, and fallback yield.

## Commands

| Command             | What it does                                               |
| ------------------- | ---------------------------------------------------------- |
| `trawl scrape`      | Scrape one URL, emit one record                            |
| `trawl batch`       | Scrape a URL list (plain text, CSV, or TSV), resumable     |
| `trawl crawl`       | BFS-crawl a site from a seed URL                           |
| `trawl map`         | Enumerate URLs from a site (sitemap + HTML crawl)          |
| `trawl sitemap`     | Discover URLs from a site's sitemap(s) only                |
| `trawl resume`      | Resume an interrupted job by ID                            |
| `trawl proxy-test`  | Validate proxy connectivity before a long run              |
| `trawl mcp`         | Run as an MCP server (stdio) for AI agent tool-use         |
| `trawl version`     | Print version info                                         |

Run `trawl <command> --help` for the full flag surface.

## Output shape

Every record is one line of JSONL (or one CSV row). The stable fields:

```json
{
  "url": "https://example.com/pricing",
  "canonical_url": "https://example.com/pricing",
  "fetched_at": "2026-04-10T22:49:41Z",
  "tier": "http",
  "status_code": 200,
  "duration_ms": 384,
  "content_hash": "sha256:...",
  "extracted": {
    "title": "...",
    "plans": [
      { "name": "Free", "price": "$0" },
      { "name": "Plus", "price": "$10" }
    ]
  },
  "body": "# Pricing\n\n...",
  "body_format": "markdown",
  "metadata": {
    "content_type": "text/html; charset=utf-8",
    "body_bytes": 24815,
    "final_url": "https://example.com/pricing",
    "screenshot_path": "/tmp/shots/<sha256>.png",
    "from_cache": false,
    "page": {
      "title": "...",
      "description": "...",
      "canonical": "https://example.com/pricing",
      "language": "en",
      "published_at": "2026-02-14T09:30:00Z",
      "open_graph": { "title": "...", "image": "..." },
      "json_ld": [ { "@type": "Article", ... } ]
    }
  },
  "failure_category": "success"
}
```

Which fields are populated:

- `body` / `body_format` only when `--format` is set.
- `extracted` when `--selector` and/or `--schema` ran successfully.
- `metadata.page` unless `--no-metadata`.
- `metadata.screenshot_path` only when `--screenshot-dir` is set AND the
  chromium engine served the row.
- `metadata.from_cache: true` only on cache replay rows.

Failed records still get written, with `failure_category` set to one
of the classified buckets (`http_4xx`, `dns_failure`, `tls_error`,
`spa_shell`, `soft_block`, etc.) — easy to `jq`-filter for the real
failures. `soft_block` catches 200 OK challenge walls (Cloudflare
"Just a moment", Akamai, DataDome, etc.); when a later tier gets
through, the per-tier detection stays on the success record as
`metadata.soft_block` for forensic analysis.

## Example schemas and configs

See [`docs/examples/`](docs/examples/):

- [`sep-article.yaml`](docs/examples/sep-article.yaml) — nested schema
  for Stanford Encyclopedia of Philosophy entries (title, pubinfo,
  TOC, related entries, author, copyright). Verified against real
  `plato.stanford.edu/entries/kant/` during development.
- [`politeness.yaml`](docs/examples/politeness.yaml) — per-host rate
  and concurrency overrides with exact and `*.suffix` wildcard match.

## Design docs

- [`docs/ROADMAP.md`](docs/ROADMAP.md) — current phase, strategic gap
  analysis, in-scope/out-of-scope list. Start here.
- [`docs/SPEC.md`](docs/SPEC.md) — the original PRD. Architecture,
  non-goals, technology choices. Historical design reference;
  ROADMAP is the live source of truth.
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — log of architectural calls
  that deviate from SPEC, each with the data that drove the decision.
- [`docs/BENCHMARK.md`](docs/BENCHMARK.md) — operational playbook for
  running Phase 0 against the 7000-company test dataset.
- [`docs/EVASION.md`](docs/EVASION.md) — anti-detection / stealth
  design doc. Tiered opt-in model for sites that fight back, with
  explicit refusals for CAPTCHA solvers, credential bypass, and
  other identity-changing features.
- [`docs/RELEASING.md`](docs/RELEASING.md) — operational checklist
  for cutting a new release. Semver policy, the full pre-flight →
  tag → workflow → smoke-test procedure, and common gotchas.
- [`docs/TODO.md`](docs/TODO.md) — standing commitments and open
  papercuts.

## What trawl is not

Trawl is a general-purpose CLI for content-extraction and tiered
scraping. It is not a managed service, not an LLM framework, not a
proxy rotation toolkit, not a distributed crawler. For the list of
things deliberately kept out of scope — LLM-based extraction, search
integration, interactive actions, webhooks, pricing-aware logic — see
`docs/ROADMAP.md`'s "explicitly deferred or out of scope" section.

**Trawl is also not a bypass tool.** For sites that actively fight
back against automated traffic, trawl's design intent is a tiered
opt-in evasion model (realistic browser headers, chromium stealth
patches, optional TLS fingerprint forgery) with explicit refusals
for CAPTCHA-solving services, credential-based auth bypass, and DoS-
level rate patterns. See `docs/EVASION.md` for the considered stance
— that doc locks in the principled shape before any of it ships, so
the eventual implementation preserves trawl's polite-by-default
identity rather than drifting into an arms-race tool.

The Unix-pipeline answer for LLM extraction is: pipe `trawl scrape
... --format markdown` into whatever LLM tool you prefer. Trawl's job
ends at clean content.

## License

TBD.
