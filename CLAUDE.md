# CLAUDE.md — trawl

Working notes for Claude Code sessions on this repo. Keep it short; the real spec lives in `docs/SPEC.md`.

## What this is

`trawl` is an intelligent tiered web scraping tool, built in Go as a standalone CLI + library. It routes each URL through the cheapest engine that returns valid content: HTTP → Lightpanda → Chromium. Per-domain tier learning, persistent frontier, polite by default, single static binary.

## Status

**Spec-only.** No code yet. `docs/SPEC.md` is a complete PRD (~540 lines). `README.md` is the elevator pitch. The first build session starts from Phase P0.

Before writing any code:
1. Read `docs/SPEC.md` end-to-end. It is self-contained.
2. Pick a real name (trawl is a placeholder — alternatives in SPEC §1).
3. Start with Phase P0 (SPEC §10). Do not skip ahead.

## Hard constraints (do not relitigate)

- **No CGO.** Single static binary is the deploy story. This forces `modernc.org/sqlite`, not `mattn/go-sqlite3`. See SPEC §7.
- **Tech stack in SPEC §7 is decided.** Go 1.23+, cobra, viper, goquery, chromedp, BadgerDB, zerolog, etc. If you strongly disagree, write `docs/DECISIONS.md` and surface it — don't silently swap.
- **v1 exclusions in SPEC §13 are deliberate.** No web UI, no scheduler, no plugin DSL, no stealth, no CAPTCHA solving, no distributed mode. Don't add them "while you're here."
- **Correctness > speed.** A fast scraper that returns empty DOMs is worse than a slow one that returns content (SPEC §2).
- **Polite by default.** robots.txt, rate limits, concurrency caps are opt-out, not opt-in.

## Current phase: P0

The thinnest useful version (SPEC §10). HTTP tier only, BadgerDB frontier, `trawl scrape`/`trawl batch`, JSONL output, resumable. Done when a 1000-URL static-HTML list can be scraped, killed mid-run, resumed, and produce correct output.

First commit checklist is in SPEC §14.

## Project layout (once code lands)

Standard Go layout expected:
```
cmd/trawl/            # cobra entrypoint
internal/frontier/    # BadgerDB-backed URL queue
internal/canonical/   # URL canonicalization
internal/engine/      # HTTP, Lightpanda, Chromium engines
internal/extract/     # CSS/XPath/JSONPath extractors
internal/output/      # JSONL/CSV/Parquet/SQLite sinks
docs/SPEC.md          # source of truth
```

## Open questions (SPEC §12)

These are unresolved on purpose. When you hit one, decide, document it, move on:
- Project layout flavor (flat vs standard)
- Job ID format (UUID vs timestamp+slug)
- BadgerDB default location (`~/.trawl/jobs/<job_id>/` is fine)
- Lightpanda binary management (recommendation: `trawl install lightpanda`)
- Dead-letter / retry policy

## Build / test commands

_None yet — no code. Once `go.mod` exists, expect the standard Go toolchain: `go build ./...`, `go test ./...`, `go vet ./...`. Update this section with the real commands when they stabilize._

## Writing style for this project

- Go idioms, not Java-in-Go. Small interfaces, accept-interfaces-return-structs, errors as values.
- Table-driven tests. Integration tests hit a local `httptest` server, not the public internet.
- Structured logs via zerolog. JSON for non-TTY, pretty for TTY.
- No premature abstraction. The `Engine` interface exists because tiered routing demands it (SPEC §8) — don't invent others without a reason.
