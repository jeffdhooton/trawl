# trawl — follow-ups & papercuts

Non-blocking issues discovered during testing. Promote to a P1/P2 task when
one rises in priority.

---

_No open items._

Fixed:

- **Extraction: empty match indistinguishable from "no selectors"** — added
  `metadata.extraction.{fields,hits}` so consumers can
  `jq 'select(.metadata.extraction.hits == 0)'` to find all-miss cases.
  Surfaced by HN's front page having no `<h1>` at all on a real-world
  test run.
