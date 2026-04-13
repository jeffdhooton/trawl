# CLAUDE.md — trawl

Working notes for Claude Code sessions on this repo. Keep it short;
`docs/ROADMAP.md` has the live phase status, `docs/SPEC.md` is the
original PRD, and `docs/DECISIONS.md` is the architectural decision
log.

## What this is

`trawl` is an intelligent tiered web scraping tool built in Go as a
standalone CLI + library. It routes each URL through the cheapest
engine that returns valid content. Persistent frontier, polite by
default, single static binary.

## Status (as of 2026-04-12)

**Shipped (v0.4.0):** everything below plus three new features on
2026-04-12: `--format json` (Body = JSON-serialized Extracted map),
v2 schema (fallback selectors via `SelectorSpec` custom unmarshaler
accepting string-or-list, five transform types: trim/regex/lowercase/
uppercase/split, version 1 backward-compatible, v2 features rejected
in v1 schemas), interactive actions (`internal/action` package,
`--action "click:.btn"` inline + `--actions file.yaml`, six verbs:
click/wait/scroll/type/sleep/evaluate, spliced into chromium pipeline
after page load before DOM capture, wired through JobConfig for
resume persistence, example at `docs/examples/hn-frontpage.yaml`).

**Previously shipped:** P0 (HTTP tier, batch/scrape/resume, persistent
frontier, politeness), P1 stage 1 (tiered router + Chromium engine),
hybrid discovery, sitemap parsing, per-domain tier learning, content
extraction (`--format html|markdown|json`, `--readability`, automatic
page metadata), BFS crawl, URL mapping, screenshot output, content
cache, schema extraction (v1 + v2), CSV/TSV output, HTTP retries
with backoff, per-host politeness, Tier 1 + Tier 2 evasion
(`--browser-like`, `--stealth`, `--user-agent`, `--no-jitter`),
Tier 3 evasion (`--tls-match chrome` via uTLS, now with full
HTTP/2 support via dual-transport `utlsRoundTripper` using
`golang.org/x/net/http2.Transport` — JA4 fingerprint is
indistinguishable from real Chrome).

**Working tiers:** HTTP (net/http + goquery) and Chromium (chromedp).
**Deferred:** Lightpanda — see DECISIONS.md for the decision rule.
Tier 4 (proxy rotation) — see `docs/PROXIES.md`. HTTP/2 SETTINGS
frame forging (EVASION.md §8.3) — only matters for detectors that
combine TLS + SETTINGS.

**Current direction:** Firecrawl gap analysis fully closed on
in-scope items. See `docs/ROADMAP.md` for next targets.

## Read these first

Any fresh session should skim these in order (5 minutes total):

1. `docs/ROADMAP.md` — current phase, gap analysis, in-scope/out-of-scope.
   Start here to know what's being built and why.
2. `docs/SPEC.md` — the PRD. Source of truth for architecture and
   non-goals.
3. `docs/DECISIONS.md` — the decision log. **Every architectural call
   that deviates from SPEC lives here with the data that drove it.**
4. `docs/BENCHMARK.md` §"Decision rule: does Lightpanda ship at all?"
   — the falsifiable rule that closed the Lightpanda question.
5. `docs/TODO.md` — standing commitments and open papercuts.

## Roadmap and priorities

See `docs/ROADMAP.md` for the current phase, the strategic gap
analysis against Firecrawl, the explicit in-scope / out-of-scope
list, and the scope reminder that keeps domain-specific logic out
of trawl's source tree. CLAUDE.md used to duplicate a priority list
here; ROADMAP.md is now the single source of truth so you only
have to update one file when direction changes.

**Note on Lightpanda:** do NOT build it without re-running Phase 0
and producing a chromium escalation rate ≥15% on a reachable sample
of n≥500 that isn't dominated by the easy subset. See the "caveat
the next session must know" in `docs/DECISIONS.md`.

## Hard constraints (do not relitigate)

- **No CGO.** Single static binary is the deploy story. Forces
  `modernc.org/sqlite` (not `mattn/go-sqlite3`). See SPEC §7.
- **Tech stack in SPEC §7 is decided.** If you strongly disagree,
  add a DECISIONS.md entry with data — don't silently swap.
- **v1 exclusions in SPEC §13 are deliberate.** No web UI, no
  scheduler, no plugin DSL, no CAPTCHA solving, no distributed
  mode. (Note: the original `no stealth` exclusion was revised
  2026-04-11 — stealth is now a tiered, opt-in feature set per
  `docs/EVASION.md`. CAPTCHA solving is still refused; see
  EVASION.md §6.1.)
- **Correctness > speed.** A fast scraper that returns empty DOMs is
  worse than a slow one that returns content.
- **Polite by default.** robots.txt, rate limits, concurrency caps
  are opt-out, not opt-in.

## Project layout

```
cmd/trawl/              cobra entrypoint, scrape/batch/crawl/map/sitemap/resume commands
scripts/install.sh      one-liner installer end users copy-paste (pulls latest release from GitHub)
.goreleaser.yaml        GoReleaser build matrix (darwin+linux × amd64+arm64, CGO off, ldflags version injection)
.github/workflows/      release.yml — CI workflow triggered on v* tag push, runs tests + GoReleaser
internal/cache/         BadgerDB-backed content cache (URL+tier → engine.Result, TTL)
internal/canonical/     URL canonicalization + tests
internal/engine/        HTTP + Chromium engines, Engine interface (Request.WantScreenshot)
internal/extract/       goquery CSS extractor + FirstLink / AllLinks resolvers
internal/failure/       Classify() — maps errors to discrete categories
internal/frontier/      BadgerDB-backed URL queue (blocking Next for crawl)
internal/output/        JSONL + CSV/TSV sinks, Record type, NewFile dispatcher
internal/politeness/    robots.txt cache + per-domain rate/concurrency + per-host HostRules
internal/action/        pre-scrape interactive actions (click/wait/scroll/type/sleep/evaluate)
internal/router/        tiered escalation loop (w/ content cache hook)
internal/schema/        YAML/JSON schema → nested structured extraction (v1 + v2)
internal/sitemap/       sitemap.xml discovery + index recursion + gzip
internal/stats/         per-job stats.json aggregator
internal/validity/      heuristics for "did this page actually load"
internal/version/       ldflags-settable version info
docs/ROADMAP.md         current phase status (source of truth)
docs/SPEC.md            original PRD — historical design intent
docs/BENCHMARK.md       operational playbook + Lightpanda decision rule
docs/EVASION.md         anti-detection / stealth design doc (tiered opt-in)
docs/RELEASING.md       release checklist (semver, tag flow, smoke tests)
docs/DECISIONS.md       architectural decision log
docs/TODO.md            standing commitments + open papercuts
docs/PROXIES.md         future P2 proxy planning (not yet committed in
                        all sessions — owned by Jeff, leave untouched)
docs/examples/          shippable schemas + configs (sep-article.yaml, hn-frontpage.yaml, politeness.yaml)
seed/                   benchmark test data (owned by Jeff, untouched)
```

## Build / test / install

```bash
go build ./...                # verify compile
go test ./...                  # full test sweep including chromium
go test -short ./...           # skip chromium tests (faster iteration)
go vet ./...                   # lint
go install ./cmd/trawl         # install to $GOPATH/bin — convention
                                # matches ~/workspace/orch
```

The binary expects `TRAWL_HOME` env var OR defaults to
`~/.trawl/jobs/<job-id>/` for state. Tests set `TRAWL_HOME` to a temp
dir via `withTrawlHome(t)` so they never touch the user's home.

## Writing style for this project

- Go idioms, not Java-in-Go. Small interfaces, accept-interfaces-
  return-structs, errors as values.
- Table-driven tests. Integration tests hit a local `httptest`
  server, not the public internet. Chromium tests gate on a
  `chromiumAvailable()` helper so they skip cleanly if Chrome isn't
  installed.
- Structured logs via zerolog. JSON for non-TTY, pretty for TTY.
  Use `.Str("elapsed", d.Round(ms).String())` for durations — do
  NOT use `.Dur()` because zerolog serializes it as a float that
  humans hate.
- No premature abstraction. The `Engine` interface exists because
  tiered routing demands it. Don't invent new abstractions without
  a concrete second implementation in hand.
- Commit discipline: each commit has a clear "why" in the body,
  not just "what." Follow the existing commit history style —
  structured paragraphs, not bullet-point kitchen sinks.
