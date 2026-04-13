#!/usr/bin/env bash
#
# bench/phase0/run.sh — Run D: re-measure chromium escalation rate
# with expanded discovery. Three-phase pipeline:
#
#   Phase 1: trawl batch with pricing_url + expanded fallback selectors
#   Phase 2: trawl map on remaining misses, grep for pricing-like URLs
#   Phase 3: trawl batch on newly discovered URLs
#
# Compares results against the Lightpanda decision thresholds:
#   - n >= 500 reachable
#   - chromium escalation rate >= 15%
#
# Prior art: Run C (2026-04-10) hit 14.08% on n=355 reachable.
# See docs/DECISIONS.md for the full history.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SEED="${REPO_ROOT}/seed/companies.csv"
RESULTS_DIR="${SCRIPT_DIR}/results-$(date +%Y%m%d-%H%M%S)"
TRAWL_BIN="${TRAWL_BIN:-trawl}"

# ── Configuration ──────────────────────────────────────────────
N=1000                    # first N rows from seed (same as Run C)
CONCURRENCY=15
RATE=1
TIMEOUT=30s
MAP_DEPTH=1               # BFS depth for map discovery
MAP_LIMIT=50              # max URLs per homepage from map
MAP_PARALLEL=4            # parallel trawl map invocations
MAP_RATE=2                # rate limit per domain for map

# Run C had 3 patterns. Run D has 18.
FALLBACK_SELECTOR='a[href*="pricing"], a[href*="/plans"], a[href*="/price"], a[href*="/subscribe"], a[href*="/subscription"], a[href*="/upgrade"], a[href*="/buy"], a[href*="/packages"], a[href*="/billing"], a[href*="/pro"], a[href*="/premium"], a[href*="/enterprise"], a[href*="/features"], a[href*="/cost"], a[href*="/rates"], a[href*="/tiers"], a[href*="/get-started"], a[href*="/signup"], a[href*="/order"]'

# Regex for filtering map output (extended grep, case-insensitive)
PRICING_REGEX='/(pricing|plans?|prices?|subscribe|subscription|upgrade|buy|packages?|billing|pro|premium|enterprise|features|cost|rates?|tiers?|get-started|signup|order)(/|$|\?)'

# ── Dependency checks ─────────────────────────────────────────
for cmd in "${TRAWL_BIN}" jq python3; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "error: ${cmd} not on PATH" >&2
    exit 1
  fi
done
if [[ ! -f "${SEED}" ]]; then
  echo "error: seed file not found: ${SEED}" >&2
  exit 1
fi

# Isolated TRAWL_HOME so we don't contend with running jobs or
# pollute the user's tier-cache / content-cache.
export TRAWL_HOME="${RESULTS_DIR}/.trawl"
mkdir -p "${TRAWL_HOME}"
mkdir -p "${RESULTS_DIR}"

echo "═══════════════════════════════════════════════════════" >&2
echo "  Run D — Phase 0 Benchmark" >&2
echo "  results: ${RESULTS_DIR}" >&2
echo "═══════════════════════════════════════════════════════" >&2
echo "" >&2

# ══════════════════════════════════════════════════════════════
# STEP 1: Extract first N rows from seed
# ══════════════════════════════════════════════════════════════
echo "[step 1] extracting first ${N} rows from seed CSV..." >&2
head -1 "${SEED}" > "${RESULTS_DIR}/seed-${N}.csv"
head -$((N + 1)) "${SEED}" | tail -n +2 >> "${RESULTS_DIR}/seed-${N}.csv"
ACTUAL=$(tail -n +2 "${RESULTS_DIR}/seed-${N}.csv" | wc -l | tr -d ' ')
echo "  → ${ACTUAL} data rows" >&2

# ══════════════════════════════════════════════════════════════
# STEP 2: Phase 1 — batch with expanded fallback selector
# ══════════════════════════════════════════════════════════════
echo "" >&2
echo "[step 2] phase 1: batch with expanded fallback (${ACTUAL} URLs)..." >&2
PHASE1_START=$(date +%s)

"${TRAWL_BIN}" batch "${RESULTS_DIR}/seed-${N}.csv" \
  --url-column pricing_url \
  --fallback-column homepage \
  --fallback-selector "${FALLBACK_SELECTOR}" \
  --tiers http,chromium \
  --concurrency "${CONCURRENCY}" \
  --rate "${RATE}" \
  --timeout "${TIMEOUT}" \
  --browser-like \
  --no-tier-learning \
  --ignore-robots \
  --job-id "phase0-rund-phase1" \
  -o "${RESULTS_DIR}/phase1.jsonl" \
  2>&1 | grep -v '"level":"debug"' >&2 || true

PHASE1_END=$(date +%s)
PHASE1_OK=$(jq -r 'select(.failure_category == "success") | .url' "${RESULTS_DIR}/phase1.jsonl" | wc -l | tr -d ' ')
PHASE1_TOTAL=$(wc -l < "${RESULTS_DIR}/phase1.jsonl" | tr -d ' ')
echo "  → ${PHASE1_OK}/${PHASE1_TOTAL} reachable in $((PHASE1_END - PHASE1_START))s" >&2

# ══════════════════════════════════════════════════════════════
# STEP 3: Identify failed rows → extract homepages for map
# ══════════════════════════════════════════════════════════════
echo "" >&2
echo "[step 3] identifying failed rows for map discovery..." >&2

# Get successfully fetched canonical URLs from phase 1
jq -r 'select(.failure_category == "success") | .canonical_url' \
  "${RESULTS_DIR}/phase1.jsonl" | sort -u > "${RESULTS_DIR}/phase1-ok.txt"

# Use Python for proper CSV parsing (seed has quoted commas in description)
python3 - "${RESULTS_DIR}/seed-${N}.csv" "${RESULTS_DIR}/phase1-ok.txt" "${RESULTS_DIR}/map-targets.txt" << 'PYEOF'
import csv, sys

seed_path, ok_path, out_path = sys.argv[1], sys.argv[2], sys.argv[3]

# Load successfully fetched URLs
with open(ok_path) as f:
    ok_urls = set(line.strip() for line in f if line.strip())

# For each seed row, if the pricing_url wasn't fetched successfully,
# emit the homepage as a map target (if it exists and is non-empty).
targets = set()
with open(seed_path, newline='') as f:
    reader = csv.DictReader(f)
    for row in reader:
        pricing = row.get('pricing_url', '').strip()
        homepage = row.get('homepage', '').strip()
        if pricing not in ok_urls and homepage:
            targets.add(homepage)

with open(out_path, 'w') as f:
    for url in sorted(targets):
        f.write(url + '\n')

print(f"  → {len(targets)} homepages to map-discover", file=sys.stderr)
PYEOF

MAP_COUNT=$(wc -l < "${RESULTS_DIR}/map-targets.txt" | tr -d ' ')

# ══════════════════════════════════════════════════════════════
# STEP 4: Phase 2 — trawl map on each homepage, filter for pricing
# ══════════════════════════════════════════════════════════════
echo "" >&2
echo "[step 4] phase 2: map discovery on ${MAP_COUNT} homepages (depth ${MAP_DEPTH}, ${MAP_PARALLEL} parallel)..." >&2
PHASE2_START=$(date +%s)

: > "${RESULTS_DIR}/map-raw.txt"

# Export variables so the xargs subshells can see them.
export TRAWL_BIN MAP_DEPTH MAP_LIMIT TIMEOUT MAP_RATE

# xargs runs MAP_PARALLEL trawl map processes concurrently.
# Each discovers URLs from one homepage; stdout is collected.
# Failures are silently dropped (|| true) — map is best-effort.
cat "${RESULTS_DIR}/map-targets.txt" | xargs -P "${MAP_PARALLEL}" -I{} \
  bash -c '
    urls=$("${TRAWL_BIN}" map "$1" \
      --depth "${MAP_DEPTH}" \
      --sources crawl \
      --same-domain \
      --limit "${MAP_LIMIT}" \
      --browser-like \
      --ignore-robots \
      --timeout "${TIMEOUT}" \
      --rate "${MAP_RATE}" \
      2>/dev/null) || true
    if [ -n "$urls" ]; then
      echo "$urls"
    fi
  ' _ {} >> "${RESULTS_DIR}/map-raw.txt" 2>/dev/null || true

# Filter for pricing-like URLs and deduplicate against phase 1
grep -iE "${PRICING_REGEX}" "${RESULTS_DIR}/map-raw.txt" 2>/dev/null \
  | sort -u \
  | comm -23 - "${RESULTS_DIR}/phase1-ok.txt" \
  > "${RESULTS_DIR}/phase2-urls.txt" || true

PHASE2_END=$(date +%s)
MAP_RAW=$(wc -l < "${RESULTS_DIR}/map-raw.txt" | tr -d ' ')
DISCOVERED=$(wc -l < "${RESULTS_DIR}/phase2-urls.txt" | tr -d ' ')
echo "  → ${MAP_RAW} URLs discovered, ${DISCOVERED} pricing-like (new) in $((PHASE2_END - PHASE2_START))s" >&2

# ══════════════════════════════════════════════════════════════
# STEP 5: Phase 3 — batch the discovered URLs
# ══════════════════════════════════════════════════════════════
if [ "${DISCOVERED}" -gt 0 ]; then
  echo "" >&2
  echo "[step 5] phase 3: batch ${DISCOVERED} map-discovered URLs..." >&2
  PHASE3_START=$(date +%s)

  "${TRAWL_BIN}" batch "${RESULTS_DIR}/phase2-urls.txt" \
    --tiers http,chromium \
    --concurrency "${CONCURRENCY}" \
    --rate "${RATE}" \
    --timeout "${TIMEOUT}" \
    --browser-like \
    --no-tier-learning \
    --ignore-robots \
    --job-id "phase0-rund-phase3" \
    -o "${RESULTS_DIR}/phase3.jsonl" \
    2>&1 | grep -v '"level":"debug"' >&2 || true

  PHASE3_END=$(date +%s)
  PHASE3_OK=$(jq -r 'select(.failure_category == "success") | .url' "${RESULTS_DIR}/phase3.jsonl" | wc -l | tr -d ' ')
  echo "  → ${PHASE3_OK} reachable from map-discovered in $((PHASE3_END - PHASE3_START))s" >&2
else
  echo "" >&2
  echo "[step 5] phase 3: skipped (no new URLs from map)" >&2
fi

# ══════════════════════════════════════════════════════════════
# STEP 6: Combine results and compute stats
# ══════════════════════════════════════════════════════════════
echo "" >&2
echo "[step 6] computing stats..." >&2

cat "${RESULTS_DIR}/phase1.jsonl" > "${RESULTS_DIR}/combined.jsonl"
[ -f "${RESULTS_DIR}/phase3.jsonl" ] && cat "${RESULTS_DIR}/phase3.jsonl" >> "${RESULTS_DIR}/combined.jsonl"

jq -s '
  def is_success: .failure_category == "success";

  {
    total: length,
    reachable: [.[] | select(is_success)] | length,
    tier_http: [.[] | select(is_success and .tier == "http")] | length,
    tier_chromium: [.[] | select(is_success and .tier == "chromium")] | length,
    failures: (
      [.[] | select(is_success | not)]
      | group_by(.failure_category)
      | map({key: .[0].failure_category, value: length})
      | from_entries
    )
  } |
  . + {
    escalation_pct: (if .reachable > 0 then (.tier_chromium / .reachable * 10000 | round / 100) else 0 end)
  }
' "${RESULTS_DIR}/combined.jsonl" > "${RESULTS_DIR}/stats.json"

# ══════════════════════════════════════════════════════════════
# STEP 7: Print summary
# ══════════════════════════════════════════════════════════════
echo "" >&2
echo "═══════════════════════════════════════════════════════"
echo "  Run D — Phase 0 Results"
echo "═══════════════════════════════════════════════════════"
jq -r '
  "  total records:            \(.total)",
  "  reachable (success):      \(.reachable)",
  "    tier http:              \(.tier_http)",
  "    tier chromium:          \(.tier_chromium)",
  "",
  "  chromium escalation rate: \(.escalation_pct)% (\(.tier_chromium)/\(.reachable))",
  "",
  "  ── Threshold check ──",
  "  n >= 500 reachable:       \(if .reachable >= 500 then "PASS" else "FAIL (need \(500 - .reachable) more)" end)",
  "  rate >= 15%:              \(if .escalation_pct >= 15 then "CROSSED — Lightpanda reopens" else "BELOW — skip holds (\(.escalation_pct)% < 15%)" end)",
  "",
  "  ── vs Run C ──",
  "  Run C: 14.08% on n=355",
  "  Run D: \(.escalation_pct)% on n=\(.reachable)",
  "  delta: \((.escalation_pct - 14.08) * 100 | round / 100)pp rate, +\(.reachable - 355) reachable",
  "",
  "  ── Failure breakdown ──",
  (.failures | to_entries | sort_by(-.value) | .[] | "  \(.key): \(.value)")
' "${RESULTS_DIR}/stats.json"
echo "═══════════════════════════════════════════════════════"
echo ""
echo "artifacts: ${RESULTS_DIR}/"
echo "  seed-${N}.csv        input (${ACTUAL} rows)"
echo "  phase1.jsonl         batch results"
echo "  map-targets.txt      homepages sent to map (${MAP_COUNT})"
echo "  map-raw.txt          all URLs from map (${MAP_RAW})"
echo "  phase2-urls.txt      pricing-like discoveries (${DISCOVERED})"
[ -f "${RESULTS_DIR}/phase3.jsonl" ] && echo "  phase3.jsonl         map-discovery batch results"
echo "  combined.jsonl       merged results"
echo "  stats.json           machine-readable stats"
