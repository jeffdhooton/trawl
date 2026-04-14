#!/usr/bin/env bash
#
# bench/pdf/run.sh — exercise trawl's PDF engine against real-world URLs.
#
# Runs the target list twice:
#   1. WITHOUT --ocr: observes Tier 1/2 behavior. Scanned PDFs end up
#      with failure_category=extraction_failed (ErrEmptyExtraction).
#   2. WITH --ocr: Tier 3 kicks in for the stub-output rows. Successful
#      records show used_ocr=true and extractor_tier=tesseract.
#
# The report cross-references the two runs per-URL so you can see which
# URLs actually needed OCR and which succeeded via Tier 1/2.
#
# Usage:
#   bench/pdf/run.sh                    # uses bench/pdf/targets.txt
#   bench/pdf/run.sh my-urls.txt        # custom target list
#
# Artifacts land in bench/pdf/results-<timestamp>/. Each run prints a
# results path and a summary to stdout.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TARGETS="${1:-${SCRIPT_DIR}/targets.txt}"
RESULTS_DIR="${SCRIPT_DIR}/results-$(date +%Y%m%d-%H%M%S)"
TRAWL_BIN="${TRAWL_BIN:-trawl}"

# ── Configuration ──────────────────────────────────────────────
CONCURRENCY=4             # modest — we're hitting real third-party sites
RATE=1                    # 1 req/sec per domain
TIMEOUT=60s               # PDFs can be big; Tier 3 OCR adds seconds/page
PDF_MAX_PAGES=10          # cap OCR to first 10 pages (full doc on Tier 1/2)
OCR_LANG="eng"

# ── Dependency checks ─────────────────────────────────────────
for cmd in "${TRAWL_BIN}" jq python3; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "error: ${cmd} not on PATH" >&2
    exit 1
  fi
done
if [[ ! -f "${TARGETS}" ]]; then
  echo "error: targets file not found: ${TARGETS}" >&2
  exit 1
fi
if ! command -v pdftotext >/dev/null 2>&1; then
  echo "warning: pdftotext not on PATH — Tier 1/2 will soft-fail per-row" >&2
  echo "         install via 'brew install poppler' or 'apt install poppler-utils'" >&2
fi
HAS_OCR=1
if ! command -v tesseract >/dev/null 2>&1 || ! command -v pdftoppm >/dev/null 2>&1; then
  echo "warning: OCR toolchain missing — the --ocr run will be skipped" >&2
  echo "         install: 'brew install tesseract' or 'apt install tesseract-ocr'" >&2
  HAS_OCR=0
fi

# Isolated TRAWL_HOME so we don't contend with running jobs or pollute
# the user's tier-cache / content-cache.
export TRAWL_HOME="${RESULTS_DIR}/.trawl"
mkdir -p "${TRAWL_HOME}"
mkdir -p "${RESULTS_DIR}"

# ── Extract URLs + expected categories from targets file ──────
# Strip comment-only lines and inline comments, preserve order. Also
# write a sidecar file mapping URL → expected category for the report.
python3 - "${TARGETS}" "${RESULTS_DIR}" << 'PYEOF'
import re, sys, pathlib, json

src = pathlib.Path(sys.argv[1])
out = pathlib.Path(sys.argv[2])

url_list = []
categories = {}

for raw in src.read_text().splitlines():
    line = raw.strip()
    if not line or line.startswith("#"):
        continue
    # split URL from inline comment at first unescaped '#'
    parts = line.split("#", 1)
    url = parts[0].strip()
    if not url:
        continue
    comment = parts[1].strip() if len(parts) > 1 else ""
    m = re.match(r"^(TL|OCR|MIX)\b", comment)
    cat = m.group(1) if m else "?"
    url_list.append(url)
    categories[url] = cat

(out / "urls.txt").write_text("\n".join(url_list) + "\n")
(out / "categories.json").write_text(json.dumps(categories, indent=2))
print(f"  → {len(url_list)} URLs (categories: {sorted(set(categories.values()))})", file=sys.stderr)
PYEOF

URL_COUNT=$(wc -l < "${RESULTS_DIR}/urls.txt" | tr -d ' ')

echo "═══════════════════════════════════════════════════════" >&2
echo "  PDF Bench — ${URL_COUNT} URLs" >&2
echo "  results: ${RESULTS_DIR}" >&2
echo "═══════════════════════════════════════════════════════" >&2

# ══════════════════════════════════════════════════════════════
# RUN 1: no --ocr. Expect Tier 1/2 success on text-layer PDFs,
# ErrEmptyExtraction on scanned ones.
# ══════════════════════════════════════════════════════════════
echo "" >&2
echo "[run 1] batch without --ocr (Tier 1/2 only)..." >&2
RUN1_START=$(date +%s)

"${TRAWL_BIN}" batch "${RESULTS_DIR}/urls.txt" \
  --tiers http,chromium \
  --concurrency "${CONCURRENCY}" \
  --rate "${RATE}" \
  --timeout "${TIMEOUT}" \
  --ignore-robots \
  --no-tier-learning \
  --job-id "pdf-bench-no-ocr-$(date +%s)" \
  -o "${RESULTS_DIR}/no-ocr.jsonl" \
  2>&1 | grep -v '"level":"debug"' >&2 || true

RUN1_END=$(date +%s)
echo "  → done in $((RUN1_END - RUN1_START))s" >&2

# ══════════════════════════════════════════════════════════════
# RUN 2: with --ocr. Tier 3 kicks in when Tier 1/2 return stub.
# Skipped when OCR toolchain isn't installed.
# ══════════════════════════════════════════════════════════════
if [ "${HAS_OCR}" -eq 1 ]; then
  echo "" >&2
  echo "[run 2] batch WITH --ocr (Tier 3 available)..." >&2
  RUN2_START=$(date +%s)

  "${TRAWL_BIN}" batch "${RESULTS_DIR}/urls.txt" \
    --tiers http,chromium \
    --concurrency "${CONCURRENCY}" \
    --rate "${RATE}" \
    --timeout "${TIMEOUT}" \
    --ocr \
    --ocr-lang "${OCR_LANG}" \
    --pdf-max-pages "${PDF_MAX_PAGES}" \
    --ignore-robots \
    --no-tier-learning \
    --job-id "pdf-bench-ocr-$(date +%s)" \
    -o "${RESULTS_DIR}/with-ocr.jsonl" \
    2>&1 | grep -v '"level":"debug"' >&2 || true

  RUN2_END=$(date +%s)
  echo "  → done in $((RUN2_END - RUN2_START))s" >&2
else
  echo "" >&2
  echo "[run 2] skipped (OCR toolchain missing)" >&2
fi

# ══════════════════════════════════════════════════════════════
# REPORT: per-URL outcome, tier distribution, category validation
# ══════════════════════════════════════════════════════════════
echo "" >&2
echo "[report] cross-referencing runs..." >&2

python3 - "${RESULTS_DIR}" << 'PYEOF'
import json, pathlib, sys, collections

d = pathlib.Path(sys.argv[1])
cats = json.loads((d / "categories.json").read_text())

def load_jsonl(path):
    recs = {}
    if not path.exists():
        return recs
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        r = json.loads(line)
        recs[r.get("url") or r.get("canonical_url")] = r
    return recs

no_ocr = load_jsonl(d / "no-ocr.jsonl")
with_ocr = load_jsonl(d / "with-ocr.jsonl")

def outcome(r):
    if r is None:
        return ("missing", None, None)
    cat = r.get("failure_category", "?")
    tier = None
    used_ocr = False
    if r.get("metadata", {}).get("pdf"):
        tier = r["metadata"]["pdf"].get("extractor_tier")
        used_ocr = r["metadata"]["pdf"].get("used_ocr", False)
    return (cat, tier, used_ocr)

# Per-URL table
rows = []
stats = {
    "no_ocr": collections.Counter(),
    "with_ocr": collections.Counter(),
    "tier_no_ocr": collections.Counter(),
    "tier_with_ocr": collections.Counter(),
    "ocr_activated": 0,
    "ocr_rescued": 0,    # failed without --ocr, succeeded with it
    "category_match": collections.Counter(),  # (expected, observed)
}

for url, expected in cats.items():
    r1 = no_ocr.get(url)
    r2 = with_ocr.get(url)
    c1, t1, _ = outcome(r1)
    c2, t2, ocr = outcome(r2)

    stats["no_ocr"][c1] += 1
    if r2 is not None:
        stats["with_ocr"][c2] += 1
    if t1:
        stats["tier_no_ocr"][t1] += 1
    if t2:
        stats["tier_with_ocr"][t2] += 1
    if ocr:
        stats["ocr_activated"] += 1
    if c1 != "success" and c2 == "success":
        stats["ocr_rescued"] += 1

    # Category validation
    observed = "?"
    if c1 == "success":
        observed = "TL"
    elif c2 == "success" and ocr:
        observed = "OCR"
    elif c2 == "success":
        observed = "TL"  # success with --ocr on but without ocr actually used
    else:
        observed = "FAIL"
    stats["category_match"][(expected, observed)] += 1

    rows.append({
        "url": url,
        "expected": expected,
        "observed": observed,
        "no_ocr": {"outcome": c1, "tier": t1},
        "with_ocr": {"outcome": c2, "tier": t2, "used_ocr": ocr} if r2 else None,
    })

# Write machine-readable artifacts
(d / "per-url.json").write_text(json.dumps(rows, indent=2))

# Pretty stats
stats_out = {
    "totals": {
        "urls": len(cats),
        "ocr_available": (d / "with-ocr.jsonl").exists(),
    },
    "no_ocr_outcomes": dict(stats["no_ocr"]),
    "with_ocr_outcomes": dict(stats["with_ocr"]),
    "tier_distribution": {
        "no_ocr": dict(stats["tier_no_ocr"]),
        "with_ocr": dict(stats["tier_with_ocr"]),
    },
    "ocr_activated": stats["ocr_activated"],
    "ocr_rescued": stats["ocr_rescued"],
}
(d / "stats.json").write_text(json.dumps(stats_out, indent=2))

# Human report
print("═══════════════════════════════════════════════════════")
print(f"  PDF Bench Results — {len(cats)} URLs")
print("═══════════════════════════════════════════════════════")
print()
print("  ── Run 1 (no --ocr) ──")
for cat, n in sorted(stats["no_ocr"].items(), key=lambda x: -x[1]):
    print(f"    {cat}: {n}")
print(f"    tier distribution: {dict(stats['tier_no_ocr'])}")
print()
if (d / "with-ocr.jsonl").exists():
    print("  ── Run 2 (with --ocr) ──")
    for cat, n in sorted(stats["with_ocr"].items(), key=lambda x: -x[1]):
        print(f"    {cat}: {n}")
    print(f"    tier distribution: {dict(stats['tier_with_ocr'])}")
    print(f"    OCR activated (tier = tesseract): {stats['ocr_activated']}")
    print(f"    OCR rescued (fail → success):     {stats['ocr_rescued']}")
    print()
print("  ── Per-URL ──")
for row in rows:
    url_short = row["url"]
    if len(url_short) > 60:
        url_short = url_short[:57] + "..."
    expected = row["expected"]
    observed = row["observed"]
    mark = "✓" if expected == observed or expected == "?" or (expected == "MIX" and observed in ("TL", "OCR")) else "✗"
    no_ocr_info = row["no_ocr"]
    no_ocr_str = f"{no_ocr_info['outcome']}"
    if no_ocr_info["tier"]:
        no_ocr_str += f"/{no_ocr_info['tier']}"
    with_ocr_str = ""
    if row["with_ocr"]:
        w = row["with_ocr"]
        with_ocr_str = f" | ocr: {w['outcome']}"
        if w["tier"]:
            with_ocr_str += f"/{w['tier']}"
    print(f"    [{expected} → {observed}] {mark} {url_short}")
    print(f"       no-ocr: {no_ocr_str}{with_ocr_str}")
print()
print("═══════════════════════════════════════════════════════")
PYEOF

# ── Cleanup ───────────────────────────────────────────────────
# The isolated TRAWL_HOME is only useful during the run — it holds the
# frontier DB, job configs, and tier-learning cache, none of which are
# interesting after the run completes. Remove it so committed bench
# artifacts stay under a few MB and consist only of result data.
# Set KEEP_TRAWL_HOME=1 to retain for debugging.
if [ -z "${KEEP_TRAWL_HOME:-}" ]; then
  rm -rf "${TRAWL_HOME}"
fi

echo "" >&2
echo "artifacts: ${RESULTS_DIR}/"
echo "  urls.txt           parsed URL list"
echo "  categories.json    expected category per URL"
echo "  no-ocr.jsonl       Run 1 output (Tier 1/2)"
[ -f "${RESULTS_DIR}/with-ocr.jsonl" ] && echo "  with-ocr.jsonl     Run 2 output (Tier 3 available)"
echo "  per-url.json       per-URL outcome breakdown"
echo "  stats.json         aggregated stats"
