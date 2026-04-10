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

**P0 shipped, P1 stage 1 shipped, benchmark-driven stage 2 mostly
shipped.** The binary compiles, installs via `go install
./cmd/trawl`, and routes real traffic against the 500-row pricing
page benchmark. Two full Phase 0 runs have produced stats.json
artifacts.

**Working tiers:** HTTP (net/http + goquery) and Chromium (chromedp).
**Deferred:** Lightpanda — see the decision note below.
**Deferred:** general BFS crawl — see next-step priorities below.

## Read these first

Any fresh session should skim these in order (5 minutes total):

1. `docs/SPEC.md` — the PRD. Source of truth for architecture and
   non-goals.
2. `docs/DECISIONS.md` — the decision log. **Every architectural call
   that deviates from SPEC lives here with the data that drove it.**
3. `docs/BENCHMARK.md` §"Decision rule: does Lightpanda ship at all?"
   — the falsifiable rule that closed the Lightpanda question.
4. `docs/TODO.md` — standing commitments and open papercuts.

## Where we are: the bottleneck pivot

**The current bottleneck is pricing-page discovery (~65% miss rate
on real SaaS homepages), NOT engine speed.** Two independent Phase 0
runs both showed the chromium escalation rate below the 15% reopen
threshold (11.4% on direct pricing_url, 4.4% on homepage +
follow-link). The Lightpanda work is closed pending discovery
improvements that change the measurable population — see
`docs/DECISIONS.md` for the full entry including the reopen caveat.

**This matters for prioritization:** structural improvements to
discovery (sitemap parsing, hybrid CSV→crawl fallback) are worth
more than incremental improvements to extraction or routing.

## Next-step priorities (in order)

Reordered 2026-04-10 after the follow-link experiment revealed
discovery is the real problem. Rationale: prefer structural fixes
(change what the system can find at all) over incremental ones
(make the existing approach 20% better).

**Scope reminder before adding anything here:** trawl is a
general-purpose tiered scraping tool. The 7000-company seed CSV is a
**test dataset** for measuring engine efficacy, not product data.
Domain-specific logic (pricing-aware selectors, SaaS-shaped heuristics,
anything that treats the seed as real input) belongs in whatever
caller/harness runs Phase 0 — not inside trawl. If a proposed priority
couldn't be justified to a non-SaaS user scraping something else, it
doesn't belong in this list.

1. **Hybrid discovery** — shipped 2026-04-10 (commit `72a25b6`).
   `--fallback-column` + `--fallback-selector` + trigger-gated retry
   on http_4xx / dns_failure. Run C data in DECISIONS.md.
2. **Sitemap parsing** — highest-yield general discovery primitive.
   `robots.txt` Sitemap: directives + well-known paths + sitemap-index
   recursion + gzip. In-scope because any scraping job benefits,
   not just pricing discovery. Probable shape: library package plus
   a `trawl sitemap <url>` subcommand that prints URLs to stdout
   (composable with `trawl batch -`).
3. ~~Richer pricing-link selector library~~ — **not a trawl concern.**
   Trawl already accepts arbitrary CSS via `--fallback-selector`; the
   caller passes whatever patterns it wants. If there's a pricing-
   specific selector library to build, it lives in the benchmark
   harness, not in this repo. The Run C data showed fallback yield
   was bottlenecked on selector quality, but that's a caller problem.
4. **YAML extract config with nested selectors** — general-purpose
   extraction config. Still blocked by "no consumer needs it yet."
5. **Full BFS crawl mode** — general-purpose depth-N traversal.
   Deferred until something concretely needs it.

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
