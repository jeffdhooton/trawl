# Trawl — PDF Engine Design

**Status:** SHIPPED 2026-04-14 (v0.7.0). Tiers 1 (pdftotext), 2
(`pdftotext -layout`), and 3 (tesseract OCR via pdftoppm) all land
in the initial cut. Everything in this doc below the Phase 8 release
reflects what's actually in the code.
**Audience:** future contributors building the engine (primary), and
operators who want to understand why trawl's PDF story looks the way
it does (secondary).
**Related:** `docs/SPEC.md` §7 (no-CGO constraint), `docs/ROADMAP.md`
(Firecrawl gap analysis — PDF parsing is the only remaining content-
extraction gap). This document is the analogue of `docs/EVASION.md`
for PDFs: tiered, opt-in, with a decision rule that keeps scope
honest.

---

## 1. Why this doc exists

Firecrawl shipped "Fire-PDF" in April 2026 — a Rust-based PDF parser
with layout-aware extraction, OCR fallback, and markdown output. It's
the last content-extraction capability trawl lacks in the Firecrawl
gap analysis. The question is not whether trawl should handle PDFs —
any crawl of a research, government, or policy site will hit
`application/pdf` responses and today's behavior (return the raw
bytes as a body) is wrong for every downstream consumer. The question
is *how much* PDF parsing trawl should do, and whether to do it in-
process or by shelling out.

The short answer: **trawl is a URL-in, clean-markdown-out tool**, and
PDFs are just another content type it should hand back as markdown.
It is not, and should not become, a general PDF analysis toolkit.
Everything in this doc flows from that positioning.

### Why we're not building a standalone Rust competitor

A serious in-process PDF parser with layout awareness and OCR
requires C/C++ libraries (`pdfium`, `mupdf`, `tesseract`) under
the hood. Go's native PDF libraries (`ledongthuc/pdf`, `pdfcpu`,
`unipdf`) are weak on multi-column and tables and have no OCR
story without CGO. Enabling CGO breaks trawl's single-static-binary
deploy promise — a hard constraint from SPEC §7 that we do not
relitigate.

We considered (and declined, same session) building a standalone
Rust PDF tool alongside trawl. The OSS landscape already has
`marker`, `docling`, `ocrs`, MinerU, and `pdf-extract` — all mature,
most with active teams behind them. A new Rust PDF tool is a fun
project but a bad allocation: trawl moves forward this month with a
shell-out engine, and if a real need for an in-process Rust parser
ever shows up, the engine's shell-out interface is exactly where
we'd swap it in.

---

## 2. The PDF extraction landscape

PDFs fall into three extraction categories, each with very different
costs and tooling needs.

### 2.1 Text-layer PDFs

The PDF has an embedded text stream. Extraction is a pure structural
walk of the PDF object graph — no vision, no ML, no OCR. This is the
dominant case: research papers, government docs, annual reports,
exported docs from Word/Google Docs. `pdftotext` from poppler-utils
handles these in milliseconds.

**What you get:** text content with reasonable reading order (most
of the time). Images are lost. Tables become whitespace-aligned
columns or run-together text depending on layout.

### 2.2 Layout-aware extraction

Same text-layer PDFs, but you want tables reconstructed as tables,
multi-column layout preserved as columns not interleaved paragraphs,
and headings detected by font-size heuristics. `pdftotext -layout`
gets you ~70% of the way using bounding-box analysis; real layout-
aware tools (`marker`, `docling`) use ML models to do it properly.

**What you get:** markdown with tables, headings, and column-safe
reading order. Costs more CPU. Some tools require GPU for reasonable
throughput.

### 2.3 Scanned / image-only PDFs

No text stream at all — the PDF is a container for page images. A
structural walk returns empty strings. Extraction requires OCR:
rasterize pages, run tesseract (or a neural OCR like `ocrs`), stitch
the output back together. Expensive: seconds per page, not
milliseconds.

**What you get:** approximate text with OCR errors, usually no
layout. Useful but lossy.

**Why this taxonomy matters:** each category has a different cost/
quality tradeoff, and the right engine design escalates through them
the same way trawl's main router escalates HTTP → Chromium. Cheap
extraction first; OCR only when the cheap path comes back empty and
the user has opted in.

---

## 3. Scope and non-goals

### In scope for v1

- Detect `application/pdf` responses from the HTTP engine and route
  them to the PDF engine instead of treating the body as HTML.
- Extract text from text-layer PDFs via `pdftotext` (poppler-utils).
- Preserve basic layout (columns, tables-as-whitespace) via
  `pdftotext -layout` as a second tier.
- Optional OCR fallback behind `--ocr` flag (off by default) via
  `tesseract`.
- Extract PDF metadata (title, author, page count, creation date)
  and surface it at `metadata.pdf`.
- Emit output as markdown by default (matching `--format markdown`
  for HTML), with JSON including the metadata fields.
- Soft-fail with clear install instructions when `pdftotext` isn't
  on the PATH. Trawl stays useful for HTML even without PDF tooling.

### Explicit non-goals

- **In-process PDF parsing.** No CGO, no vendored C libraries, no
  pure-Go PDF parser (they're not good enough). Shell-out to user-
  installed binaries is the whole story.
- **Structured table extraction beyond what `-layout` produces
  naturally.** Tables-as-markdown-pipes is a v2 question, gated on a
  real consumer asking.
- **Image extraction.** Figures in the PDF are discarded. Consumers
  wanting images can reach for `pdfimages` themselves.
- **Form field extraction.** PDF forms are a different problem shape
  and no consumer has asked.
- **Password-protected PDFs.** Fail with a clear error. Cracking
  encryption is out of scope; accepting a user-supplied password is
  a v2 feature if anyone needs it.
- **Marker / docling integration.** Deferred. They're both strong
  candidates for a future Tier 3 when a consumer shows us a PDF
  where `pdftotext -layout` produces garbage that `marker` handles
  well. Until then, the dependency surface (Python, PyTorch, model
  weights) isn't justified.
- **Structured extraction via `--schema`.** Schema is HTML-shaped
  (CSS selectors). PDFs don't have a selector model. If structured
  extraction from PDFs ever matters, it's a separate feature
  (probably page-region rectangles) and not part of the PDF engine.

The v1 line is: text and basic layout, cleanly, with graceful
degradation. Everything else waits for a consumer ask.

---

## 4. Architecture

### 4.1 Dispatch

The PDF engine is not a new tier in the main HTTP → Chromium ladder.
It's a **parallel branch** dispatched by content type. Flow:

```
URL
 │
 ▼
Router → HTTP engine fetches
                │
                ▼
        Content-Type: application/pdf?
         ┌──────┴───────┐
         │ no            │ yes
         ▼               ▼
   continue as HTML   PDF engine takes the body,
   (current behavior) runs internal tier loop,
                      returns a Result with
                      markdown body + pdf metadata
```

Rationale for parallel-branch rather than new-tier:

- The current tier ladder models "same content type, different
  rendering cost." PDF is a different content type entirely. Mixing
  PDF tiers into the HTML ladder would require every HTML tier to
  know what to do with bytes it can't render.
- Content-type dispatch is cheap (one header check) and honest — the
  routing decision is driven by what the server actually sent, not a
  guess from the URL suffix.
- URL-suffix fast-path (`.pdf`) can be added later as an
  optimization that skips HTTP-level probing when the caller is
  confident. Out of scope for v1.

### 4.2 Not an Engine — a post-fetch body transformer

**Revised 2026-04-14 during implementation planning.** The PDF
processor is NOT an `engine.Engine`. It's a post-fetch body
transformer that lives in a new `internal/pdf/` package, parallel
to `internal/extract/` (which houses markdown conversion,
readability, and metadata extraction).

Reason: the HTTP engine already fetched the PDF bytes. Making the
PDF engine a second `Engine` would either duplicate the fetch or
require a different interface that takes bytes rather than a URL —
both ugly. Instead, the existing tier ladder (HTTP → Chromium)
handles *getting* the bytes unchanged; PDF extraction is a body
transform that runs in `cmd/trawl/buildRecord` right after the
router returns a successful fetch, gated by an `isPDF(ct)` check.

Shape of the integration in `cmd/trawl/scrape.go`:

```go
best := outcome.Result // from router.Route

// PDF body transform runs BEFORE the HTML-gated pipeline
// (metadata, readability, CSS, schema, markdown conversion).
// On success, best.Body becomes markdown and best.ContentType
// becomes "text/markdown", so the isHTML() gates downstream
// all correctly skip.
if best != nil && isPDF(best.ContentType) {
    md, info, err := pdf.Extract(best.Body, pdfOpts)
    if err != nil {
        // soft-fail path — populate failure category, return record
    }
    best.Body = md
    best.ContentType = "text/markdown"
    rec.Metadata.PDF = toPDFInfo(info)
    if rec.Metadata.Page != nil && rec.Metadata.Page.Title == "" {
        rec.Metadata.Page.Title = info.Title
    }
}

// ... existing metadata / readability / CSS / schema / format pipeline ...
```

`output.Metadata` gains a new `PDF *PDFInfo` field:

```go
type PDFInfo struct {
    PageCount     int
    Title         string
    Author        string
    CreatedAt     time.Time
    HasTextLayer  bool
    UsedOCR       bool
    ExtractorTier string     // "pdftotext", "pdftotext-layout", "tesseract"
    Pages         []PDFPage  // per-page text, for consumers that want it
}

type PDFPage struct {
    Number int
    Text   string
}
```

The `internal/pdf.Info` struct mirrors `output.PDFInfo` so
`buildRecord` has a trivial field-for-field copy into the output
layer. Why a new typed struct rather than stuffing into `Header` or
`Evasion`: PDF metadata is durable per-document state, not per-fetch
engine state, and consumers will want it structured the same way
`metadata.page` is structured for HTML.

### 4.3 Internal tier loop (inside the PDF engine)

```
Tier 1: pdftotext              (text-layer, fast, ~100ms)
         │
         ▼
    validity: body non-empty AND > stub-threshold?
     ┌────┴────┐
     │ yes     │ no
     ▼         ▼
  return    Tier 2: pdftotext -layout
              │     (same binary, different flag)
              ▼
         validity: same check
          ┌───┴────┐
          │ yes    │ no
          ▼        ▼
       return   --ocr enabled?
                 ┌──┴──┐
                 │ no  │ yes
                 ▼     ▼
              fail   Tier 3: tesseract (rasterize + OCR, ~seconds/page)
              with      │
              "may be   ▼
              scanned,  return whatever tesseract produced
              try --ocr"
```

**Tier 2 is a refinement, not an escalation.** `pdftotext` with and
without `-layout` run against the same bytes; `-layout` is strictly
more information-preserving but can produce awkward output for
single-column docs. The design choice to run Tier 1 first (no
`-layout`) and only escalate on empty is: the single-column case is
the common case, and plain `pdftotext` output is cleaner for it.
What would change our minds: if Tier 1 ships and we find `-layout`
is always-or-nearly-always better in practice, collapse the two
into one tier with `-layout` as default.

**Tier 3 only runs with explicit `--ocr`.** Off by default because:
(1) OCR is slow (seconds per page for multi-page docs), (2) OCR
output quality varies wildly, and (3) users who need scanned-PDF
support should be choosing that explicitly, same as they choose
`--stealth`.

### 4.4 Binary detection and soft-fail

On startup of any command that could hit a PDF (scrape, batch,
crawl), trawl checks the PATH for `pdftotext` and optionally
`tesseract` (only if `--ocr` was passed). Detection is lazy-cached
on first use, not eager at startup — no point forcing the check for
users whose jobs never see a PDF.

When `pdftotext` is missing and a PDF is encountered:

```
skip: url=https://example.com/doc.pdf reason=pdftotext_missing
      hint="install poppler-utils: brew install poppler (macOS) or
      apt install poppler-utils (Debian)"
```

The fetch is classified as a new failure category,
`pdf_tooling_missing`, so it shows up distinctly in stats.json and
can be filtered. The underlying `Result` still returns the raw PDF
bytes in `Body` so a caller who wants to handle the PDF downstream
can — soft-fail means "trawl couldn't extract, here's the raw" not
"trawl discarded your content."

When `--ocr` is passed and `tesseract` is missing, trawl fails fast
at command-start rather than at first OCR attempt. OCR is an
explicit user ask; failing early is friendlier than processing 100
URLs before discovering tesseract isn't installed.

---

## 5. Flag surface

New flags on `scrape`, `batch`, `crawl`:

- `--ocr` — enable Tier 3 (tesseract OCR) for scanned PDFs. Off by
  default. Requires `tesseract` on PATH; command fails at start if
  missing.
- `--ocr-lang <code>` — tesseract language pack code
  (e.g. `eng`, `eng+deu`). Default `eng`. Only meaningful with
  `--ocr`.
- `--pdf-max-pages <N>` — cap OCR processing at the first N pages.
  Default 50. Protects against accidentally OCR'ing a 500-page
  scanned book. Only applies to the OCR tier; text-layer extraction
  is cheap enough that full-doc is fine.

No flags for disabling the PDF engine entirely in v1. If a user
wants the raw PDF bytes, they set `--format json` and read the
fields accordingly, or they use a content-type filter upstream.
Adding `--no-pdf` is a v2 question if anyone asks.

---

## 6. Output shape

### 6.1 Markdown output (`--format markdown`, default for PDF)

Body field contains the extracted text as markdown. Headings are
detected by tesseract/pdftotext heuristics (font size deltas). Lists
and tables are preserved as best the extractor can manage — usually
whitespace-aligned rather than real markdown tables for v1.

Metadata surfaces at `metadata.pdf`:

```json
{
  "url": "https://example.com/paper.pdf",
  "final_url": "https://example.com/paper.pdf",
  "status_code": 200,
  "content_type": "text/markdown",
  "body": "# Introduction\n\nThe quick brown fox...",
  "metadata": {
    "page": { "title": "...", ... },
    "pdf": {
      "page_count": 12,
      "title": "A Study of Something",
      "author": "Jane Doe",
      "created_at": "2024-03-15T00:00:00Z",
      "has_text_layer": true,
      "used_ocr": false,
      "extractor_tier": "pdftotext"
    }
  }
}
```

`metadata.page` is still populated where trawl can infer values
(title from PDF metadata becomes `metadata.page.title`) so
downstream consumers using `jq '.metadata.page.title'` work
uniformly across HTML and PDF inputs.

### 6.2 JSON body output (`--format json`)

**Revised 2026-04-14.** `--format json` keeps its uniform meaning
across HTML and PDF: it serializes `rec.Extracted` (the flat CSS /
schema extraction output) into `rec.Body`. It does NOT diverge to
emit per-page PDF text.

Per-page PDF text lives at `metadata.pdf.pages`, structured
identically to how `metadata.page` holds HTML metadata. Consumers
reach for it the same way they reach for any other metadata field:

```bash
trawl scrape https://example.com/paper.pdf --format markdown \
  | jq '.metadata.pdf.pages[] | {page: .number, first_line: .text}'
```

The concatenated full-text IS the markdown body when `--format
markdown` is on. Consumers who want only pages without the markdown
can leave `--format` unset and still get `metadata.pdf.pages` —
metadata is populated regardless of `--format`.

Why this shape over the "body_json = {pages, full_text}" divergence
earlier in this doc: `--format json` means the same thing for HTML
today (serialized extraction output), and divergence-for-
divergence's-sake makes the mental model harder. Page granularity
is real PDF-specific data, and `metadata.pdf` is the right home
for PDF-specific data.

### 6.3 CSV output

CSV output of PDFs works the same way as HTML — auto-discovered
columns from the first record, metadata flattened with dot-notation
(`metadata.pdf.page_count`, etc.). No special-casing.

---

## 7. Testing strategy

Chromium tests already gate on `chromiumAvailable()`; PDF tests
follow the same pattern with `pdftotextAvailable()` and
`tesseractAvailable()` helpers. Tests skip cleanly on machines
without the binaries.

Test fixtures in `testdata/pdf/`:

1. `text-layer.pdf` — single-column academic-paper shape, text
   stream present. Exercises Tier 1.
2. `multi-column.pdf` — two-column layout with a table. Exercises
   Tier 2 (layout mode) and documents the current Tier-1-vs-Tier-2
   quality gap so future changes to the escalation logic have a
   baseline.
3. `scanned.pdf` — image-only PDF (scanned page). Exercises Tier 3
   when `--ocr` is set; also exercises the "may be scanned, try
   --ocr" error path when it isn't.
4. `encrypted.pdf` — password-protected. Exercises the clean-error
   path.
5. `metadata-rich.pdf` — title/author/creation-date present.
   Validates `metadata.pdf` extraction.

All fixtures are generated by small Go programs in `testdata/pdf/`
so they're reproducible and don't balloon the repo with opaque
binary blobs. Fixtures are small enough (<50KB each) that
committing them is fine.

Integration test that matters most: scrape a URL that serves a
PDF through a local `httptest` server, verify the body is
markdown not raw PDF bytes, verify `metadata.pdf` is populated.
That test is the one future refactors will most often break.

---

## 8. Decision rule: when does v1 ship?

Unlike the evasion work, there's no falsifiable "does this tier
matter" benchmark to run first — every PDF-containing crawl today
produces wrong output (raw PDF bytes in `body`), so the floor for
shipping any PDF engine is "better than current." The rule is
shape-based instead:

**Ship when:**
1. A text-layer PDF fetched through trawl returns clean markdown
   (integration test passes).
2. A multi-column PDF returns readable output via Tier 2 (manual
   verification on `multi-column.pdf` fixture — doesn't have to be
   perfect, just not interleaved garbage).
3. Missing `pdftotext` soft-fails with the correct install hint.
4. `--ocr` correctly fails at command-start when `tesseract` is
   missing.
5. `metadata.pdf` is populated on all successful extractions and
   distinguishable in output from HTML fetches' `metadata.page`.

Tier 3 (OCR) can ship behind `--ocr` or defer to a follow-up — Jeff
chose to include OCR in v1, so item 4 is a v1 requirement. What
gets deferred if OCR delays shipping: the v1 cut happens without it
and OCR is a v1.1.

---

## 9. What would change our minds

Scenarios that would force a redesign of this doc:

- **`pdftotext` proves insufficient on real consumer data.** If a
  consumer reports that the output is garbage for their use case
  and they can show `marker` or `docling` producing materially
  better output on the same PDF, Tier 3 (Marker integration) moves
  from "deferred" to "build." The shell-out interface is designed
  so this is a contained addition.
- **A consumer needs structured PDF extraction (form fields, table
  cells as cells, figure bounding boxes).** That's a different
  feature shape than the text/markdown pipeline and would justify a
  parallel design doc, not a mutation of this one.
- **OCR becomes a hot path rather than an opt-in niche.** If >30%
  of PDFs hit in real use are scanned, the default-off stance is
  wrong and OCR should be part of the main escalation ladder rather
  than gated behind a flag. Unlikely for general-purpose scraping,
  plausible for specific verticals (government records, legal
  archives).
- **`pdftotext` gets unusably slow on a real workload.** Poppler's
  parser is battle-tested but not optimized for throughput. If a
  consumer hits a job where PDF extraction is the bottleneck and
  shelling out the per-doc cost dominates, the fix is batching
  (one `pdftotext` invocation over multiple docs) or a long-lived
  worker process. Both are non-trivial; both would be documented
  additions to this doc, not silent changes to the engine.

---

## 10. Open questions

Questions this doc doesn't yet answer, deliberately, because they
need a real consumer signal first:

- **Should the router retry with a different engine when a PDF
  fetch itself fails (403, rate-limited)?** Trawl's main tier
  escalation already handles this for HTML. The PDF engine is
  downstream of content-type detection, so the main ladder's
  escalation applies naturally to the fetch. No PDF-specific
  retry logic needed for v1.
- **Should `--schema` ever work on PDFs?** Probably not — CSS
  selectors don't apply. A separate "extract these page regions
  as named fields" feature is the closest analogue and it's far
  enough from v1 scope that we're not designing it here.
- **Concurrency model for OCR.** Tesseract is CPU-bound; running
  many concurrent tesseract processes saturates the box. The v1
  plan is to reuse trawl's existing per-host concurrency caps,
  which should be adequate since OCR is per-document not per-URL.
  If that's wrong in practice, a separate `--ocr-concurrency` flag
  is the cheapest fix.

---

## Appendix: binary dependencies in one place

| Binary      | Package                | Required for      | Install                                                   |
| ----------- | ---------------------- | ----------------- | --------------------------------------------------------- |
| `pdftotext` | `poppler-utils`        | Tier 1 + Tier 2   | `brew install poppler` / `apt install poppler-utils`      |
| `tesseract` | `tesseract-ocr`        | Tier 3 (`--ocr`)  | `brew install tesseract` / `apt install tesseract-ocr`    |
| `pdfinfo`   | `poppler-utils`        | Metadata (optional; fallback is parsing `pdftotext -info` stderr) | same as pdftotext |

No other binaries. No Python, no Node, no Docker. If `poppler-utils`
is installed, trawl handles text-layer PDFs. If `tesseract` is also
installed and `--ocr` is passed, it handles scanned PDFs too. That
is the entire dependency story.
