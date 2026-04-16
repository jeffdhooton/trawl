package main

import (
	"context"
	"os/signal"
	"syscall"

	tmcp "github.com/jeffdhooton/trawl/internal/mcp"
	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run trawl as an MCP (Model Context Protocol) server over stdio",
		Long: `Start an MCP server that exposes trawl's commands as tools to any
agent that speaks MCP (Claude Code, Cursor, Codex, etc.). Communication
is over stdio: the agent spawns 'trawl mcp' as a subprocess and sends
JSON-RPC over stdin/stdout.

Five tools are registered:
  - trawl_scrape   — fetch one URL, return a structured record
  - trawl_batch    — fetch a list of URLs (≤50 per call)
  - trawl_crawl    — BFS-crawl a site (limit ≤500 per call)
  - trawl_map      — enumerate URLs on a site (≤5000 per call)
  - trawl_sitemap  — pull URLs from a site's sitemap(s)

For larger jobs use the corresponding CLI subcommand directly — those
have a persistent frontier, resume on SIGINT, and per-job stats. The
MCP tool surface is for inline-in-the-loop use.

Example registration in Claude Code's .mcp.json:

  {
    "mcpServers": {
      "trawl": { "command": "trawl", "args": ["mcp"] }
    }
  }

See docs/MCP.md for the full agent registration recipes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCP(cmd.Context())
		},
	}
}

func runMCP(parentCtx context.Context) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return tmcp.Run(ctx)
}
