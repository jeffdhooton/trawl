# trawl — follow-ups & papercuts

Non-blocking issues discovered during testing. Promote to a P1/P2 task when
one rises in priority.

---

## Standing commitments (not papercuts — falsifiable plans)

- **Lightpanda decision stays closed pending new Phase 0 data.** Three
  runs have now executed the decision rule from `docs/BENCHMARK.md`:
  Run A (n=500, pricing_url only, 11.4% chromium rate), Run B (n=500,
  homepage + follow-link, 4.4%), Run C (n=999, hybrid discovery,
  14.08%). All three fall below the 15% reopen threshold, but Run C
  came within 0.9 points and the trend across runs is directional
  toward the threshold. See `docs/DECISIONS.md` for the full three-
  addendum entry, including the "caveat the next session must know"
  about measurable-population bias. The rule auto-reopens if a future
  run shows ≥15% chromium escalation on n≥500 reachable — do not
  build Lightpanda before that evidence lands.

- **Phase 0 re-run with BFS crawl + richer discovery is still pending.**
  BFS crawl mode shipped 2026-04-10 (`trawl crawl`), as did URL mapping
  (`trawl map`) and the `--schema` extraction path. None of them have
  been re-run against the 1000-row `seed/companies.csv` subset to see
  whether the expanded measurable population moves the chromium
  escalation rate past 15%. Next Lightpanda data point belongs here.
  See Run C in `docs/DECISIONS.md` for the baseline (14.08%, n=355) to
  beat.

---

## Open papercuts

_No open items._

Fixed:

- **Extraction: empty match indistinguishable from "no selectors"** — added
  `metadata.extraction.{fields,hits}` so consumers can
  `jq 'select(.metadata.extraction.hits == 0)'` to find all-miss cases.
  Surfaced by HN's front page having no `<h1>` at all on a real-world
  test run.
