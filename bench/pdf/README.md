# bench/pdf/ — PDF engine exercise harness

Smoke-bench the v0.7.0 PDF engine against real-world URLs. Runs the same
target list twice — once without `--ocr`, once with — and cross-
references the results so you can see which URLs needed which tier.

## Running

```bash
# Install trawl first (or set TRAWL_BIN=/path/to/trawl).
# Requires poppler-utils for Tier 1/2; tesseract + pdftoppm for Tier 3.

# Default: bench the committed targets.txt
bench/pdf/run.sh

# Custom target list
bench/pdf/run.sh path/to/my-urls.txt
```

Results land in `bench/pdf/results-<timestamp>/`:

```
urls.txt           parsed URL list (comments stripped)
categories.json    expected category per URL
no-ocr.jsonl       Run 1 output (no --ocr set)
with-ocr.jsonl     Run 2 output (--ocr set, Tier 3 available)
per-url.json       per-URL outcome breakdown
stats.json         aggregated machine-readable stats
```

## What gets exercised

| Run | Flags | What it validates |
|-----|-------|-------------------|
| 1   | none  | Tier 1 (`pdftotext`) and Tier 2 (`-layout`) work end-to-end |
| 2   | `--ocr --pdf-max-pages 10` | Tier 3 (`pdftoppm` + `tesseract`) activates on scanned PDFs |

Cross-referencing:

- **Text-layer canaries** (arXiv, IRS forms) should `success` in both runs
  with `extractor_tier=pdftotext`. If Run 1 fails but Run 2 succeeds,
  either the text-layer has corrupted or our stub-detection threshold
  is off.
- **OCR canaries** (scanned docs) should fail Run 1 with
  `extraction_failed` and succeed Run 2 with `extractor_tier=tesseract`
  and `used_ocr=true`.
- **Mix targets** (govinfo CFR older volumes) might succeed in either
  run; the report just records which tier served.

## Interpreting stats.json

```json
{
  "totals": { "urls": 8, "ocr_available": true },
  "no_ocr_outcomes":   { "success": 7, "extraction_failed": 1 },
  "with_ocr_outcomes": { "success": 8 },
  "tier_distribution": {
    "no_ocr":   { "pdftotext": 7 },
    "with_ocr": { "pdftotext": 7, "tesseract": 1 }
  },
  "ocr_activated": 1,   // Run 2 records with tier=tesseract
  "ocr_rescued":   1    // failed in Run 1, succeeded in Run 2
}
```

`ocr_rescued > 0` is the signal that Tier 3 wiring actually works
against real scanned content. If `ocr_rescued == 0` and you expected
OCR targets in your list, check whether those URLs actually serve PDFs
(some archives serve HTML viewer pages that *link* to PDFs — trawl
won't chase those without `--crawl`).

## Adding your own targets

`targets.txt` uses an inline-comment convention for categories:

```
https://arxiv.org/pdf/1706.03762  # TL  Attention Is All You Need
https://www.example.com/scan.pdf  # OCR WWII telegram
https://www.example.com/mixed.pdf # MIX  older CFR volume
```

Categories (`TL` / `OCR` / `MIX`) are expectations for the post-run
report, not hard assertions. URLs without an inline category get `?`
and the report still works.

## Why this isn't a pass/fail gate

Unlike `bench/phase0/` (which drives the Lightpanda reopen threshold),
this bench is a **smoke harness**, not a statistical decision
instrument. A small URL list is fine because we're validating the
engine's correctness against diverse content shapes, not measuring
escalation rates that need n≥500 to be meaningful. If a future
consumer reports PDF-specific behavior (e.g. "Tier 2 produces garbage
on multi-column academic papers"), extend `targets.txt` with repro URLs
and let the bench track that shape going forward.

## Dependency notes

- Without `pdftotext`: every URL fails Run 1 with
  `failure_category=pdf_tooling_missing`. The script still runs and
  reports — useful for validating the soft-fail path.
- Without `tesseract` or `pdftoppm`: Run 2 is skipped with a warning;
  Run 1 still executes.
- `python3` and `jq` are used for target parsing and stats aggregation.
  Same deps as `bench/phase0/`.
