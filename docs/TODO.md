# trawl — follow-ups & papercuts

Non-blocking issues discovered during testing. Promote to a P1/P2 task when
one rises in priority.

---

## Standing commitments (not papercuts — falsifiable plans)

- **Lightpanda decision is deferred until Phase 0 benchmark data exists.**
  See `docs/BENCHMARK.md` §"Decision rule: does Lightpanda ship at all?" for
  the thresholds and the non-negotiable "same-day" Phase 0 trigger. The
  original P1 stage 2 plan (build Lightpanda now) is on hold pending data —
  **not abandoned**. If you're reading this and Phase 0 has run, go read the
  stats.json and execute the decision rule, don't punt it forward.

---

## Open papercuts

_No open items._

Fixed:

- **Extraction: empty match indistinguishable from "no selectors"** — added
  `metadata.extraction.{fields,hits}` so consumers can
  `jq 'select(.metadata.extraction.hits == 0)'` to find all-miss cases.
  Surfaced by HN's front page having no `<h1>` at all on a real-world
  test run.
