# trawl — decision log

Architectural and scope calls that deserve a durable written record. One
entry per decision. Newest at the top. Each entry must answer: what, why,
what the data said, and what would change our minds.

---

## 2026-04-11 — Tier 1 + Tier 2 evasion: build speculatively, waive the consumer-ask rule

**Decision:** Ship `--browser-like` (Tier 1: rotating UAs, Chrome
header set, in-memory cookie jar, ±20% jitter) and `--stealth`
(Tier 2: chromium init script patching navigator.webdriver and
friends) without waiting for a consumer to report a hostile-target
failure. Tier 3 (uTLS fingerprint forgery) and Tier 4 (proxy
rotation) **stay deferred** behind their existing decision rules.

**Context:** `docs/EVASION.md` §5.1 and §5.2 each specified a
"ship when a consumer reports failure mode X" gate. The principled
argument for those gates was that speculative evasion features
guarantee an ad-hoc shape that embeds the first consumer's specific
bypass. The counter-argument that won this round: Jeff has decided
the next hostile target should hit a tool that's already ready,
not discover the gap mid-incident. The shape was already designed
in EVASION.md before any consumer pressure existed, so the original
"ad-hoc shape" risk is mitigated — the doc was the design exercise,
this PR is the build exercise.

**What the data said:** N/A — explicitly speculative. The justification
is "the design doc is mature and the cost of building Tier 1+2 is
small." Tier 3's maintenance cost (uTLS fingerprints rotate) and
Tier 4's scope (proxies are their own phase) keep them deferred —
those gates are still in force.

**What would change our minds (= revert to deferred):**

- A consumer reports that Tier 1+2 broke against a real target in a
  way the doc didn't anticipate, AND fixing it requires changing the
  shape we built (not just adding a new patch to stealth.js). That's
  evidence the speculative build encoded a wrong assumption.
- The maintenance burden of stealth.js patches grows beyond ~200
  lines or starts requiring per-target customization. At that point
  we either vendor an upstream lib or refuse to chase the patch
  treadmill (per the §5.2 "we don't fight a sophisticated arms race"
  framing).

**Implementation deviations from EVASION.md (recorded inline in
the §5.1 / §5.2 SHIPPED subsections, summarized here):**

- Cookie jar is in-memory per-job, not on-disk per-host (resolved
  the §9 "leaning in-memory" question in favor of in-memory).
- UA rotation is sticky-per-host (matched the §9 lean).
- The pool excludes Firefox/Safari because their `Sec-CH-UA` headers
  differ from Chromium's — mixing them in would create inconsistent
  header blocks. The doc was silent on this.
- `metadata.evasion` is a typed nested struct, not a `map[string]any`
  (because `output.Metadata` is already a typed struct everywhere
  else and the consistency mattered more than the doc's example
  shape).
- Stealth script is in-tree (~110 lines), embedded via `//go:embed`,
  rather than vendored from `chromedp-undetected` (resolved the §9
  question against vendoring).

## 2026-04-10 — Feature batch: crawl, map, screenshots, cache, schema, CSV, retries, politeness

**Decision:** Ship eight features in a single session as a Firecrawl
parity pass and quality-of-life improvements, with specific non-obvious
shape calls recorded here so future maintainers understand the
semantics without re-reading the commits.

**Context:** SPEC §13 deliberately excluded LLM extraction, webhooks,
and a web UI — but left many smaller features unspecified. The
ROADMAP's gap analysis against Firecrawl flagged seven of these as
in-scope and small-to-medium cost. All were built in one session
2026-04-10 and pushed as commits `112c713` and `b77d81d`. The phase
writeups in `docs/ROADMAP.md` have the full detail; this entry
captures the architectural calls that weren't otherwise obvious from
the code.

### Schema extraction (`--schema`) — v1 shape

**Unblocking consumer:** Jeff's business partner scraping Stanford
Encyclopedia of Philosophy articles via Firecrawl. SEP gave us a
concrete, non-trivial schema shape (flat fields + arrays-of-objects
with attribute extraction) to design against.

**Design calls worth remembering:**

1. **Empty selector = "self"** inside a nested `fields` context. The
   SEP case proves this is necessary: every array-of-objects
   extraction wants to grab text/attr from the iterated element
   itself, not a child. Alternative syntaxes considered: `"."`,
   `":self"`, `"&"` (Sass). Empty was picked because it's the least
   noisy and parse-unambiguous. Top-level empty selectors are
   rejected at Load time so the "self" meaning can't leak up.
2. **Relative URLs stay relative.** `extracted.link = "../foo/"` in
   the output, not `"https://host/foo/"`. Reason: SEP consumers need
   to cheaply distinguish intra-encyclopedia links from external
   ones. They already have `record.canonical_url` if they want to
   resolve. What would change our minds: a concrete consumer that
   needs absolute URLs by default AND whose consumers can't join
   against canonical_url.
3. **Missing matches are omitted from output**, not present as `""`
   or `null`. Consumers can `jq 'select(.extracted.foo)'` to filter
   on presence. Explicit `required` fields are v1-excluded —
   silent omission is simpler and sufficient for known consumers.
4. **`version: 1` is mandatory.** Future breaking changes can ship
   without inventing a second format.

### Content cache (`--cache`) — semantic subtleties

1. **Opt-in, not opt-out.** A fresh `trawl scrape` must never
   surprise the user with stale data from a week ago. Users who
   want caching ask for it with `--cache`. What would change our
   minds: a prominent-enough "served from cache" log line that
   surprise is no longer a risk.
2. **Key is `(canonical URL, tier name)`, not URL alone.** Different
   tiers can produce different bodies for the same URL (HTTP vs
   chromium), so a forced-tier re-run should miss the cache entry
   that a different tier populated. Inside the router tier loop,
   cache lookup happens per-tier before each Fetch.
3. **Cached results still run through validity.** A stale stub
   body in the cache escalates past exactly as if the live fetch
   had returned it — the cache doesn't trap a user in bad content.
4. **Puts only on successful LIVE fetches.** Cache-replay successes
   are NOT re-put. The stored body's timestamp is fixed at the
   moment of the original live fetch; a replay does not "refresh"
   the TTL.
5. **Tier-learning is NOT updated from cache hits.** The learning
   signal is "what served this host LIVE." A single successful
   replay shouldn't lock a host's tier preference.

### HTTP retries (`--retries`) — scope boundaries

1. **Network layer only.** Retries fire on connection-level
   transients (timeouts, ECONNRESET/REFUSED, truncated EOF,
   io.EOF at Client.Do, HTTP 429/502/503/504). They do NOT fire
   on stub-body responses — those stay with the router's
   validity → escalate path. A stub body is a successful fetch
   from TCP's point of view; retrying it would waste budget on a
   server that's serving exactly what it meant to.
2. **Permanent failures return immediately**: ctx cancel/deadline,
   TLS cert verification errors (`*tls.CertificateVerificationError`,
   `x509.UnknownAuthorityError`, `x509.HostnameError`), all 4xx
   except 429. No amount of retrying fixes an expired cert or a
   404.
3. **Chromium does NOT get retries in v1.** chromedp's timeout
   model is different and chromium fetches fail much less often
   from network transients (they fail from JS hangs and memory
   pressure, which retries don't help). Keeping retries HTTP-only
   means one reliable retry path instead of two partially-overlapping
   ones. What would change our minds: a chromium-heavy workload
   with measurable transient failures.
4. **Exponential backoff with jitter, capped at 10s INCLUDING
   jitter.** An earlier draft capped before applying ±25% jitter,
   which meant the observed delay could exceed the cap by up to
   25%. Fixed so "max 10s" means observed max is 10s.

### CSV output (`-o results.csv`) — column strategy

1. **Extension sniffing, no flag.** `.csv`/`.tsv` → CSV sink,
   everything else (and stdout) → JSONL. This matches how most
   Unix tools work and keeps the CLI simple.
2. **Auto-discovered columns from the first record**, not from
   the user's `--selector` list. Reason: a user running
   `trawl batch --selector 'title=h1' --selector 'price=.price'
   -o out.csv` expects `url, canonical_url, ..., title, price`
   to "just work" without repeating themselves in `--csv-columns`.
3. **Column set LOCKS at first write.** Later records with new
   `extracted` keys silently drop those keys. No way to rewrite
   the header once downstream tooling has read it, so we'd rather
   be honest about the tradeoff than pretend we can stream-append.
   `DroppedKeys()` tracks the skipped keys so a post-run summary
   is possible.
4. **Non-scalar values get JSON-encoded inline.** Ugly, but CSV
   is single-valued per cell by definition. `jq` / pandas can
   re-parse. The alternative — dropping non-scalar values silently
   — would lose schema-extracted data without warning.
5. **Dead-letter queue stays JSONL regardless of primary sink
   format.** Benchmark scripts depend on its shape and nobody
   wants a dead-letter CSV that drops half the fields.

### Per-host politeness (`--politeness`) — match rules

1. **Exact host OR `*.suffix` wildcard.** No regex. Regex is a
   rabbit hole of "does this mean the whole string or a substring"
   questions and the overwhelming majority of real overrides are
   "slow-crawl this specific host" or "slow-crawl this TLD." What
   would change our minds: a consumer with a legitimate need for
   regex (not just a preference).
2. **`*.suffix` does NOT match the bare TLD.** `*.gov` matches
   `irs.gov` but not `gov`. This is the Python `fnmatch` "at least
   one label" rule.
3. **Rate AND concurrency overridable, burst stays global.** Burst
   matters less than sustained rate for polite crawling, and
   per-host burst adds a third knob to reason about.
4. **First-match-wins, top-to-bottom.** Users put specific rules
   before catch-alls. Alphabetical sorting would be less
   predictable.
5. **Loaded once at command start.** File changes mid-run don't
   take effect — restart the crawl. Runtime reload would be a lot
   of plumbing for a feature nobody has asked for.

### BFS crawl (`trawl crawl`) — termination protocol

Worth recording because `sync.Cond`-based worker pools are subtle:

- **`BlockingNext` signals `ErrEmpty` only when queue is empty AND
  `inFlight == 0`.** Either condition alone isn't enough — an
  empty queue with workers still processing could yield new
  children any moment.
- **`inFlight` is an in-memory counter**, not derived from
  `StateInFlight` in the frontier DB. It's reconstructed as zero
  on startup because `Recover` drains all in_flight records back
  to queued before workers spin up.
- **Link discovery happens BEFORE `MarkDone`.** If the order were
  reversed, a sibling worker could wake on the empty-queue
  broadcast, see `inFlight == 0`, and terminate the crawl
  prematurely — right before the children get enqueued.
- **`limit` caps URLs ENQUEUED, not fetched.** Deterministic from
  the frontier's natural unit. Dedup-rejected children release
  their budget slot so a cycle-heavy graph doesn't burn the limit
  on duplicates.

### What did NOT change

- No new measurement data on Lightpanda. The rule in the previous
  entry still stands; none of these features unblock its reopening.
  BFS crawl shipped, but nobody has re-run Phase 0 against the
  richer selector library yet.
- No changes to the existing tier router, politeness default
  rates, or URL canonicalization rules.
- SPEC §13 exclusions (LLM extract, search, webhooks, web UI,
  scheduler, distributed mode) all still stand.

**Authored during session:** 2026-04-10.
**Commit references:** `112c713` (five phases), `b77d81d` (three-feature batch).

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

### Addendum — 2026-04-10: third data point from n=1000 hybrid run

After shipping hybrid discovery (commit `72a25b6` — `--fallback-column`
plus trigger-gated retry on http_4xx / dns_failure), re-ran Phase 0
against the first 1000 rows of `seed/companies.csv` with:

```
--url-column pricing_url
--fallback-column homepage
--fallback-selector 'a[href*="pricing"], a[href*="/plans"], a[href*="/price"]'
--tiers http,chromium --concurrency 15 --rate 1 --timeout 30s
```

```
Run C — hybrid discovery, n=999 (1 row had blank pricing_url)
wall clock        4m35s

total             999
reachable         355   (35.5%)
unreachable       644

successes by tier:
  http            305   avg 661ms
  chromium         50   avg 3.06s   (4.6x slower than http)

chromium_escalation_rate = 50 / 355 = 14.08%

fallback:
  attempted       579   ← rows whose primary failed with http_4xx or dns_failure
  succeeded        24   ← fallback path recovered them
  no_link         464   ← homepage fetched OK, selector matched nothing
  unreachable      91   ← homepage fetch itself failed

failures by category (after hybrid recovery applied):
  http_4xx        511   (down from ~535 pre-hybrid)
  dns_failure      44
  timeout          24
  tls_error        24
  robots_blocked   15
  all_tiers_exhausted 9
  http_5xx          6
  connection_refused 5
  spa_shell         4
  parked_domain     2
```

**Rule evaluation — does this reopen Lightpanda?** No, but it's the
closest call yet:

- Rate: 14.08% vs 15% threshold → **miss by 0.9 points**
- Sample: n=355 reachable vs n≥500 threshold → **miss by 145 rows**

Both thresholds fail. The rule stays formally closed.

**But the trend is the signal.** Across three data points, the
escalation rate has moved exactly as the caveat in the previous
addendum predicted:

```
Run A (n=500, pricing_url only):     11.4%  on n=158 reachable
Run B (n=500, homepage+follow):       4.4%  on n=91 reachable
Run C (n=999, hybrid):               14.08% on n=355 reachable
```

As the measurable population expanded, the escalation rate climbed
toward the threshold. Run B looks like an outlier because it was
follow-only — which selected for the easy homepages without trying
the direct pricing URLs — so the population was the JS-light subset.
Run C's hybrid combines both paths and is the most representative
measurement so far.

**Scaling comparison — is hybrid actually helping?** Yes, modestly:

- Run A scaled linearly to n=1000: ~316 reachable
- Run C hybrid actual: 355 reachable
- **Net: +39 rows, +12% reach improvement** attributable to hybrid

But the fallback path's internal yield is poor: **24 successes out of
579 attempts (4.1%)**. The dominant failure mode is `no_link` at 464
— the homepage loaded fine, the 3-pattern selector just didn't match.
This is exactly the "65% pricing-page discovery miss rate" from the
earlier addendum, re-confirmed on a larger sample. The hybrid machinery
is working; the selector library is the bottleneck it's waiting on.

**Next steps (decided):**

1. Build the richer pricing-link selector library (priority #3 in
   CLAUDE.md). Target the 464 no_link rows with patterns like
   `/subscribe`, `/upgrade`, `/buy`, footer selectors, and text-based
   matches. Ordering matters — cheap href substring matches first,
   expensive text-content matches second.
2. Re-run Phase 0 with the new selector set against the same
   `/tmp/seed-1000.csv` so Run D is apples-to-apples with Run C.
3. If Run D pushes reach past n=500 AND chromium rate past 15%, the
   Lightpanda rule **auto-reopens** on its own terms. If reach crosses
   but rate doesn't, the skip decision is reinforced with real data.

**Sitemap parsing (priority #2) is deferred to after Run D** because
the hybrid pipeline we already built is sitting idle on 464 homepages
waiting for better selectors. Highest marginal value is unlocking
that existing infrastructure, not building new discovery paths.

**Authored during session:** 2026-04-10.
**Commit reference for data:** `72a25b6` + `~/.trawl/jobs/phase0-1000-hybrid/`.
