# trawl

**Intelligent tiered web scraping tool.** Routes each URL through the cheapest engine that returns valid content: HTTP → Lightpanda → Chromium. Built in Go as a standalone CLI + library.

> Status: spec-only. No code yet. This directory exists so a fresh Claude Code instance can pick up the work.

---

## For the AI agent picking this up

1. **Read [`docs/SPEC.md`](docs/SPEC.md) first.** It is a complete PRD with architecture, tech-stack decisions, build phases, and a first-commit checklist. ~470 lines, self-contained, opinionated.
2. **Start with Phase P0** (§10 of the spec). It is the thinnest version that's actually useful — HTTP tier only, single-URL + URL list, JSONL output, BadgerDB frontier, resumable. Done when you can scrape a 1000-URL static-HTML list end-to-end, kill it mid-run, resume, and get correct output.
3. **Don't relitigate the decisions in §7 (tech stack) or §13 (excluded from v1).** They are deliberate. If you disagree strongly, write your reasoning into a `docs/DECISIONS.md` and surface it to the user — don't silently override.
4. **Pick a real name** before the first commit. `trawl` is a placeholder. Alternatives in the spec.
5. **No CGO.** Hard constraint. Single static binary is the deploy story.

## For humans reading this in a year

The pitch in one paragraph: most scraping tools force one engine choice — `requests`, Playwright, Scrapy — and live with the tradeoffs. The result is you either burn 100x the time running headless Chromium on plain HTML, or you ship a fast HTTP scraper that returns empty bodies on every SPA. Trawl auto-routes each URL to the cheapest engine that works (HTTP → Lightpanda → Chromium), learns per-domain which tier to start at, persists the frontier so crashes don't lose work, and produces structured output (JSONL/Parquet/SQLite/CSV).

## Why Go

Concurrency-heavy + I/O-bound + single-binary deploy = Go's sweet spot. Goroutines + channels handle 500-2000 in-flight requests on a single $5 VPS. The browser engines are subprocesses anyway, so language-level browser bindings don't matter. Full reasoning in spec Appendix A.

## Layout

```
trawl/
├── README.md          # this file
└── docs/
    └── SPEC.md        # full PRD — read this first
```

Once Phase P0 starts, expect the standard Go layout: `cmd/trawl/`, `internal/frontier/`, `internal/engine/`, `internal/extract/`, `internal/output/`.
