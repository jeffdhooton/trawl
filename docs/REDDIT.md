# Trawl — Reddit Strategy

**Status:** Recipe. No code changes required; works against trawl as of
v0.7.1 with existing flags and the two schemas shipped in
`docs/examples/reddit-subreddit.yaml` and
`docs/examples/reddit-thread.yaml`.
**Audience:** users who want Reddit content out of trawl, and future
contributors tempted to add "reddit support" as a new engine or flag.
The point of this doc is to document the routes that work today so
that doesn't happen prematurely.

---

## 1. Why this doc exists

Reddit is famously hostile to scrapers since the 2024 API lockdown and
the Google licensing deal. `www.reddit.com` serves a bot-verification
interstitial to non-allowlisted clients, and `robots.txt` itself is
served as a "Blocked" HTML page. A fresh trawl user pointing at Reddit
sees a wall of `blocked by robots.txt` errors, concludes the tool
doesn't support Reddit, and moves on.

In fact trawl can pull Reddit content cleanly today — no new engine,
no stealth escalation, no proxy pool. The trick is routing to the
right subdomain with the right flags. This doc is that recipe.

---

## 2. The three routes, ranked

```
     simplest + canonical         structured CSS        not recommended
              │                         │                       │
              ▼                         ▼                       ▼
       Route A: .json API    Route B: old.reddit.com   Route C: stealth
          HTTP (no evasion)       HTTP + browser-like    chromium on www
```

### 2.1 Route A: the `.json` API (recommended default)

Every Reddit URL accepts a `.json` suffix and returns canonical,
typed data — the same shape the deprecated public API returned. No
HTML parsing, no selector fragility, no schema needed. Works on
`www.reddit.com` without any TLS or UA evasion: Reddit gates the
HTML bot wall but leaves the `.json` endpoint open for polite
clients.

```bash
trawl scrape "https://www.reddit.com/r/golang/new.json?limit=25" \
  --format json --ignore-robots -o golang.jsonl

# Full comment tree for a single thread:
trawl scrape \
  "https://www.reddit.com/r/golang/comments/1skayg7.json?limit=500&depth=10" \
  --format json --ignore-robots
```

On a JSON-content-type response with no schema configured, `--format
json` passes the body through unchanged — this is the
direct-to-API path. `--format html` and `--format markdown` work too
(both also pass through on JSON responses) but `--format json` is the
honest label.

What you get:
- Canonical typed data. No flag incantation beyond `--ignore-robots`.
- Comment trees come back fully nested (parent → children) — no
  post-processing to reconstruct threads.
- `after` / `before` pagination cursors inside the JSON response.
- ~230ms per request; 100 posts per page via `limit=100`.

When to use: anywhere you want Reddit data. This is the default.
Reach for Route B only if you need CSS-selector extraction for a
specialized pipeline, or if you want subreddit sidebar metadata (bio,
rules, moderator list) that the API doesn't expose cleanly.

### 2.2 Route B: old.reddit.com (when you want schema extraction)

The `old.reddit.com` subdomain serves the pre-2018 HTML UI — no React,
no hydration, full content in the initial response. Reddit has kept it
up for accessibility and power-user reasons and has (so far) not bot-
walled it the way `www.reddit.com` is walled.

```bash
trawl scrape https://old.reddit.com/r/golang/ \
  --schema docs/examples/reddit-subreddit.yaml \
  --format json \
  --ignore-robots --browser-like \
  -o golang.jsonl
```

What you get:
- The HTTP tier handles it — no chromium cost, ~500–800ms per page.
- Every post's data-* attributes give you id, score, author, domain,
  comment count, timestamp, rank, flair, NSFW/spoiler/stickied flags.
- Threads work the same way, plus each comment's id, author,
  permalink, reply count, score, body markdown, edited timestamp.
- Pagination URL (`?count=25&after=t3_<id>`) is captured in the
  schema's `next_page` field.

When to use: anything where you want structured post/comment data with
CSS-selector extraction. This is the default.

### 2.3 Route C: stealth chromium against www.reddit.com

Not recommended. Possible in principle — full chromium with `--stealth
--tls-match chrome` and a warm session cookie can render the React app
— but the payoff is negative: the same data is available cleanly via
Routes A and B at a fraction of the cost, and Reddit's bot-detection
on `www.reddit.com` is aggressive enough that you'll land on the
"Please wait for verification" wall often even with a real browser.

Documented here only to close the loop: if the `.json` endpoint gets
locked down and old.reddit also goes away, this is where the work
starts — likely with a rotating-proxy pool (see `docs/PROXIES.md`) and
an authenticated cookie jar. Until that day, don't reach for it.

---

## 3. The flags that matter

### `--ignore-robots` (required)

Reddit's `/robots.txt` returns a "whoa there, pardner!" HTML block
page to non-allowlisted user agents. Trawl's politeness layer sees a
non-parseable robots.txt and fails closed — correct default, wrong
behavior for Reddit.

`--ignore-robots` makes the ignore explicit and logs a warning. If
that sits uncomfortably: Reddit's ToS permits scraping of public
content at modest rates for personal use; the enforcement surface is
rate-based, not a `robots.txt` directive. Stay polite (see §4) and
you're fine.

### `--browser-like` (Route B only)

Required for `old.reddit.com`: Reddit blocks empty or obviously-botty
User-Agents on the HTML subdomain. The `--browser-like` flag rotates
a realistic Chrome UA with matching headers.

Not needed for Route A. The `.json` endpoint on `www.reddit.com`
serves trawl's default UA without complaint — provided you stay
polite (§4). If you crank concurrency, that stops being true and
`--browser-like` becomes a cheap insurance policy.

### `--tls-match chrome` (not currently needed)

Reddit doesn't appear to TLS-fingerprint the `.json` endpoint today.
Earlier drafts of this doc recommended it; empirically it's
unnecessary. Keep it in reserve: if Reddit tightens and Route A
starts returning the "Blocked" page, this is the first flag to add.

### `--stealth` (not needed)

Don't reach for stealth on Reddit. It only activates when the router
escalates to chromium, and the whole point of this recipe is staying
on the HTTP tier. Stealth adds cost and complexity you don't need.

---

## 4. Politeness

Reddit enforces per-IP rate limits aggressively. The conservative
numbers below are from community reports; adjust to your taste.

Create a `docs/examples/politeness.yaml`-shaped file for Reddit:

```yaml
hosts:
  old.reddit.com:
    requests_per_minute: 60
    max_concurrent: 2
  www.reddit.com:
    requests_per_minute: 60
    max_concurrent: 2
```

Pass via `--politeness reddit-politeness.yaml`. A bit faster than 1
req/s will still work for short jobs; multi-hour crawls should stay
at or below 1 req/s per IP. If you need more throughput, that's a
rotating-proxy-pool problem (see `docs/PROXIES.md`), not a
politeness-override problem.

---

## 5. Pagination recipes

### 5.1 Subreddit pages via Route A (.json API)

`after=t3_<id>` cursors come back in every response's `data.after`
field. Loop until `after` is null (end of listing or Reddit's 1000-
item cap):

```bash
after=""
page=1
while :; do
  url="https://www.reddit.com/r/golang/new.json?limit=100"
  [ -n "$after" ] && url="$url&after=$after"
  trawl scrape "$url" --format json --ignore-robots -o "page${page}.jsonl"
  after=$(jq -r '.body | fromjson | .data.after // empty' "page${page}.jsonl")
  [ -z "$after" ] && break
  page=$((page+1))
done
```

### 5.2 Comment trees via Route A

```bash
trawl scrape \
  "https://www.reddit.com/r/golang/comments/1skayg7.json?limit=500&depth=10" \
  --format json --ignore-robots
```

`limit` caps total comments returned; `depth` caps nesting. Values of
500 / 10 cover ~99% of threads without triggering the "load more
comments" placeholder.

### 5.3 Subreddit pages via Route B (old.reddit schema)

```bash
# First page:
trawl scrape https://old.reddit.com/r/golang/ \
  --schema docs/examples/reddit-subreddit.yaml \
  --format json --ignore-robots --browser-like \
  -o page1.jsonl

# Extract next_page, continue:
next=$(jq -r '.extracted.next_page' page1.jsonl)
trawl scrape "$next" \
  --schema docs/examples/reddit-subreddit.yaml \
  --format json --ignore-robots --browser-like \
  -o page2.jsonl
```

---

## 6. Known footguns

### 6.1 The bot wall slips past validity

When `www.reddit.com` returns its "Please wait for verification"
interstitial, it's an HTTP 200 with ~8KB of HTML and a `text/html`
content-type. Trawl's validity heuristic sees a successful fetch and
does not escalate to chromium. Consumers who don't check the page
title will silently ingest interstitials as content.

Mitigation today: always prefer Route A or Route B. Route C is where
this bites.

Future work: `internal/validity` could gain a signature check for
Reddit's specific bot-wall HTML and flag it as `blocked_by_bot_wall`
so the router either escalates or fails cleanly. Noted in
`docs/TODO.md`.

### 6.2 Score fuzzing on comments

Comment `score` is vote-fuzzed on old.reddit — the `.score.unvoted`
span's title attribute is the closest to real but still has small
perturbation applied by Reddit. Subreddit-listing `data-score` is
less-fuzzed. For exact counts, use Route B (.json API).

### 6.3 Stickied posts appear at rank 1–2

Schema-extracted `rank` includes stickied announcements. Filter on
the `stickied` field in post-processing if you want organic ranking.

### 6.4 "load more comments" placeholders

Route A's thread schema captures `.morechildren` stubs as
`more_comments_stubs` so you can count them. Expanding them requires
a POST to `/api/morechildren.json` with a list of child IDs, which
trawl doesn't do today (and shouldn't — it's an endpoint-specific
API call, not general-purpose scraping). If you need full comment
trees, use Route B.

### 6.5 Private and quarantined subreddits

Route A returns a login wall. Route B returns
`{"reason":"private","message":"Forbidden"}`. Both are out of scope —
trawl doesn't authenticate. If you need private content, Reddit's
official OAuth API is the right tool, not trawl.

---

## 7. What would change this recipe

Scenarios that would trigger a rewrite:

- **old.reddit.com goes away.** Reddit has talked about sunsetting it
  for years; when it happens, Route A dies and the recipe becomes
  "Route B for everything, fall back to chromium + proxies." The
  schemas in `docs/examples/` would be deprecated.
- **The `.json` endpoint gets locked to authenticated-only.** Would
  push Route A back to sole-primary and likely force a shift to
  chromium + proxy rotation for heavy use.
- **Reddit's TLS/IP fingerprinting tightens on old.reddit too.** Would
  require `--tls-match chrome` on Route A as well; recipe mechanically
  widens but stays the same shape.
- **A real consumer signal for Reddit-specific crawl depth.** If
  someone needs full comment expansion across many threads, that's
  when a `--reddit-expand-morechildren` feature earns its keep. Until
  then, Route B already handles it with `limit` + `depth`.

---

## 8. What trawl explicitly does not do for Reddit

- **Authentication.** No OAuth, no cookie jar management. Public
  content only.
- **Posting, voting, subscribing, or any state-changing actions.**
  Trawl is read-only by design.
- **Bypassing the bot wall on www.reddit.com.** Documented as Route C
  but deliberately not supported as a first-class recipe. If you need
  it often enough that Routes A and B aren't sufficient, the right
  answer is Reddit's OAuth API, not a scraper arms race.
- **Real-time comment streaming.** Trawl is batch/poll; use Reddit's
  websocket API or PushShift-style archives for streaming.

These align with trawl's general non-goals (SPEC §13): no
authenticated content, no CAPTCHA solving, no arms race with
bot-detection.
