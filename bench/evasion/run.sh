#!/usr/bin/env bash
#
# bench/evasion/run.sh — sweep evasion target list through trawl three
# ways and print a comparison table. Empirical input for the
# EVASION.md §5.3 Tier 3 decision rule: if Tier 1+2 already wins on
# the targets we expected to need Tier 3, we don't need Tier 3 yet.
#
# Modes per target:
#   1. baseline    — default trawl, no evasion. polite identity.
#   2. tier1       — --browser-like (rotating Chrome UA + headers + jar + jitter)
#   3. tier1+2     — --browser-like --stealth --tiers chromium
#                    (same as tier1 plus chromium stealth init script,
#                     forced to chromium so the http engine isn't
#                     even attempted — we want to test stealth)
#   4. tier3       — --browser-like --tls-match chrome --tiers http
#                    (Chrome JA4 forgery via uTLS on the http path,
#                     forced to http because chromium has its own real
#                     Chrome TLS stack and would mask the signal —
#                     we want to know if forging the ClientHello alone
#                     unblocks anything that tier1 couldn't reach
#                     on the http path)
#
# Verdict heuristic per row:
#   - status >= 400              → "blocked"
#   - body  <  1500 bytes        → "stub"     (likely SPA shell or empty doc)
#   - status == 200 && body OK   → "ok"
#   - error from trawl           → "error"
#
# Output is a fixed-width table written to stdout, plus the raw JSONL
# from each fetch in $RESULTS_DIR for jq follow-up.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGETS_FILE="${SCRIPT_DIR}/targets.txt"
RESULTS_DIR="${SCRIPT_DIR}/results-$(date +%Y%m%d-%H%M%S)"
TRAWL_BIN="${TRAWL_BIN:-trawl}"
PAUSE_BETWEEN_TARGETS_S="${PAUSE_BETWEEN_TARGETS_S:-3}"
PER_FETCH_TIMEOUT="${PER_FETCH_TIMEOUT:-45s}"

if ! command -v "${TRAWL_BIN}" >/dev/null 2>&1; then
  echo "error: ${TRAWL_BIN} not on PATH. install with 'go install ./cmd/trawl' from the repo root." >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required for parsing trawl JSONL output" >&2
  exit 1
fi
if [[ ! -f "${TARGETS_FILE}" ]]; then
  echo "error: ${TARGETS_FILE} not found" >&2
  exit 1
fi

mkdir -p "${RESULTS_DIR}"
echo "results dir: ${RESULTS_DIR}" >&2

# verdict <statusCode> <bodyBytes> <error>
verdict() {
  local status="$1"
  local bytes="$2"
  local err="$3"
  if [[ -n "${err}" && "${err}" != "null" ]]; then
    echo "error"
    return
  fi
  if [[ -z "${status}" || "${status}" == "0" ]]; then
    echo "error"
    return
  fi
  if (( status >= 400 )); then
    echo "blocked"
    return
  fi
  if (( bytes < 1500 )); then
    echo "stub"
    return
  fi
  echo "ok"
}

# fetch_one <mode-label> <url> <out-file> <extra trawl args...>
fetch_one() {
  local mode="$1"; shift
  local url="$1"; shift
  local out="$1"; shift

  # Run trawl. Don't fail the bench on a non-zero exit; trawl returns
  # nonzero for any per-row error and we want to record those.
  set +e
  "${TRAWL_BIN}" scrape "${url}" \
    --timeout "${PER_FETCH_TIMEOUT}" \
    --no-tier-learning \
    -o "${out}" \
    "$@" \
    >"${out}.log" 2>&1
  set -e

  # If trawl wrote nothing (binary crashed mid-write), fall back.
  if [[ ! -s "${out}" ]]; then
    echo '{"status_code":0,"metadata":{"body_bytes":0},"tier":"-","error":"no output"}'
    return
  fi

  # Output may be the canonical JSONL record OR a CSV/etc — for this
  # bench we always force JSONL via the .jsonl extension below.
  jq -c '{status_code, tier, body_bytes:(.metadata.body_bytes // 0), error}' "${out}" | head -n1
}

# Header.
printf '%-55s  %-9s  %-7s  %-9s  %-9s  %s\n' \
  "URL" "MODE" "STATUS" "BYTES" "TIER" "VERDICT"
printf '%-55s  %-9s  %-7s  %-9s  %-9s  %s\n' \
  "$(printf '%.0s-' $(seq 1 55))" \
  "$(printf '%.0s-' $(seq 1 9))" \
  "$(printf '%.0s-' $(seq 1 7))" \
  "$(printf '%.0s-' $(seq 1 9))" \
  "$(printf '%.0s-' $(seq 1 9))" \
  "$(printf '%.0s-' $(seq 1 8))"

idx=0
while IFS= read -r line || [[ -n "${line}" ]]; do
  # Skip blanks and comments.
  trimmed="$(echo "${line}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  [[ -z "${trimmed}" || "${trimmed:0:1}" == "#" ]] && continue

  url="${trimmed}"
  idx=$((idx + 1))
  short_url="${url:0:55}"

  # 1. Baseline.
  baseline_out="${RESULTS_DIR}/${idx}-baseline.jsonl"
  baseline_json="$(fetch_one baseline "${url}" "${baseline_out}")"
  bs=$(jq -r '.status_code // 0' <<<"${baseline_json}")
  bb=$(jq -r '.body_bytes // 0' <<<"${baseline_json}")
  bt=$(jq -r '.tier // "-"' <<<"${baseline_json}")
  be=$(jq -r '.error // ""' <<<"${baseline_json}")
  bv=$(verdict "${bs}" "${bb}" "${be}")
  printf '%-55s  %-9s  %-7s  %-9s  %-9s  %s\n' \
    "${short_url}" "baseline" "${bs}" "${bb}" "${bt}" "${bv}"

  # 2. Tier 1.
  tier1_out="${RESULTS_DIR}/${idx}-tier1.jsonl"
  tier1_json="$(fetch_one tier1 "${url}" "${tier1_out}" --browser-like)"
  s1=$(jq -r '.status_code // 0' <<<"${tier1_json}")
  b1=$(jq -r '.body_bytes // 0' <<<"${tier1_json}")
  t1=$(jq -r '.tier // "-"' <<<"${tier1_json}")
  e1=$(jq -r '.error // ""' <<<"${tier1_json}")
  v1=$(verdict "${s1}" "${b1}" "${e1}")
  printf '%-55s  %-9s  %-7s  %-9s  %-9s  %s\n' \
    "" "tier1" "${s1}" "${b1}" "${t1}" "${v1}"

  # 3. Tier 1+2 via chromium. Forces chromium so we're actually
  # testing the stealth.js patches, not just whether http+headers
  # already won. If chromium isn't installed this row will fail —
  # that's a real signal, not a script bug.
  tier12_out="${RESULTS_DIR}/${idx}-tier12.jsonl"
  tier12_json="$(fetch_one tier12 "${url}" "${tier12_out}" --browser-like --stealth --tiers chromium)"
  s2=$(jq -r '.status_code // 0' <<<"${tier12_json}")
  b2=$(jq -r '.body_bytes // 0' <<<"${tier12_json}")
  t2=$(jq -r '.tier // "-"' <<<"${tier12_json}")
  e2=$(jq -r '.error // ""' <<<"${tier12_json}")
  v2=$(verdict "${s2}" "${b2}" "${e2}")
  printf '%-55s  %-9s  %-7s  %-9s  %-9s  %s\n' \
    "" "tier1+2" "${s2}" "${b2}" "${t2}" "${v2}"

  # 4. Tier 3 via http. Forces --tiers http so we isolate the
  # ClientHello forgery — chromium would use its own real Chrome
  # TLS stack and serve the row regardless, masking any signal.
  # The interesting case is "tier1 http path failed AND tier3
  # http path succeeded" — that's the JA4-blocking footprint.
  tier3_out="${RESULTS_DIR}/${idx}-tier3.jsonl"
  tier3_json="$(fetch_one tier3 "${url}" "${tier3_out}" --browser-like --tls-match chrome --tiers http)"
  s3=$(jq -r '.status_code // 0' <<<"${tier3_json}")
  b3=$(jq -r '.body_bytes // 0' <<<"${tier3_json}")
  t3=$(jq -r '.tier // "-"' <<<"${tier3_json}")
  e3=$(jq -r '.error // ""' <<<"${tier3_json}")
  v3=$(verdict "${s3}" "${b3}" "${e3}")
  printf '%-55s  %-9s  %-7s  %-9s  %-9s  %s\n' \
    "" "tier3" "${s3}" "${b3}" "${t3}" "${v3}"
  printf '\n'

  # Polite pause between targets so we're not bombing any one detector
  # network simultaneously across consecutive lines.
  sleep "${PAUSE_BETWEEN_TARGETS_S}"
done < "${TARGETS_FILE}"

echo "" >&2
echo "raw JSONL records under ${RESULTS_DIR}" >&2
echo "to inspect a single row:" >&2
echo "  jq '.metadata.evasion' ${RESULTS_DIR}/1-tier12.jsonl" >&2
