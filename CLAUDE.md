# CLAUDE.md — trawl

Working notes for Claude Code sessions on this repo. Keep it short; the
real spec lives in `docs/SPEC.md` and the live decision log in
`docs/DECISIONS.md`.

## What this is

`trawl` is an intelligent tiered web scraping tool built in Go as a
standalone CLI + library. It routes each URL through the cheapest
engine that returns valid content. Persistent frontier, polite by
default, single static binary.

## Status (as of 2026-04-10)

**Shipped:** P0 (HTTP tier, batch/scrape/resume, persistent frontier,
politeness), P1 stage 1 (tiered router + Chromium engine), hybrid
discovery (`--fallback-column` + `--fallback-selector` on http_4xx /
dns_failure), sitemap parsing (`trawl sitemap <url>`, library at
`internal/sitemap`), per-domain tier learning (`internal/tierlearn`,
persistent cache at `$TRAWL_HOME/tier-cache`), content extraction
(`--format html|markdown`, `--readability`, automatic page metadata
with Open Graph + Twitter + JSON-LD + published_at at
`internal/extract/{metadata,markdown,readability}.go`).

**Working tiers:** HTTP (net/http + goquery) and Chromium (chromedp).
**Deferred:** Lightpanda — see DECISIONS.md for the decision rule
and the three data points (11.4% → 4.4% → 14.08% escalation).

**Current direction:** BFS crawl mode — `trawl crawl <url> --depth N
--same-domain --limit N`. Reuses the persistent frontier with workers
blocking on empty queue instead of exiting. Composes with the
content-extraction phase to become "give me clean markdown from an
entire site." See `docs/ROADMAP.md` for the phased plan and the
explicit out-of-scope list.

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
  scheduler, no plugin DSL, no stealth, no CAPTCHA solving, no
  distributed mode.
- **Correctness > speed.** A fast scraper that returns empty DOMs is
  worse than a slow one that returns content.
- **Polite by default.** robots.txt, rate limits, concurrency caps
  are opt-out, not opt-in.

## Project layout

```
cmd/trawl/              cobra entrypoint, batch/scrape/resume commands
internal/canonical/     URL canonicalization + tests
internal/engine/        HTTP + Chromium engines, Engine interface
internal/extract/       goquery CSS extractor + FirstLink resolver
internal/failure/       Classify() — maps errors to discrete categories
internal/frontier/      BadgerDB-backed URL queue
internal/output/        JSONL sink + Record type
internal/politeness/    robots.txt cache + per-domain rate/concurrency
internal/router/        tiered escalation loop
internal/sitemap/       sitemap.xml discovery + index recursion + gzip
internal/stats/         per-job stats.json aggregator
internal/validity/      heuristics for "did this page actually load"
internal/version/       ldflags-settable version info
docs/SPEC.md            the PRD
docs/BENCHMARK.md       operational playbook + Lightpanda decision rule
docs/DECISIONS.md       architectural decision log
docs/TODO.md            standing commitments + open papercuts
docs/PROXIES.md         future P2 proxy planning (not yet committed in
                        all sessions — owned by Jeff, leave untouched)
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
