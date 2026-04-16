# Trawl Benchmark — SaaS Pricing Pages

The end-to-end test that proves trawl is real. Runs against `seed/companies.csv` (~7065 unique SaaS domains) and produces a structured pricing dataset as the artifact.

> See `docs/SPEC.md` §11 for the formal success criteria. This doc is the operational playbook for actually running the benchmark.

---

## What we're testing

Pricing pages are the worst-case tier mix in one job:

- **Some are static SSR HTML.** `Stripe`, `Linear`, `Vercel` marketing pages. → Tier 1 (HTTP).
- **Some are React/Vue SPAs that hydrate on load.** Many indie SaaS sites. → Tier 2 (Lightpanda).
- **Some hide pricing behind interactive Stripe widgets / accordions.** → Tier 3 (Chromium).
- **Some are gated behind "talk to sales" with no public price.** → Extraction failure, logged.
- **Some are behind Cloudflare's hard wall.** → Anti-bot detection, dead-lettered.

If trawl can route this list correctly *without per-URL configuration*, the router works. If it can't, the router doesn't.

---

## Decision rule: does Lightpanda ship at all?

> **This section exists to prevent "measure first" from silently becoming "measure eventually."**

The original spec guessed a 60/30/10 HTTP/Lightpanda/Chromium tier distribution. That guess drove the decision to build Lightpanda as the middle tier. As of P1 stage 1 (2026-04-10), no actual measurement exists to support it. Before any Lightpanda work happens, Phase 0 of this benchmark runs and produces the real tier distribution.

### Non-negotiable commitments

1. **Phase 0 runs the same day the unblocking features land.** The unblocking features are: CSV input (DONE), YAML extract config, failure categorization, stats.json output. Crawl mode is optional for Phase 0 if `pricing_url` from the seed CSV is used directly. "Same day" means within 24 hours of the last feature commit, not "next week," not "when convenient."

2. **The Lightpanda decision is made immediately after Phase 0.** It does not drift into a future sprint. It does not wait for a second opinion. The decision happens the same session that Phase 0 completes, using the Phase 0 stats.json as the sole input.

### Decision thresholds

Let `escalation_rate = (lightpanda_eligible + chromium_needed) / total_reachable` measured against the Phase 0 run, where "lightpanda-eligible" means "HTTP tier escalated AND the content would have been served by a JS-executing engine that is not a full Chromium." In Phase 0 we can only observe `chromium_needed` directly (since there's no Lightpanda tier yet) — so for the Phase 0 decision, use `chromium_escalation_rate = chromium_needed / total_reachable`.

| `chromium_escalation_rate` | Decision |
|---|---|
| **< 10%** | **Skip Lightpanda entirely.** Document the observed rate in a DECISIONS.md entry and explicitly mark Lightpanda out of scope for v1. The HTTP→Chromium two-tier ladder is sufficient. |
| **10% – 25%** | **Judgment call with real numbers.** Consider: Chromium cold-start cost on the benchmark box, total wall-clock delta vs forced-Chromium, pricing-page specific behavior. Write the reasoning into DECISIONS.md whichever way it goes. |
| **> 25%** | **Build Lightpanda.** The middle tier pays for itself. Proceed with the original P1 stage 2 plan. |

### Calibration notes

- **Pricing pages are more JS-heavy than random homepages.** The 50-URL mixed test (2026-04-10) showed 4% chromium escalation across a hand-picked mix of Wikipedia/docs/marketing/tools. Expect Phase 0 to be dramatically higher — possibly 40-60% — because Stripe Pricing Tables, Chargebee embeds, tier-toggle widgets, and custom React pricing components all hydrate client-side. The 50-URL number is **not** a prior for the benchmark.
- **The 19% chromium rate on the partial `homepage` column smoke** (also 2026-04-10, ~179 companies) is a weak upper bound, not the real number. Homepages are lighter than pricing pages and many of those 19% were failures (parked domains, 403s, DNS issues) not real escalations. The Phase 0 run using `--url-column pricing_url` with failure categorization enabled will produce the number that matters.
- **"Reachable" excludes dead rows.** A 2019-era CSV has ~15-25% DNS/TLS/parked failures (per Phase 1 rot test design). Those rows are excluded from the denominator of the decision rule — they aren't tier decisions, they're dead data.

### Artifacts the decision requires

The Phase 0 run MUST produce:

- `runs/smoke/stats.json` with at minimum: `total`, `reachable`, `tier_distribution: {http, chromium}`, `failures_by_category`, `wall_clock_ms`.
- `runs/smoke/results.jsonl` with per-URL `tier` populated, so the ratio is verifiable.
- `runs/smoke/dead_letter.jsonl` with the unreachable rows (so they can be excluded from the denominator).

If any of these are missing, the decision is postponed until they exist — NOT made on incomplete data.

---

## Extraction targets (per page)

```
{
  "company": "Linear",
  "homepage": "https://linear.app",
  "pricing_url": "https://linear.app/pricing",
  "tier_used": "lightpanda",
  "plans": [
    { "name": "Free",     "price": "$0",  "period": "month", "currency": "USD" },
    { "name": "Standard", "price": "$8",  "period": "month", "currency": "USD", "per": "user" },
    { "name": "Plus",     "price": "$14", "period": "month", "currency": "USD", "per": "user" }
  ],
  "contact_sales": false,
  "extracted_at": "2026-04-10T..."
}
```

Minimum viable extraction: company name + at least one plan with a price (or `contact_sales: true`).

---

## Phased run plan

Don't run all 7065 on day one. Build confidence in stages.

### Phase 0 — Smoke test (100 rows, ~5 min)

```bash
head -101 seed/companies.csv > /tmp/smoke.csv
trawl batch /tmp/smoke.csv \
  --url-column pricing_url \
  --schema extract/pricing.yaml \
  --output runs/smoke.jsonl \
  --concurrency 20
```

(If you haven't built a schema yet, `--selector 'title=h1' --selector
'price=.price'` is a zero-config fallback that still exercises the
pipeline end-to-end.)

**Pass criteria:**
- ≥80% return *any* extraction (low bar — proves the pipeline runs)
- Tier distribution is non-trivial (not 100% in any single tier)
- No crashes, no goroutine leaks
- Output is valid JSONL

Use this to iterate on the extraction config without burning the full run.

### Phase 1 — Rot test (889 rows, ~10 min)

The connor11528 subset is from 2019. Expect 15-25% dead, parked, or redirected.

```bash
grep ',connor11528,' seed/companies.csv > /tmp/rot.csv
trawl batch /tmp/rot.csv \
  --url-column pricing_url \
  --schema extract/pricing.yaml \
  --output runs/rot.jsonl
```

**What this stresses:** failure handling, dead-letter queue, redirect chains, DNS NXDOMAIN, certificate errors, parked domain detection. Trawl should NOT crash or hang on any of these — every failure should be a logged record, not a process exit.

**Pass criteria:**
- Process exits cleanly
- Every input URL has either a result row or a dead-letter row
- Failure modes are categorized in stats: `dns_failure`, `tls_error`, `404`, `parked`, `cloudflare_block`, `soft_block`, `extraction_failed`

### Phase 2 — Full run (7065 rows)

The real benchmark.

```bash
trawl batch seed/companies.csv \
  --url-column pricing_url \
  --fallback-column homepage \
  --fallback-selector 'a[href*="pricing"], a[href*="/plans"], a[href*="/price"]' \
  --schema extract/pricing.yaml \
  --politeness docs/examples/politeness.yaml \
  --output runs/full.jsonl
```

(Prometheus metrics endpoint is deferred to P2 per `docs/ROADMAP.md`.
Use `stats.json` in the job dir for run-end aggregates.)

**Success criteria** (from `docs/SPEC.md` §11):

| Metric | Target |
|---|---|
| Pages with valid extraction | ≥90% of *reachable* pages (≥75% of all input) |
| Tier distribution | Roughly 60/30/10 HTTP/Lightpanda/Chromium |
| Wall-clock runtime | <30% of "force chromium" baseline |
| Resumability | Kill at 50% → resume → identical final output |
| Peak RAM | <2GB on the box running trawl |
| Output format | Valid JSONL, one record per input URL (success or failure) |

### Phase 3 — Force-Chromium baseline

```bash
trawl batch seed/companies.csv \
  --url-column pricing_url \
  --schema extract/pricing.yaml \
  --force-tier chromium \
  --output runs/chromium-only.jsonl
```

**Why:** the router is only valuable if this comparison shows a real speedup. If forced-Chromium is *not* substantially slower than the routed run, the router isn't pulling its weight.

Compare:
- Wall-clock time (router run vs forced-Chromium run)
- Memory peak
- Success rate (should be roughly equal — Chromium handles everything Lightpanda does, just slower)
- Cost-per-page if running on a metered VPS

A 3x speedup is acceptable; 5x+ is the goal.

---

## Output artifacts

Each run produces:

```
runs/<run_id>/
├── results.jsonl        # one line per URL: extraction OR failure
├── stats.json           # final stats: tier distribution, success rate, timings
├── dead_letter.jsonl    # URLs that exhausted all tiers/retries
└── manifest.json        # run config snapshot for reproducibility
```

The successful runs become a usable SaaS pricing dataset on their own, independent of the benchmark.

---

## What "done" looks like

Trawl P2 ships when the full 7065 run:

1. Completes without manual intervention
2. Hits the metric targets above
3. Produces a dataset where >5000 rows have at least one extracted price field
4. Can be killed and resumed at any point with no data loss

Once that lands, the same harness can point at any other seed list (AI tools directory, package metadata, conference talks) without code changes.

---

## Notes for the next operator

- **Run on a real network**, not coffee shop wifi. 7065 outbound connections will get you flagged as a port scanner on captive portals.
- **Don't run from a residential IP if you can avoid it.** Use a VPS with a clean datacenter IP block. Any rate-limit or anti-bot ban applied to your home IP is annoying to recover from.
- **Set politeness defaults conservatively for the first full run.** `--rate-per-domain 0.5/s --concurrent-per-domain 2`. Most pricing pages are unique domains so the global concurrency is what matters; per-domain caps are mostly cosmetic here.
- **Save the run dir.** Don't `rm -rf runs/`. The historical comparison data is the most valuable thing trawl produces — every subsequent run gets compared against the previous baseline.
