// Package mcp exposes trawl's commands as Model Context Protocol
// tools over stdio. It is a thin adapter on top of internal/job:
// each tool's Args struct maps to a job.ScrapeOpts / job.MapOpts /
// job.Config, and the handler invokes the corresponding job entry
// point and serializes the result(s) as MCP content.
//
// Transport is stdio only — agents spawn `trawl mcp` as a
// subprocess. HTTP/SSE is intentionally deferred until a
// remote-trawl use case appears.
//
// Hard caps are enforced on batch (BatchURLLimit) and crawl
// (CrawlURLLimit) so a single tool call can't burn unbounded
// resources. A single agent call should be a coherent unit of work,
// not a multi-hour crawl — that's what `trawl crawl` on the CLI is
// for.
package mcp

import (
	"context"

	"github.com/jeffdhooton/trawl/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// BatchURLLimit caps the number of URLs accepted by trawl_batch
	// in a single tool call. Beyond this, the agent should switch to
	// running `trawl batch` directly via Bash for the persistent
	// frontier + resume guarantees.
	BatchURLLimit = 50

	// CrawlURLLimit caps the limit argument to trawl_crawl. Same
	// rationale as BatchURLLimit — agent tool calls should be
	// bounded; long crawls belong on the CLI.
	CrawlURLLimit = 500
)

// Run starts the MCP server over stdio and blocks until ctx is
// cancelled or stdin closes. Used by the `trawl mcp` cobra command.
//
// All five tools (scrape, batch, crawl, map, sitemap) are
// registered. Tool descriptions include when-to-use guidance so
// agents pick the right tool without trial and error.
func Run(ctx context.Context) error {
	server := NewServer()
	return server.Run(ctx, &mcp.StdioTransport{})
}

// NewServer constructs a configured MCP server with all trawl tools
// registered. Exported for testing — production callers should use
// Run, which both constructs and starts the server.
func NewServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "trawl",
		Version: version.Version,
		Title:   "trawl — local-first web scraping for AI agents",
	}, nil)

	registerScrapeTool(server)
	registerBatchTool(server)
	registerCrawlTool(server)
	registerMapTool(server)
	registerSitemapTool(server)

	return server
}
