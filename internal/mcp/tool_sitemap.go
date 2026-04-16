package mcp

import (
	"context"
	"time"

	"github.com/jeffdhooton/trawl/internal/sitemap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sitemapArgs is the input schema for trawl_sitemap.
type sitemapArgs struct {
	URL string `json:"url" jsonschema:"the site URL to discover sitemaps for"`

	MaxURLs   int `json:"maxUrls,omitempty" jsonschema:"cap total URLs returned across all sitemaps, default 50000"`
	MaxDepth  int `json:"maxDepth,omitempty" jsonschema:"cap sitemap-index recursion depth, default 3"`
	TimeoutMS int `json:"timeoutMs,omitempty" jsonschema:"per-fetch timeout in milliseconds, default 30000"`
}

func registerSitemapTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "trawl_sitemap",
		Title:       "Pull URLs from a site's sitemap(s)",
		Description: sitemapToolDescription,
	}, sitemapHandler)
}

const sitemapToolDescription = `Locate a site's sitemap(s) via robots.txt Sitemap: directives or the ` +
	`well-known paths (/sitemap.xml, /sitemap_index.xml), parse them, and ` +
	`return the URL list. Sitemap-index files are recursively expanded. ` +
	`Gzipped sitemaps are decompressed transparently. URLs are deduped in ` +
	`first-seen order.

Use this when:
  - You want every URL the site has officially declared.
  - You need the discovery to be fast (sitemap-only, no HTML parsing).
  - The site is known to publish a complete sitemap (most CMS-based
    sites do).

Do NOT use for:
  - Sites without sitemaps — use trawl_map (which combines sitemap +
    crawl) or trawl_crawl.
  - Discovering URLs that aren't in the sitemap (modal flows, internal
    nav, paginated lists not covered) — use trawl_map.`

func sitemapHandler(ctx context.Context, _ *mcp.CallToolRequest, args sitemapArgs) (*mcp.CallToolResult, any, error) {
	if args.URL == "" {
		return errorResult("url is required"), nil, nil
	}
	maxURLs := args.MaxURLs
	if maxURLs <= 0 {
		maxURLs = 50000
	}
	maxDepth := args.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	timeout := 30 * time.Second
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}

	urls, _, err := sitemap.Discover(ctx, args.URL, sitemap.Options{
		MaxURLs:  maxURLs,
		MaxDepth: maxDepth,
		Timeout:  timeout,
	})
	if err != nil {
		return errorResult("sitemap discover: %s", err.Error()), nil, nil
	}
	return stringListResult(urls), nil, nil
}
