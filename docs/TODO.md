# trawl — follow-ups & papercuts

Non-blocking issues discovered during testing. Promote to a P1/P2 task when
one rises in priority.

---

## Extraction: empty match is indistinguishable from "no selectors"

**Severity:** low — UX papercut during selector debugging.

When `scrape`/`batch` is invoked with `--selector` but all selectors miss the
DOM, `extract.CSS` returns an empty map and `output.Record.Extracted` is
serialized with `omitempty`, so the `extracted` field is dropped entirely.
That makes the JSONL output identical to a record where no selectors were
requested — a user debugging "why didn't my selectors match?" has no signal.

**Repro:** run `trawl scrape https://news.ycombinator.com/item?id=40000001
--selector "title=.titleline > a"` (wrong selector for item pages). The
output record has no `extracted` key and no `error`.

**Possible fixes:**

- (a) Always emit `"extracted": {}` when selectors were requested but 0
  matched. Cheapest; downstream consumers that check for key presence keep
  working.
- (b) Record per-field status so a miss becomes `{"title": null}` instead of
  an omission. More informative but changes the schema.
- (c) Surface an `extraction_hits` integer in `metadata` so consumers can
  tell at a glance. Least disruptive.

Leaning toward (a) + (c) combined: always emit `extracted` when fields were
requested, and add `metadata.extraction_hits`. Wait for a second complaint
before deciding.

---
