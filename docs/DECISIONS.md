# trawl — decision log

Architectural and scope calls that deserve a durable written record. One
entry per decision. Newest at the top. Each entry must answer: what, why,
what the data said, and what would change our minds.

---

## 2026-04-10 — Defer Lightpanda pending better data

**Decision:** Skip building the Lightpanda engine for now. Ship the
HTTP→Chromium two-tier ladder as v1's default. Revisit after crawl mode
lands and we can re-run Phase 0 against a sample that isn't dominated
by seed rot.

**Context:** SPEC §7 and the original P1 stage 2 plan called for
Lightpanda as a middle tier between HTTP and Chromium, based on a
guessed 60/30/10 HTTP/Lightpanda/Chromium distribution. The decision
rule in `docs/BENCHMARK.md` committed to measuring before building.

**Phase 0 run, 2026-04-10, n=500 pricing URLs from `seed/companies.csv`:**

```
total           500
reachable       158   (31.6%)
unreachable     342   (68.4%)

successes by tier:
  http            140   avg 765ms
  chromium         18   avg 2.93s  (3.83x slower than http)

failures by category:
  http_4xx        269   (53.8%)   ← dominant failure mode
  dns_failure      26   ( 5.2%)
  timeout          14   ( 2.8%)
  robots_blocked   10   ( 2.0%)
  tls_error         9   ( 1.8%)
  all_tiers_exhausted  6
  http_5xx          3
  parked_domain     2
  connection_refused 2
  spa_shell         1

chromium_escalation_rate = 18 / 158 = 11.4%
```

**Why skip Lightpanda despite 11.4% being in the judgment-call zone:**

1. **Rot dominates the signal.** 54% of pricing URLs are 4xx. The 2019
   seed data has restructured pricing paths. The actual tier decision
   sample (n=158) is too small to drive a Lightpanda build commitment
   — a single outlier moves the rate 0.6 percentage points. The 95% CI
   on 11.4% with n=158 is roughly [7%, 16%], which straddles the skip
   threshold.

2. **Total latency savings are small at this escalation rate.** Scaling
   to the full 7065 benchmark: ~254 chromium pages × 2.93s ≈ 12.4 min
   of chromium engine time. Lightpanda at ~500ms cuts that to ~2 min.
   With 15-worker concurrency, wall-clock savings are ~2-3 minutes on
   a ~15 minute benchmark. Material but not transformative.

3. **The real bottleneck is data quality, not engine speed.** Crawl
   mode (follow homepage → pricing link) could recover many of the
   269 http_4xx rows and dramatically improve reachable count. That's
   a much bigger leverage point than a 2-3 minute engine speedup.

4. **Lightpanda build cost (4-6 hours: binary management subcommand,
   subprocess lifecycle, per-tier proxy plumbing, engine pool
   recycling) is disproportionate to the measured savings.** The
   "premature optimization" warning applies directly.

5. **Skipping is reversible.** The Engine interface, router, stats
   collector, and tier-preference machinery are all in place. Adding
   Lightpanda later is a contained addition, not a refactor. Nothing
   about the current architecture forecloses the option.

**What would change our minds (trigger Lightpanda build):**

- Re-running Phase 0 after crawl mode lands shows the chromium
  escalation rate jumps above **15%** on a significantly larger
  reachable sample (n ≥ 500).
- OR a user-facing benchmark (full 7065 run) shows chromium wall
  clock > 25% of total run time.
- OR a production deployment hits a consistent >20% chromium rate
  on a non-rot-dominated dataset.

**Next steps:**

1. Build crawl mode (homepage → pricing link resolution).
2. Re-run 500-row Phase 0 with crawl enabled.
3. Append a follow-up entry to this decision with the new data.
4. If the rate is <15%, this decision stands and Lightpanda
   stays out of v1. If ≥15%, reopen.

**Authored during session:** 2026-04-10, Claude Code with Jeff.
**Commit reference for data:** `cf01017` + `/Users/jhoot/.trawl/jobs/phase0-500/`.

### Addendum — 2026-04-10: second data point from follow-link run

After building `--follow-link` (commit `ed9eac0`), re-ran the same 500-row
seed against the `homepage` column with CSS selector
`a[href*="pricing"], a[href*="/plans"], a[href*="/price"]`. The prefetch
routes through the full tier router, so SPA homepages can escalate to
chromium for nav DOM discovery.

```
Run B — homepage + follow-link + router-prefetch

total           500
reachable        91   (18.2%)
unreachable     409

successes by tier:
  http            87   avg 183ms
  chromium         4   avg 2.69s

failures by category:
  follow_failed   325   ← 65% of input has no <a href> match for
                          /pricing, /plans, or /price even when the
                          homepage renders successfully
  dns_failure      27
  timeout          14
  other            12   ← homepage 4xx that router propagated
  tls_error         9
  all_tiers_exhausted 8
  robots_blocked    7
  spa_shell         3
  parked_domain     2
  connection_refused 2

chromium_escalation_rate = 4 / 91 = 4.4%
```

**Both data points fall below the 15% reopen trigger:**
- Run A direct pricing_url: 11.4% on n=158
- Run B homepage + follow-link: 4.4% on n=91

The Lightpanda skip decision is reinforced. The follow-link run revealed
a distinct, larger problem: **pricing-page discovery from homepages is
~65% miss rate** with a simple href substring selector. That's a
product/data problem, not an engine problem — no middle tier would
change the numerator because the failures are structural (`<button>`
nav, client-side routing, non-matching terms like `/upgrade`).

**Next steps (not yet decided):**

1. Build a richer pricing-link selector library (10+ patterns including
   `/subscribe`, `/upgrade`, `/buy`, `a:contains("Pricing")`).
2. Implement hybrid discovery: try `pricing_url` from CSV, fall back to
   homepage+follow-link on 4xx.
3. Parse `/sitemap.xml` where available.
4. Accept the ~158 reachable pricing pages as the benchmark's baseline
   and document the 70% rot as a seed-quality finding rather than an
   engineering gap.

None of these open the Lightpanda question. This decision is closed
pending a meaningfully different reachable sample (n ≥ 500 with a rate
above 15%).

**Caveat the next session must know:** both measurements were taken on
the discoverable subset of pricing pages (~35% of input). The 65% that
missed are exactly the pages hidden behind interactive widgets,
`<button>`-only navigation, and JS-rendered pricing tables — which are
also the pages most likely to need a JS engine to render. The sample
is biased toward the easy half. **If discovery improves to >70% reach
AND re-measurement on the expanded population shows the escalation
rate climbing past 15%, the rule auto-reopens.** This isn't a flaw in
the decision (the rule was followed correctly given the data
available); it's a footnote that the "skip" verdict is conditional on
the measurable population at the time of measurement.
