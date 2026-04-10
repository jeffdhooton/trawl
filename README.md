# trawl

**Intelligent tiered web scraping.** A single Go binary that routes each
URL through the cheapest engine that returns valid content, remembers
which tier worked per host, persists the frontier so long crawls
survive crashes, and produces clean markdown + metadata ready for
downstream pipelines.

No API key. No runtime dependency. No Docker required. `go install` and
you're done.

---

## Quickstart

```sh
# One URL, clean markdown out, page metadata auto-extracted
trawl scrape https://linear.app --format markdown --readability

# Batch of URLs from a file, resumable
trawl batch urls.txt --selector "title=h1" --selector "price=.price" \
  --output results.jsonl

# Discover URLs from a site's sitemap(s)
trawl sitemap https://stripe.com > stripe-urls.txt

# CSV with hybrid discovery: try pricing_url first, fall back to
# homepage + link follow on http_4xx / dns_failure
trawl batch companies.csv \
  --url-column pricing_url \
  --fallback-column homepage \
  --fallback-selector 'a[href*="pricing"]'

# Resume a job that was SIGINT'd mid-crawl
trawl resume <job-id>
```

## Install

```sh
go install github.com/jeffdhooton/trawl/cmd/trawl@latest
```

Or clone and build:

```sh
git clone https://github.com/jeffdhooton/trawl.git
cd trawl
go build ./cmd/trawl
```

Trawl has no CGO dependencies and produces a single static binary
suitable for copying onto a $5 VPS.

## What it does

- **Tiered routing.** Each URL starts at HTTP (`net/http` + `goquery`);
  if the response is unvalid (SPA shell, timeout, etc.) the router
  escalates to Chromium (`chromedp`). Invalid-final responses (404,
  DNS failure) short-circuit — chromium can't help there either.
- **Persistent frontier.** BadgerDB-backed queue survives SIGINT, machine
  restarts, and mid-crawl crashes. `trawl resume <job-id>` picks up
  where it left off.
- **Tier learning.** After the first run, trawl remembers which tier
  served each host successfully. Subsequent crawls skip the HTTP tier
  on known-SPA hosts, saving wasted work. Cache lives at
  `$TRAWL_HOME/tier-cache` and is cross-job by design.
- **Content extraction.** `--format markdown` runs an HTML→markdown
  converter; `--readability` strips nav/footer/ads before conversion
  (or before CSS extraction). Every record gets automatic page
  metadata: title, description, canonical URL, language, Open Graph,
  Twitter cards, JSON-LD structured data, published date.
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
  concurrency caps are opt-out, not opt-in. `--ignore-robots` exists
  but logs a warning.
- **Failure classification + stats.** Every job emits a `stats.json`
  with reachable/unreachable counts, per-category failure breakdown,
  per-tier latency, chromium escalation rate, and fallback yield.

## Commands

| Command         | What it does                                               |
| --------------- | ---------------------------------------------------------- |
| `trawl scrape`  | Scrape one URL, emit one JSONL record                      |
| `trawl batch`   | Scrape a URL list (plain text, CSV, or TSV), resumable     |
| `trawl resume`  | Resume an interrupted job by ID                            |
| `trawl sitemap` | Discover URLs from a site's sitemap(s)                     |
| `trawl version` | Print version info                                         |

Run `trawl <command> --help` for the full flag surface.

## Output shape

Every record is one line of JSONL. The stable fields:

```json
{
  "url": "https://example.com/pricing",
  "canonical_url": "https://example.com/pricing",
  "fetched_at": "2026-04-10T22:49:41Z",
  "tier": "http",
  "status_code": 200,
  "duration_ms": 384,
  "content_hash": "sha256:...",
  "extracted": { "title": "...", "price": "..." },
  "body": "# Pricing\n\n...",
  "body_format": "markdown",
  "metadata": {
    "content_type": "text/html; charset=utf-8",
    "body_bytes": 24815,
    "final_url": "https://example.com/pricing",
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

`body` and `body_format` are only populated when `--format` is
explicitly set. `metadata.page` is populated unless `--no-metadata`.
Failed records still get written, with `failure_category` set to one
of the classified buckets (`http_4xx`, `dns_failure`, `tls_error`,
`spa_shell`, etc.) — easy to `jq`-filter for the real failures.

## Design docs

- [`docs/ROADMAP.md`](docs/ROADMAP.md) — current phase, strategic gap
  analysis, in-scope/out-of-scope list. Start here.
- [`docs/SPEC.md`](docs/SPEC.md) — the original PRD. Architecture,
  non-goals, technology choices. Canonical design reference.
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — log of architectural calls
  that deviate from SPEC, each with the data that drove the decision.
- [`docs/BENCHMARK.md`](docs/BENCHMARK.md) — operational playbook for
  running Phase 0 against the 7000-company test dataset.
- [`docs/TODO.md`](docs/TODO.md) — standing commitments and open
  papercuts.

## What trawl is not

Trawl is a general-purpose CLI for content-extraction and tiered
scraping. It is not a managed service, not an LLM framework, not a
proxy rotation toolkit, not a distributed crawler. For the list of
things deliberately kept out of scope — LLM-based extraction, search
integration, interactive actions, webhooks, pricing-aware logic — see
`docs/ROADMAP.md`'s "explicitly deferred or out of scope" section.

The Unix-pipeline answer for LLM extraction is: pipe `trawl scrape
... --format markdown` into whatever LLM tool you prefer. Trawl's job
ends at clean content.

## License

TBD.
