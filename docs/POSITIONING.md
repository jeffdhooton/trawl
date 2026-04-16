# Trawl — Positioning

This doc captures *how trawl is positioned in the market*, not what it does technically. Read this before writing the README, marketing copy, or any external-facing description. The goal is to prevent the most common positioning failure mode: framing trawl as "the open-source alternative to [hosted SaaS]," which puts trawl in someone else's gravity well and loses the comparison by default.

> **Maintainer's note:** trawl exists because of a specific structural gap in the web scraping market. The gap isn't "no good scrapers exist" — there are excellent ones. The gap is "no scraper exists that's designed for AI agents to call directly, runs entirely on your own machine, and doesn't charge per page." Hold that frame when in doubt about how to describe the project.

---

## 1. The one-line pitch

> **Trawl is local-first web scraping for AI agents.**

Not "web scraper." Not "Firecrawl alternative." Not "Playwright but faster." Local-first web scraping for AI agents. Every word is load-bearing:

- **Local-first** — runs as a binary on your machine. Not a service, not a cloud, not an API. Your data, your infra, your control.
- **Web scraping** — what it does. Don't dress it up.
- **For AI agents** — the primary user is an autonomous agent in a tool-use loop, not a human developer integrating an SDK into a web app.

Every other dimension of the pitch (tiered routing, resumability, no per-page fees, JSON-first output) flows from those three words.

---

## 2. Who trawl is for

**The primary user is an AI agent.** When trawl is invoked, it is almost always being invoked by another piece of software — most commonly Claude Code or a similar agent in an autonomous loop. The CLI is designed for that consumer: structured output by default, deterministic exit codes, JSON schemas that don't drift between releases, no interactive prompts, no auth tokens, no rate limit headers to parse.

The secondary users are humans in narrow situations:
- **Solo operators** running personal scraping projects where the recurring cost of a hosted SaaS would be material (>1000 pages/month).
- **Researchers and data scientists** who want a one-shot dataset built locally and don't want their target URLs going through a third party.
- **Privacy-sensitive scrapers** — anyone who can't ship the URLs they're crawling, or the auth headers they're using, to an external service (HIPAA, internal tools, customer data, anything regulatory-adjacent).
- **Developers building AI agents that ship to other people's machines.** If your agent needs to scrape, embedding `trawl` as a binary dependency is dramatically simpler than wiring up an API key flow with a third-party scraping service.

If a potential user doesn't fit any of these descriptions, **send them to Firecrawl, Scrapy, or Playwright** with a clean conscience. Trawl is not trying to be everything for everyone.

---

## 3. Who trawl is NOT for

Be honest about this. The wrong customer is worse than no customer.

| User | Use Firecrawl / hosted SaaS instead |
|---|---|
| Product builder shipping a B2B SaaS that needs scraping as a feature | They want managed infra, anti-bot evasion they don't have to think about, and a billing line they can pass through to customers. Hosted SaaS is the right call. |
| Team that needs 99.9% uptime, dashboards, audit logs, multi-user access | These are SaaS features. Trawl is single-user single-machine. |
| User who wants to scrape Cloudflare / Akamai / PerimeterX-protected sites at scale | Firecrawl's "Fire-engine" has years of proprietary cloud infra for this. Trawl's proxy support helps but won't catch up. Send them away politely. |
| Engineer who wants schema-driven LLM extraction *today* without writing selectors | Firecrawl's `extract` endpoint does this. Trawl's eventual answer is `distill` (a sibling project), but until that ships, Firecrawl wins on this dimension. |
| Anyone who values "sign up, paste API key, get data in ten minutes" | Trawl has a higher activation cost. That's the price of local-first. |

The pattern: **anyone who would rather rent infrastructure than own it** is not a trawl user. Trawl is for people who want to own their tools.

---

## 4. The four positioning pillars

These are the dimensions on which trawl is structurally different from any hosted SaaS scraper, *because of architectural choices the SaaS competitors have made and cannot easily reverse*. Lean on these in every external description.

### 4.1 Local-first by design

> "The scraper that runs in your terminal, not their cloud."

Trawl is a single static Go binary. It runs on your machine. There is no cloud, no service, no account, no dashboard, no API key. Your URLs never leave your network. Your data never touches a third party. Your credentials, if you use any, stay in your shell.

This is a positioning choice that hosted SaaS scrapers literally cannot match without abandoning their business model. It's a structural moat, not a feature.

### 4.2 Designed for agent loops, not web apps

> "Built for AI agents to call, not for humans to integrate."

Most scraping tools (Firecrawl, ScrapingBee, Apify, Bright Data SERP API) are designed to be called from application code that a human wrote. They expose REST APIs, ship Python/Node SDKs, document auth flows, and assume the consumer is a human developer wiring them into a feature.

Trawl is designed to be called by an autonomous AI agent in a tool-use loop. The CLI is the API. Output is JSON by default. There are no auth tokens, no rate limit headers, no SDK installations, no version drift between SDK releases, no webhook callbacks. You shell out to `trawl`, you get a JSON object, you parse it, you move on. The agent never has to think about HTTP semantics or API client lifecycles.

This is the same instinct behind the sibling project `scry` (code intelligence for agents instead of editors). The thesis: **the developer toolchain is being rewritten for agents, and the rewrites are 10-100x simpler because they don't carry the human-UI tax.**

### 4.3 Tiered routing pays browser cost only on browser pages

> "Pays browser cost only on pages that need a browser."

Most scraping tools, including Firecrawl, default to rendering pages in a real browser. This is correct for hostile or JS-heavy targets but wildly wasteful for the static-HTML majority of the web. A SSR'd marketing page does not need Chromium to be parsed.

Trawl auto-routes each URL to the cheapest engine that returns valid content: HTTP first, Chromium only when HTTP returns insufficient content. On a typical mixed crawl, ~70-85% of pages are handled at the HTTP tier in ~200ms each, with zero proxy bandwidth consumed. The remaining ~15-30% escalate to Chromium.

This is a real, measurable win on the static-HTML web. It's also a cost win on metered residential proxies — tier 1 hits don't consume proxy bandwidth at all.

### 4.4 Your data, your machine, your pipeline

> "No vendor in the data path."

Trawl's output flows directly into your local pipeline. Pipe it to `jq`, `dlt`, `DuckDB`, `polars`, `pandas`, your own scripts — all in the same shell session, all on the same machine, all without network round-trips. No webhook callbacks. No async polling. No "wait for the job to finish on someone else's server."

Three corollaries:
- **Privacy.** Sensitive data never crosses a network boundary you don't control.
- **Composition.** Local tools compose. Hosted APIs are harder to compose because they sit behind a network boundary.
- **No vendor lock-in.** Trawl is a binary you control. There is no service that can change pricing, deprecate features, get acquired, or shut down.

---

## 5. What NOT to say

The framing trap to avoid above all else:

### ❌ "Open-source alternative to Firecrawl"

This framing puts trawl in Firecrawl's gravity well. The reader's mental model becomes: *"Firecrawl is the real product; trawl is the budget version."* From there, any feature comparison is trawl-loses-by-default because Firecrawl has more funding, more engineers, more years of proprietary cloud infra, and more brand awareness.

### ❌ "Self-hosted Firecrawl"

Same trap. Worse phrasing.

### ❌ "Faster than Playwright"

Misleading framing. Trawl uses Chromium under the hood for JS-heavy pages — it isn't faster than Playwright at the thing Playwright is good at. Trawl is *structurally* faster on the static-HTML majority because it routes to HTTP first. The right framing is "tiered routing avoids browser cost when a browser isn't needed," not "faster than Playwright."

### ❌ "AI-powered scraping"

Marketing slop. Trawl has no LLM in the request path. Don't promise capabilities trawl doesn't have. (The sibling project `distill` will eventually be the LLM-aware extraction layer, and *that* doc can use AI framing — not this one.)

### ❌ "Replace your scraping pipeline with trawl"

Don't punch up. Trawl is a focused tool, not a platform. People who already have a working scraping pipeline should keep it. Trawl is for people building new things or for whom the existing options don't fit.

### ❌ "The fastest scraper"

Speed is a *consequence* of tiered routing, not a positioning claim. "Fastest" invites benchmark wars trawl will lose against custom-tuned single-purpose scrapers. The right framing is "tiered routing means you don't pay browser cost on pages that don't need a browser" — that's a structural claim, not a benchmark.

### ❌ "Built with Go" / "Written in Rust" / "Pure TypeScript"

Implementation language is not positioning. Nobody chooses a tool because of the language it's written in (a few people do, but they're not your audience). Mention Go in the README as a fact, not as a feature.

---

## 6. How to talk about specific competitors

When users compare trawl to other tools (they will — this question comes up constantly), use these honest framings.

### Firecrawl

> **Firecrawl is a hosted SaaS. Trawl is a local binary.** They solve overlapping problems for different audiences. If you want managed infrastructure, polished SDKs, and don't mind paying per page, Firecrawl is excellent. If you want local control, zero per-page fees, and an interface designed for AI agents to call directly, trawl is the right choice. Both are valid.

Notice what this framing does NOT do:
- Doesn't claim trawl is "better"
- Doesn't position trawl as a budget Firecrawl
- Doesn't compete on Firecrawl's strongest dimensions (anti-bot, web coverage, ergonomics)
- Doesn't pretend Firecrawl is bad

### Scrapy

> **Scrapy is a Python framework for building scrapers. Trawl is a CLI you shell out to.** Scrapy gives you a programming model (spiders, pipelines, middlewares) for writing scraping code in Python. Trawl gives you a binary that takes URLs in and produces JSON out. If you want to write Python and own a custom pipeline, Scrapy is excellent. If you want a tool you (or an AI agent) can shell out to without writing any code, trawl is what you want.

### Playwright

> **Playwright is a browser automation library. Trawl is a tiered scraper that uses Playwright for the browser tier.** They're not competitors — Playwright is a building block. Trawl handles the routing decision (does this page need a browser or not?), the persistence (resumable frontier), the politeness (rate limits, robots.txt), and the output formatting. For any individual page that needs a browser, trawl uses Playwright (or chromedp) under the hood.

### ScrapingBee / ScraperAPI / Bright Data SERP API

> **These are paid scraping APIs. Trawl is a free local binary.** Same general framing as Firecrawl. They handle proxies and anti-bot in the cloud and bill per request; trawl handles them locally and is free. The cost comparison shifts dramatically based on volume: at low volume, the paid APIs are simpler; at high volume, trawl is dramatically cheaper.

### Apify

> **Apify is a marketplace of pre-built scrapers running in the cloud. Trawl is a single tool you run yourself.** Apify's value prop is "someone has already built a scraper for the site you care about." Trawl's value prop is "you can build your own scrape job in five minutes and run it locally." Different shapes.

### Crawl4AI

> **Crawl4AI is a Python library for LLM-friendly scraping. Trawl is a Go binary.** Crawl4AI is a closer match to trawl on positioning (LLM-first, local-first) but is a Python library you import, not a CLI you shell out to. If you want to embed scraping inside Python code, Crawl4AI is the natural choice. If you want a binary your AI agent can shell out to without an SDK, trawl is what you want.

---

## 7. Volume break-even (be ready for cost questions)

People comparing trawl to hosted SaaS scrapers will ask "but isn't Firecrawl just $19/mo?" Have a clean answer:

| Pages/month | Firecrawl Hobby | Trawl on Hetzner |
|---|---|---|
| 500 | Free tier | $5/mo VPS (waste) |
| 3,000 | $19/mo | $5/mo VPS |
| 10,000 | ~$60/mo | $5/mo VPS + $10/mo proxy |
| 100,000 | ~$600/mo | $5-15/mo VPS + ~$30/mo proxy |
| 1,000,000 | ~$6,000/mo | $20-50/mo VPS + ~$100/mo proxy |
| 10,000,000 | ~$60,000/mo | $100-200/mo (multi-box) + bandwidth |

Break-even is around 5-10K pages/month. Below that, the hosted SaaS is simpler. Above that, the cost gap compounds dramatically. **Don't fight the comparison at low volume — concede it.** Say: "If you're scraping less than 5K pages a month, just use Firecrawl. Trawl pays for itself when you're at scale."

---

## 8. The honest meta-position

> **Firecrawl is the SaaS. Trawl is the binary.**
>
> **Firecrawl is for product builders.** They want to ship a feature without running scraping infra and they're happy paying per page.
>
> **Trawl is for agents and operators.** They want local control, no per-page fees, full composability with local tools, and no third party in the data path.
>
> Both positions are valid. Choose based on which axis matters more to you.

This is the elevator pitch when someone asks "why would I use trawl instead of Firecrawl?" Don't make it about features. Make it about deployment model and audience. The audiences barely overlap; the wrong customer is worse than no customer.

---

## 9. What the README should look like

When the agent writes or updates `README.md`, it should:

1. **Lead with the one-liner from §1.** Local-first web scraping for AI agents. Three lines max.
2. **One paragraph explaining the tiered router**, because that's the most concrete differentiator and the easiest to grok.
3. **A 3-4 line "who this is for"** that mentions agents, operators, privacy-sensitive use cases.
4. **A 2-line "who this is NOT for"** that politely sends inappropriate users to Firecrawl/Scrapy/etc. *Doing this builds trust* — people who see honest "use the other thing if X" are more likely to trust the rest of your claims.
5. **NO comparison tables** with hosted SaaS scrapers. Tables invite feature-vs-feature wars trawl will lose on individual axes. Save comparisons for blog posts, where they can be contextualized.
6. **NO benchmark claims** without a reproducible script in the repo. "10x faster than X" without a benchmark is marketing slop and undermines trust.
7. **A clear quick-start** with three concrete examples: single-page scrape, URL-list batch, follow-link crawl. Show the JSON output.
8. **Honest installation requirements**, including the SCIP indexer story (download on first use), proxy story (BYO), and any limitations.

---

## 10. What if Firecrawl ships local-first?

They might. They have an OSS repo and could plausibly ship a local-runner CLI. If they do, the positioning shifts but doesn't collapse:

- **Trawl is still designed for agents specifically**, not for human developers. CLI-first, JSON-first, single binary, no SDK. Firecrawl's local runner would inherit the SDK-and-API surface.
- **Trawl is still tiered-routed.** Firecrawl renders everything in a browser by design (smart-wait + browser actions are core to their pitch). A local Firecrawl would still be browser-first.
- **Trawl is still simpler to embed.** A Go binary with no runtime requirements is easier to ship as a dependency than a Python or Node library.

The structural moats — designed for agents, tiered routing, single static binary — are not features Firecrawl can copy without changing what they fundamentally are. That's the long-term defense.

---

## TL;DR

- **Pitch**: "Local-first web scraping for AI agents."
- **Audience**: AI agents in autonomous loops + privacy-sensitive operators + high-volume scrapers.
- **Anti-pitch**: Don't say "open-source Firecrawl." Don't compete on Firecrawl's strongest dimensions.
- **Moats**: local-first deployment, agent-first interface, tiered routing, no vendor lock-in.
- **Honest concessions**: Firecrawl wins on day-1 ergonomics, anti-bot at scale, schema-driven LLM extraction (until distill ships), interactive actions, polished SDKs, web coverage at scale.
- **Send inappropriate users away politely** — it builds trust and protects the audience that actually fits.
