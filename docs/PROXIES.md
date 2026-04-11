# Proxies — Reference for the P2 build agent

This doc covers everything trawl needs to know about HTTP/HTTPS proxies: the types, the providers, the rotation strategies, the gotchas, and the concrete shape of the trawl proxy config. Read this before implementing the `proxy:` job-config block.

> Scope: this doc covers proxy *routing*. It deliberately does NOT cover TLS fingerprint mimicry (uTLS, curl-impersonate, JA3/JA4 spoofing) — see [`docs/EVASION.md`](EVASION.md) for the tiered opt-in model that handles those. Tier 4 of EVASION.md explicitly defers proxy rotation back here, so the two docs are meant to be read together: PROXIES.md covers "how to route through different IPs," EVASION.md covers "how to look like a real browser once the request goes out." See §7 of this doc for the in-line pointer at the TLS/fingerprint handoff.

---

## 1. Mental model

A proxy is an intermediary that forwards your HTTP/HTTPS requests to the target server. The target sees the **proxy's IP**, not yours. The benefits:

- **Bypass IP rate limits.** A site that allows 100 req/hour per IP allows 100 req/hour *per proxy IP*. With 1000 proxy IPs, that's 100K req/hour.
- **Bypass IP-based bans.** When one IP gets blocked, rotate to the next.
- **Bypass geo-restrictions.** A proxy in Germany makes you appear to be in Germany.
- **Distribute reputation.** Your scraper's identity is spread across many IPs, so no single IP accumulates a "bot score."

Proxies are NOT a silver bullet against modern anti-bot systems. Cloudflare/PerimeterX/Akamai inspect TLS fingerprints, HTTP/2 frame ordering, browser behavior, and mouse/scroll telemetry — none of which proxies affect. **For hostile targets, the right answer is often to escalate to Chromium (which produces a real browser fingerprint) and use a residential proxy together.** Proxies alone are necessary but not sufficient.

---

## 2. Proxy types

There are four meaningfully different types. The type matters more than the rotation strategy.

| Type | Cost | Speed | Detectability | When to use |
|---|---|---|---|---|
| **Datacenter** | $0.50–2/GB or $1–5/IP/mo | Very fast (~10–50ms added) | Easy to detect — IPs come from AWS/GCP/Hetzner blocks listed in commercial IP-reputation databases | Friendly targets, high volume, cost-sensitive jobs. Default for trawl. |
| **ISP / Static residential** | $5–10/GB | Fast (~50–150ms added) | Medium — looks like a residential ISP IP but stable and reusable | The sweet spot for serious work — datacenter speed + residential reputation |
| **Residential (rotating)** | $5–15/GB | Slow (~500–3000ms added) | Hard to detect — real consumer IPs sourced via SDK-installed apps or P2P networks | Sites that explicitly block datacenter IPs (LinkedIn, Google, Instacart, ticketing, sneakers) |
| **Mobile (4G/5G)** | $15–30/GB | Variable (~200–2000ms added) | Nearly impossible — IPs come from carrier CGNAT pools shared with millions of real users | The hardest targets (Instagram, TikTok, large e-commerce). Last resort. |

### Heuristic for picking a type

- **You don't know what the target uses for anti-bot:** start with datacenter, escalate to residential only on observed blocks.
- **Target is a SaaS marketing/pricing page:** datacenter is fine (this is the trawl benchmark case).
- **Target is a consumer-facing big-tech product:** residential, possibly mobile.
- **Target is your own staging environment:** no proxy at all.

You almost never want pure mobile for a friendly site (you'll burn money), and you almost never want pure datacenter for a hostile site (you'll get banned).

---

## 3. Provider landscape (2026)

| Tier | Providers | Notes |
|---|---|---|
| **Enterprise (expensive, reliable)** | Bright Data, Oxylabs | The "default" choices for serious commercial scraping. Expect $500+/mo minimums. Best IP quality, best support, strict ToS. |
| **Mid-tier (good value)** | Smartproxy / Decodo, Soax, IPRoyal, NetNut | Cheaper than Bright/Oxy, similar quality for most use cases. Good starting point for trawl users. |
| **Budget datacenter** | Webshare, ProxyMesh, ProxyEmpire | $5–30/mo for unlimited datacenter bandwidth on rotating IPs. Perfect for the SaaS pricing benchmark. |
| **Avoid** | Anything advertised on scraping forums for $1/mo | Stolen IPs, malware-installed botnets, or honeypots. You'll get burned. |

Trawl should not bake in any provider — the user supplies an upstream URL and trawl just uses it. But the docs and skill prompts can recommend Webshare for datacenter and Smartproxy for residential as sensible defaults.

---

## 4. Rotation strategies

Rotation is **not a separate piece of infrastructure** — it's a per-request decision made by encoding a session ID into the proxy username. All major providers follow this pattern:

```
http://username-session-abc123:password@gate.smartproxy.com:7000
                  ^^^^^^^^^^^^^^^^^^
                  session ID — change it to rotate the exit IP
```

When trawl sends a request through this URL, the provider's gateway sees the `session-abc123` token and routes to the same exit IP it used last time for that session. Use a new session ID and you get a new IP. Different providers use slightly different parameter names (`session`, `sid`, `sticky`, `country`), but the pattern is the same.

### The three strategies

| Strategy | How it works | When to use |
|---|---|---|
| **Per-request** | New random session ID on every request → new IP every request | One-shot URL fetches, maximum IP diversity, no session state |
| **Sticky session** | Same session ID for N minutes → same IP for that window | Multi-page crawls of one site, login flows, anything that needs cookie continuity |
| **Per-domain sticky** | One session ID per target domain, persisted across the run | Trawl's recommended default — preserves cookies and reputation per domain, naturally spreads load across the IP pool |

### Why per-domain sticky is the right default for trawl

1. **Cookies stay valid.** A site that issues a session cookie expects to see the same IP on subsequent requests. Per-request rotation breaks that.
2. **Reputation accumulates per domain, not globally.** If example.com bans your IP, you've burned ONE session, not your entire pool.
3. **Naturally distributes load.** With 7000 unique domains in the seed list, you'll consume up to 7000 sessions — exactly the IP diversity you want, with no extra logic.
4. **It's resumable.** Persist `domain → session_id` to BadgerDB and a resumed run picks up where it left off without re-establishing sessions.

---

## 5. The trawl proxy config

This is the spec for the build agent. The job YAML gets a `proxy:` block:

```yaml
proxy:
  upstream: http://user-{{session}}:pass@gate.smartproxy.com:7000
  rotation: per_domain      # per_request | per_domain | sticky:5m
  fallback: direct          # direct | fail | retry
  countries: [us, gb]       # optional, if upstream supports country selection
  rotate_on_status: [403, 429, 503]
  max_session_age: 30m      # rotate even sticky sessions after this
```

### Field semantics

- **`upstream`** — the proxy URL with `{{session}}` placeholder that trawl substitutes per rotation policy. `{{country}}` and `{{sid}}` are also valid placeholders (provider-specific).
- **`rotation`** — `per_request` (new session every request), `per_domain` (one session per target domain), `sticky:5m` (one session per N minutes regardless of domain).
- **`fallback`** — what to do when the proxy itself fails (network error, auth failure, gateway error):
  - `direct` — try the request without a proxy (only for non-hostile targets — could leak your real IP)
  - `fail` — drop the URL into the dead-letter queue
  - `retry` — retry through a fresh session, up to N times
- **`countries`** — geo-restrict exit IPs. Provider must support this (most do via username flags).
- **`rotate_on_status`** — HTTP status codes that trigger a forced session rotation. 403/429/503 are the standard "you're rate limited" signals.
- **`max_session_age`** — hard cap on how long a sticky/per-domain session lives. Forces freshness even for friendly sites.

### CLI override

For ad-hoc runs, allow `--proxy <url>` on the CLI to set the upstream without a YAML file:

```bash
trawl batch urls.txt --proxy http://user:pass@gate.smartproxy.com:7000
```

Defaults to `rotation: per_domain`, `fallback: direct` for CLI mode.

### Per-tier proxy override

The HTTP, Lightpanda, and Chromium tiers should be able to use *different* proxy configurations. The HTTP tier can hammer a cheap datacenter pool, while Chromium uses a residential pool for the few hostile targets that escalate that far. Concrete config shape:

```yaml
proxy:
  http:
    upstream: http://user-{{session}}:pass@datacenter.proxy.com:7000
    rotation: per_domain
  lightpanda:
    upstream: http://user-{{session}}:pass@datacenter.proxy.com:7000
    rotation: per_domain
  chromium:
    upstream: http://user-{{session}}:pass@residential.proxy.com:7000
    rotation: per_domain
    countries: [us]
```

Or, if all tiers should use the same config, use the flat `proxy:` form from above. The build agent picks whichever shape is cleaner.

---

## 6. Go implementation notes

### HTTP tier (`net/http`)

Per-request proxy is set via `http.Transport.Proxy`:

```go
transport := &http.Transport{
    Proxy: func(req *http.Request) (*url.URL, error) {
        return proxyManager.URLForRequest(req), nil
    },
    // tune timeouts, MaxIdleConnsPerHost, etc.
}
client := &http.Client{Transport: transport}
```

The `Proxy` function is called per-request, so it can return different URLs based on the target host (per-domain sticky) or a session counter (per-request rotation).

For HTTPS targets, Go's transport automatically uses CONNECT to the proxy — no extra work needed.

**Auth gotcha:** when the proxy URL contains `user:pass@`, Go automatically sends `Proxy-Authorization: Basic <base64>`. This works for most providers but breaks if the password contains `@` or `:` — URL-encode them.

### Lightpanda tier

Lightpanda is driven over CDP, but the proxy must be set as a launch flag, not a CDP command. When spawning Lightpanda:

```go
cmd := exec.Command("lightpanda", 
    "--cdp-port", "9222",
    "--proxy-server", "http://user:pass@gate.smartproxy.com:7000",
)
```

The proxy is pinned for the lifetime of the Lightpanda process. To rotate, you need to either:
1. **Recycle the Lightpanda instance** with a new `--proxy-server` flag (clean but expensive)
2. **Maintain separate Lightpanda pools per session** (one process per session ID — only feasible for small pools)

Recommendation: recycle. The Lightpanda pool already recycles every M pages for memory reasons — extend that recycling logic to also recycle when the proxy session needs rotation. Per-domain sticky works naturally because trawl can route same-domain requests to the same Lightpanda instance.

### Chromium tier (`chromedp`)

Same pattern as Lightpanda — proxy is a browser launch flag:

```go
opts := append(chromedp.DefaultExecAllocatorOptions[:],
    chromedp.ProxyServer("http://user:pass@gate.smartproxy.com:7000"),
)
allocCtx, _ := chromedp.NewExecAllocator(ctx, opts...)
browser, _ := chromedp.NewContext(allocCtx)
```

Chromium doesn't natively support per-request proxy auth — if your provider requires basic auth, you have to either:
1. Bake auth into the URL (`http://user:pass@host:port`) — works for HTTP proxies, not always for HTTPS
2. Use Chromium's `--proxy-bypass-list` and intercept auth via `Network.authRequired` CDP event (more complex but more robust)

Recommendation: start with URL-embedded auth, escalate to CDP auth interception if it breaks on real providers.

### The proxy manager

A central `internal/proxy/` package owns:

```go
type Manager interface {
    // URLForRequest returns the proxy URL for an outbound HTTP request,
    // applying the configured rotation strategy.
    URLForRequest(req *http.Request) *url.URL
    
    // SessionForDomain returns (and creates if needed) the session ID
    // for a given target domain. Used by Lightpanda/Chromium pools to
    // decide whether to recycle.
    SessionForDomain(domain string) string
    
    // Rotate forces a new session for the given domain. Called by the
    // router when a 403/429/503 is observed.
    Rotate(domain string)
    
    // Persist saves the domain→session map to BadgerDB for resumability.
    Persist() error
}
```

The manager is the single source of truth for "which IP should this request go out from." Tiers ask it; they don't make their own decisions.

---

## 7. When proxies aren't enough

Some targets defeat naive proxy rotation. The signs:

- 403 / 429 from every IP, even fresh ones
- Cloudflare interstitial pages ("Checking your browser...")
- HTTP/2 with empty or instant-error responses
- Successful requests that return data slightly different from a real browser (price prices missing, content `[loading...]`, etc.)

When you see these, the problem isn't your IP — it's your **TLS fingerprint** or **HTTP/2 frame ordering**. Modern anti-bot systems (Cloudflare bot manager, PerimeterX, Akamai) inspect:

- **JA3 / JA4 fingerprint** — a hash of your TLS ClientHello (cipher suite order, extensions, curves). Go's `crypto/tls` has a recognizable fingerprint that's easy to flag.
- **HTTP/2 SETTINGS frame order** — Go sends frames in a different order than Chrome.
- **Header order** — Go's `net/http` alphabetizes headers; real browsers don't.

The fixes are out of v1 scope, but for the build agent's awareness:

- **`refraction-networking/utls`** — drop-in replacement for `crypto/tls` that mimics Chrome/Firefox/Safari fingerprints. The most common fix.
- **`bogdanfinn/tls-client`** — higher-level wrapper around utls with HTTP/2 handling and JA3 string presets. Easier to integrate.
- **`lwthiker/curl-impersonate`** — patched curl that produces real browser fingerprints. Useful for tier 1 if you're willing to shell out to a binary.
- **Just escalate to Chromium.** A real Chromium browser produces a real Chromium fingerprint by definition. Slow but unbeatable.

Trawl's v1 strategy: **don't ship fingerprint mimicry**. When tier 1 fails on suspected anti-bot, escalate to tier 3 (Chromium) and accept the cost.

**Update (2026-04-11):** the "add fingerprint support in a future version" promise above is now concrete. See [`docs/EVASION.md`](EVASION.md) for the principled tiered model — Tier 1 (honest browser mimicry, behind `--browser-like`), Tier 2 (chromium stealth patches, behind `--stealth`), and Tier 3 (TLS fingerprint forgery via utls, behind `--tls-match chrome`). Each tier has a falsifiable decision rule gating when trawl will actually ship it. The proxy layer in this doc composes with those tiers — proxies for IP diversity, evasion tiers for per-request fingerprint. EVASION.md §5.4 explicitly defers proxy rotation back to PROXIES.md so the two documents have a clean handoff at Tier 4.

---

## 8. Operational gotchas

### Bandwidth costs

Residential proxies are billed per GB and prices are real. A 100MB image-heavy page costs 100MB off your quota. Mitigations:

- Always send `Accept-Encoding: gzip, br`
- Use `Range: bytes=0-200000` to cap response size when you only need the first chunk
- Block image/font/css requests in Lightpanda/Chromium via CDP (`Network.setBlockedURLs`)
- Prefer tier 1 (HTTP, no asset loading) whenever possible — this is one of the strongest arguments for the tiered router

### Pool size vs concurrency

Cheap proxy plans cap connections per IP (often 5–25). If your global concurrency is 200 and your plan allows 5 concurrent per IP, you need at least 40 IPs in active rotation. Match pool size to `target_concurrency × 5` minimum, ideally `× 10`.

For datacenter providers like Webshare you typically get a fixed pool (e.g., 100 IPs) and rotate through them — easy to reason about. For residential providers the pool is "millions of IPs" but billed per GB — concurrency is the only knob.

### Session burn rate

Per-domain sticky on a 7000-domain crawl will consume 7000 sessions. Most providers don't charge per session (only per GB), but a few do — check before you run. If session count is metered, fall back to `sticky:N` (one session per N requests, recycled) instead of per-domain.

### Health checks

Before a long run, validate the proxy:

```bash
trawl proxy-test --upstream http://user:pass@gate.smartproxy.com:7000
```

Should:
1. Connect to the proxy and authenticate
2. Make a test request to a known echo service (httpbin.org/ip is the standard)
3. Verify the returned IP differs from your direct IP
4. Make 5 requests with different sessions and verify rotation works
5. Print the observed exit IPs

This subcommand should ship in P2 alongside the proxy support — it saves an enormous amount of "is the proxy broken or is my code broken" debugging.

### Per-domain failure tracking

When a domain consistently fails through the proxy but works direct (or vice versa), the proxy manager should learn that. Persist a per-domain "proxy works / direct works / both fail" hint in BadgerDB and route accordingly. This is a P3-ish enhancement but worth designing the data model for in P2.

### Don't leak your real IP

If `fallback: direct` is enabled, it's possible for a hostile site to see your real IP when the proxy fails. For high-stakes runs, use `fallback: fail` (drop the URL) instead. Document this clearly in the config docs — users will assume "fallback to direct" is safe and it isn't always.

---

## 9. Recommendations by scale

| Scale | Recommended setup |
|---|---|
| **Dev / smoke test** (<100 URLs) | No proxy at all. Run direct from a clean IP. |
| **Trawl benchmark** (~7K URLs, friendly SaaS targets) | Webshare datacenter ($10/mo, 100 rotating IPs) with `per_domain` rotation. Or no proxy at all if running from a fresh datacenter VPS. |
| **Small commercial** (10K–100K URLs, mixed targets) | Smartproxy or IPRoyal mid-tier residential, `per_domain` rotation, ~$50–100/mo bandwidth budget |
| **Medium commercial** (100K–1M URLs, hostile targets) | Bright Data or Oxylabs residential, per-domain rotation with `rotate_on_status: [403, 429]`, dedicated VPS, monitoring on bandwidth burn |
| **Large commercial** (1M+ URLs) | Multi-provider setup (datacenter for friendly, residential for hostile), distributed across multiple VPS regions, custom pool management, possibly TLS fingerprinting via utls. At this scale you're past trawl's v1 design and into custom infrastructure. |

---

## 10. What ships in P2

Concrete scope for the build agent. The P2 proxy feature is "done" when:

- [ ] `proxy:` block in job YAML (per §5)
- [ ] `--proxy <url>` CLI flag for ad-hoc runs
- [ ] `internal/proxy/` package with `Manager` interface (per §6)
- [ ] HTTP tier respects proxy via `http.Transport.Proxy`
- [ ] Lightpanda tier respects proxy via `--proxy-server` launch flag, recycles on session change
- [ ] Chromium tier respects proxy via `chromedp.ProxyServer`
- [ ] Per-domain sticky rotation works and persists to BadgerDB
- [ ] Per-request and `sticky:N` rotation work
- [ ] `rotate_on_status` triggers session rotation on configured codes
- [ ] `trawl proxy-test` subcommand validates a proxy upstream
- [ ] Failure modes are logged: proxy auth failed, proxy unreachable, session exhausted
- [ ] Documentation: a `proxies.md` user guide that points at provider recommendations

What's explicitly OUT of P2:

- TLS fingerprint mimicry (uTLS, curl-impersonate)
- HTTP/2 frame reordering
- CAPTCHA solving
- Browser fingerprint spoofing beyond what Chromium does naturally
- Multi-provider failover within a single job
- Automatic provider selection based on target type

These are all reasonable v2/v3 features. None of them belong in v1.
