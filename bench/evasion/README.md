# Evasion target benchmark

Tiny harness for measuring trawl's Tier 1 + Tier 2 evasion against
real public targets. Empirical input for the
`docs/EVASION.md` §5.3 Tier 3 decision rule: if Tier 1+2 already wins
on the targets we expected to need Tier 3, we don't need Tier 3 yet.

## Files

- `targets.txt` — categorized URL list. Comment lines explain which
  tier each target is *expected* to need before running. Update the
  comments after each run with what was *observed* so the file
  becomes a living record.
- `run.sh` — sweeps every target through four modes (baseline /
  Tier 1 / Tier 1+2 / Tier 3) and prints a comparison table. Raw
  JSONL goes into a timestamped `results-YYYYMMDD-HHMMSS/`
  directory.

  - **baseline:** default trawl, no evasion.
  - **tier1:** `--browser-like`. http path with rotating UA + full
    Chrome header set + cookie jar + ±20% jitter. Default tier
    ladder, so chromium fallback is allowed.
  - **tier1+2:** `--browser-like --stealth --tiers chromium`. Forces
    chromium so we test the stealth.js init script, not whether
    headers + UA already won.
  - **tier3:** `--browser-like --tls-match chrome --tiers http`.
    Forces http so we test ClientHello forgery in isolation —
    chromium would use its own real Chrome TLS and mask the signal.
    The interesting transition is `tier1=blocked` (or fell back
    to chromium) AND `tier3=ok` → JA3/JA4-blocking footprint that
    Tier 3 unlocks.

## Running it

```bash
# from the repo root
go install ./cmd/trawl
bench/evasion/run.sh
```

Requirements: `trawl` on PATH, `jq` installed, a local Chrome/
Chromium for the Tier 1+2 mode (the same one trawl already uses
for `--tiers chromium`).

The script paces itself with a 3-second sleep between targets and
caps each fetch at 45 seconds. ~2-3 minutes wall clock for the full
list.

## Reading the table

```
URL                                                      MODE       STATUS   BYTES      TIER       VERDICT
https://www.indeed.com/q-software-engineer-jobs.html     baseline   403      245        http       blocked
                                                         tier1      200      87412      http       ok
                                                         tier1+2    200      87530      chromium   ok
```

Verdict heuristic:
- `blocked` — HTTP status ≥ 400.
- `stub`    — status 200 but body < 1500 bytes (SPA shell or empty
  doc, almost certainly didn't really render).
- `ok`      — status 200 with a real-looking body.
- `error`   — trawl returned a non-zero exit or no JSONL.

## What the results mean

The interesting cases are *transitions* between modes:

- `baseline=blocked, tier1=ok, tier1+2=ok` — Tier 1 was sufficient.
  The site gates on User-Agent or missing browser headers.
- `baseline=blocked, tier1=blocked, tier1+2=ok` — Tier 2 was needed.
  The site runs a JS fingerprint check that the stealth init script
  defeats.
- `baseline=blocked, tier1=blocked, tier1+2=blocked` — Tier 1+2 was
  insufficient. Either the site uses TLS fingerprinting (Tier 3) or
  it's behind a behavioral check / CAPTCHA / Cloudflare Challenge
  (Tier 3+ wall, refused per §6).
- `baseline=ok` everywhere — the site doesn't care, and the row is
  just a control.

## Etiquette

- These are real public sites with real anti-abuse infrastructure.
  Each script run = three fetches per target. Don't loop the script.
  Don't add ten copies of the same domain to `targets.txt`.
- If a target starts returning challenges to your IP, that means you
  *lost* — back off for a day, don't escalate. See EVASION.md §6.
- The script honors trawl's polite-by-default — robots.txt is
  respected, jitter is on in tier1 / tier1+2 modes.

## Updating targets.txt

Add a target if:
- It's a public, non-authenticated page.
- You have a hypothesis about which tier it needs.
- It represents a *category* (pricing pages, job listings, e-commerce
  PDPs, etc.) that isn't already covered.

Don't add:
- Login-walled pages (not what trawl is for).
- Sites you don't have a defensible reason to scrape.
- Anything that's already in the seed corpus.
