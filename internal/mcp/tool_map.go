package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/jeffdhooton/trawl/internal/canonical"
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/jeffdhooton/trawl/internal/sitemap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mapArgs is the input schema for trawl_map.
type mapArgs struct {
	Seed string `json:"seed" jsonschema:"the seed URL to enumerate URLs from"`

	Sources    string `json:"sources,omitempty" jsonschema:"URL sources: 'sitemap', 'crawl', or 'both' (default 'both'). Sitemap URLs land first; crawl fills the gaps."`
	Depth      int    `json:"depth,omitempty" jsonschema:"BFS depth for the crawl source, default 2"`
	Limit      int    `json:"limit,omitempty" jsonschema:"hard cap on URLs returned, default 1000. Capped at 5000 by the MCP tool."`
	SameDomain bool   `json:"sameDomain,omitempty" jsonschema:"restrict crawl-discovered URLs to the seed's host (default true)"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"per-fetch timeout in milliseconds, default 30000"`
	SitemapMax int    `json:"sitemapMax,omitempty" jsonschema:"cap on URLs pulled from sitemaps, default 50000"`

	IgnoreRobots bool    `json:"ignoreRobots,omitempty" jsonschema:"bypass robots.txt for the crawl source"`
	Concurrency  int     `json:"concurrency,omitempty" jsonschema:"max concurrent fetches for the crawl source, default 8"`
	RatePerSec   float64 `json:"ratePerSec,omitempty" jsonschema:"requests per second per domain for the crawl source, default 2"`
}

const mapURLLimit = 5000

func registerMapTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trawl_map",
		Title:       "Enumerate URLs on a site",
		Description: mapToolDescription,
	}, mapHandler)
}

const mapToolDescription = `Discover every URL on a site by combining two sources:
  1. Sitemap — robots.txt Sitemap: directives plus /sitemap.xml and
     /sitemap_index.xml, recursively expanded.
  2. HTML crawl — BFS link discovery from the seed page (HTTP-only,
     no chromium, no extraction, no record writes).

Returns a flat URL list. Sitemap URLs land first (publisher-declared,
authoritative); crawl URLs fill the gaps. Deduped.

Use this when:
  - You need a list of URLs to feed into trawl_batch or trawl_scrape.
  - You want to know what's on a site before deciding whether to crawl
    its full content.
  - The agent flow is "discover, then act" — map first, then batch the
    interesting URLs.

Capped at 5000 URLs per call. For full-site enumeration use
'trawl map' on the CLI.

Do NOT use for:
  - SPA-heavy sites where nav links only render after JavaScript runs —
    use trawl_crawl with chromium tier instead (slower but thorough).
  - When you only want sitemap URLs — use trawl_sitemap (faster).`

func mapHandler(ctx context.Context, _ *mcp.CallToolRequest, args mapArgs) (*mcp.CallToolResult, any, error) {
	if args.Seed == "" {
		return errorResult("seed is required"), nil, nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 1000
	}
	if limit > mapURLLimit {
		return errorResult(
			"limit too large (%d > %d) — for larger maps run `trawl map` on the CLI",
			limit, mapURLLimit,
		), nil, nil
	}
	sources := args.Sources
	if sources == "" {
		sources = "both"
	}
	switch sources {
	case "sitemap", "crawl", "both":
	default:
		return errorResult(`invalid sources %q (want "sitemap", "crawl", or "both")`, sources), nil, nil
	}
	depth := args.Depth
	if depth <= 0 {
		depth = 2
	}
	timeout := 30 * time.Second
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	sitemapMax := args.SitemapMax
	if sitemapMax <= 0 {
		sitemapMax = 50000
	}

	canonSeed, err := canonical.Canonicalize(args.Seed, canonical.Options{})
	if err != nil {
		return errorResult("canonicalize seed: %s", err.Error()), nil, nil
	}

	// Shared dedup + emit cap. Mirrors cmd/trawl/map.go's emit
	// closure but stores into a slice instead of writing to stdout.
	var (
		mu       sync.Mutex
		seen     = make(map[string]struct{})
		emitted  []string
	)
	emit := func(u string) bool {
		mu.Lock()
		defer mu.Unlock()
		if _, dup := seen[u]; dup {
			return true
		}
		if len(emitted) >= limit {
			return false
		}
		seen[u] = struct{}{}
		emitted = append(emitted, u)
		return true
	}

	// 1. Sitemap source.
	if sources == "sitemap" || sources == "both" {
		urls, _, serr := sitemap.Discover(ctx, canonSeed, sitemap.Options{
			MaxURLs:  sitemapMax,
			MaxDepth: 3,
			Timeout:  timeout,
		})
		if serr != nil && sources == "sitemap" {
			return errorResult("sitemap: %s", serr.Error()), nil, nil
		}
		for _, u := range urls {
			canon, cerr := canonical.Canonicalize(u, canonical.Options{})
			if cerr != nil {
				continue
			}
			if !emit(canon) {
				break
			}
		}
	}

	// 2. Crawl source.
	if (sources == "crawl" || sources == "both") && len(emitted) < limit {
		if emit(canonSeed) {
			cerr := job.RunMap(ctx, canonSeed, job.MapOpts{
				Depth:        depth,
				SameDomain:   args.SameDomain,
				Timeout:      timeout,
				IgnoreRobots: args.IgnoreRobots,
				Concurrency:  args.Concurrency,
				RatePerSec:   args.RatePerSec,
			}, emit)
			if cerr != nil && sources == "crawl" {
				return errorResult("crawl: %s", cerr.Error()), nil, nil
			}
		}
	}

	return stringListResult(emitted), nil, nil
}
