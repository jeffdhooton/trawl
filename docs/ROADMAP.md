# trawl — roadmap

**Current phase:** content extraction (not yet started)
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
| HTML → clean markdown output                       | ❌           | yes      | small   |
| Metadata extraction (title, OG, canonical, lang)   | ❌           | yes      | small   |
| Boilerplate/readability stripping                  | ❌           | yes      | small   |
| BFS crawl mode (`--depth N --same-domain`)         | ❌           | yes      | medium  |
| URL mapping (fast link discovery, no fetch)        | partial     | yes      | small   |
| Screenshot output                                  | ❌           | yes      | small   |
| Schema-based structured extraction (JSON/YAML)     | ❌           | yes      | medium  |
| Interactive actions (click, scroll, wait, execJS)  | ❌           | debatable | large |
| LLM extraction                                     | ❌           | **no**   | —       |
| Proxy rotation as a core feature                   | ❌           | P2 only  | large   |
| Search integration                                 | ❌           | **no**   | —       |
| Webhooks / async API                               | ❌           | **no**   | —       |
| Content caching                                    | partial     | yes      | medium  |

**What trawl has that Firecrawl doesn't:** single static binary,
no API key, persistent resumable frontier, tiered routing with
learning, JSONL composability, robots-polite-by-default. These are
real differentiators — do not throw them away chasing parity.

---

## Phasing

Phases are ordered by marginal value on already-built infrastructure,
not by feature glamour. Each phase should feel like it unlocks a new
use case, not like it sprinkles polish.

### Phase: content extraction (CURRENT)

**Goal:** turn `trawl scrape <url>` into "give me clean content from
a URL" — the 90% Firecrawl use case.

**Bundle:**
1. HTML → clean markdown via `github.com/JohannesKaufmann/html-to-markdown`
   (pure-Go, CGO-free). New `--format markdown|html|text` flag; default
   stays `html` so existing callers don't break.
2. Automatic metadata extraction on every record: title, description,
   canonical URL, language, Open Graph tags, published date where
   parseable. Lives under `metadata.page` in the output record. No
   configuration — it's always on and cheap.
3. Boilerplate removal via `github.com/go-shiori/go-readability` (pure
   Go, actively maintained). Opt-in via `--readability` flag. Strips
   nav, footer, ads before markdown conversion or CSS extraction.

**Why this bundle, not piecemeal:** each piece alone is marginally
useful; together they unlock the new use case. They all operate on
the HTML body the router already fetches — no new plumbing. The
output record already has a `Metadata` struct with room for a `Page`
sub-object. Testable end-to-end with httptest, no Chromium needed.

**Exit criteria:** `trawl scrape https://linear.app/pricing --format
markdown --readability` returns clean markdown with page metadata
populated, comparable in quality to `curl https://api.firecrawl.dev/
v1/scrape -d '{url: ..., formats: [markdown]}'`.

### Phase: BFS crawl mode

**Goal:** finish the SPEC's long-standing "crawl" command. Given a
seed URL, walk same-domain links breadth-first up to a depth or URL
cap, emit each page through the same scrape pipeline.

**Shape:** `trawl crawl <url> --depth N --same-domain --limit 1000`.
Reuses the persistent frontier (workers block on empty queue instead
of exiting). Combines trivially with the content-extraction phase to
become "give me clean content from an entire site."

**Dependencies:** content extraction phase shipped first, so the
output of a crawl is immediately useful.

### Phase: screenshot output

**Goal:** surface the visual evidence that's already rendered during
a chromium fetch.

**Shape:** `--screenshot <path>` flag on scrape/batch/crawl. When the
chromium engine serves a page, it captures a PNG and writes it
alongside (or to a path template). Zero work in the HTTP-served
case. chromedp already supports this; the engine just needs to
surface the bytes.

### Phase: URL mapping

**Goal:** upgrade `trawl sitemap` into a more general "give me all
the URLs on this site" primitive. Combines sitemap parsing with HTML
link discovery for sites that don't publish a sitemap.

**Shape:** `trawl map <url>` — fast, no content fetch, no extraction.
Output is a URL list to stdout, composable with batch.

### Phase: schema-based structured extraction

**Goal:** finish CLAUDE.md's priority #4. YAML/JSON schema file
drives nested CSS extraction, letting users describe "pricing plans
have a name, price, and feature list" declaratively.

**Still blocked on:** a real consumer. Ship this when someone
actually has a schema they want to feed in, not before.

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
- **Content caching beyond frontier dedup.** The frontier already
  dedupes URLs. Caching response bodies keyed by content hash would
  help re-runs, but no one has asked for it yet.

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
