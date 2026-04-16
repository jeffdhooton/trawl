# trawl MCP server

`trawl mcp` runs the trawl binary as a [Model Context Protocol](https://modelcontextprotocol.io/) server over stdio. Agents (Claude Code, Cursor, Codex, etc.) spawn it as a subprocess and call its tools as a first-class part of their tool-use loop — no shelling-out to Bash, no parsing CLI output, no JSONL post-processing in the agent.

This is the move the [positioning doc](./POSITIONING.md) has been demanding: *"local-first web scraping for AI agents,"* with the agent calling the binary as a protocol, not a command.

---

## Why MCP

trawl is already designed around agent consumers (CLI-first, JSON output, deterministic exit codes, no SDKs). MCP closes the last gap: instead of an agent shelling out via Bash and parsing JSONL, the tools appear in the agent's tool list with structured args, structured results, and per-tool descriptions written for LLMs.

Three concrete wins:

1. **Args are typed.** No more "remember the flag spelling for `--screenshot-dir`" — the agent sees a JSON schema with descriptions.
2. **Results are structured.** Each output record is a `TextContent` block (parseable JSON) AND a `StructuredContent` field. Agents can pick whichever shape they prefer.
3. **The tool descriptions are when-to-use guides.** Each tool's description tells the agent *which* trawl tool to reach for and which to skip — `trawl_map` for URL discovery, `trawl_batch` for known lists, `trawl_crawl` for following links, etc.

---

## Five tools

| Tool | What it does | Hard cap |
|---|---|---|
| `trawl_scrape` | Fetch one URL, return one record | — |
| `trawl_batch` | Fetch a list of URLs concurrently | 50 URLs / call |
| `trawl_crawl` | BFS-crawl a site from a seed | `limit` ≤ 500 |
| `trawl_map` | Enumerate URLs from sitemap + crawl | `limit` ≤ 5000 |
| `trawl_sitemap` | Pull URLs from sitemap(s) only | — |

The caps exist so a single tool call can't burn unbounded resources. Above the cap, the error message points the agent at the corresponding CLI subcommand, which has the persistent frontier + resume guarantees you actually want for big jobs.

Every tool exposes the same routing knobs as the CLI: `tiers`, `forceTier`, `format`, `readability`, `selectors`, `schemaPath`, `browserLike`, `stealth`, `tlsMatch`, `ocr`, etc. The args structs in `internal/mcp/tool_*.go` are the source of truth for the exact shape.

---

## Quick start

### Claude Code

Add to `.mcp.json` at the project root (or `~/.claude.json` for global):

```json
{
  "mcpServers": {
    "trawl": {
      "command": "trawl",
      "args": ["mcp"]
    }
  }
}
```

Then in any Claude Code session: ask Claude to scrape, batch, crawl, map, or pull a sitemap. The trawl tools show up in the tool list.

### Cursor

`~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "trawl": {
      "command": "trawl",
      "args": ["mcp"]
    }
  }
}
```

### Codex CLI

`~/.codex/config.toml`:

```toml
[mcp_servers.trawl]
command = "trawl"
args = ["mcp"]
```

### Custom Go client

```go
import "github.com/modelcontextprotocol/go-sdk/mcp"

transport := &mcp.CommandTransport{Command: exec.Command("trawl", "mcp")}
client := mcp.NewClient(&mcp.Implementation{Name: "myagent", Version: "v1"}, nil)
session, _ := client.Connect(ctx, transport, nil)

res, _ := session.CallTool(ctx, &mcp.CallToolParams{
    Name: "trawl_scrape",
    Arguments: map[string]any{
        "url":    "https://example.com",
        "format": "markdown",
    },
})
```

The full server-side surface lives in [`internal/mcp/`](../internal/mcp/).

---

## Result shape

Tools that return records (`scrape`, `batch`, `crawl`) emit one [`output.Record`](../internal/output/output.go) per row, both as a JSON `TextContent` block (one per row) AND as the result's `StructuredContent` (`{records: [...], count: N}`).

Tools that return URL lists (`map`, `sitemap`) emit one URL per `TextContent` block AND as `StructuredContent` (`{urls: [...], count: N}`).

Per-fetch failures stay inside the record (`failure_category`, `error`) — they don't lift to MCP-level errors. Setup-time failures (bad URL, oversize batch, invalid `sources`) DO set `IsError: true` so the agent can self-correct.

---

## Design decisions

### stdio only for v1

HTTP/SSE transport is supported by the SDK but intentionally deferred. Stdio matches trawl's "single static binary, agent spawns subprocess" model exactly. Adding HTTP brings auth, CORS, deployment, and a daemon lifecycle to think about — none of which has a clear consumer ask yet. If a remote-trawl use case appears, revisit.

### Hard caps on batch/crawl/map

A tool call should be a coherent unit of work. 50 URLs in a batch fits in seconds; 500 URLs in a crawl fits in a few minutes; 5000 in a map fits comfortably. Beyond that, you want the persistent frontier + resume + per-job stats that `trawl batch|crawl|map` give you on the CLI. The MCP error message points at the CLI alternative explicitly so agents can route the user to the right tool.

### No `trawl_resume` MCP tool

Resume is a multi-call lifecycle (create job → interrupt → resume) and the MCP tool surface is per-call. Agents that need to resume a long-running job should shell to `trawl resume <id>` directly. Adding `resume` to MCP would imply we also need job-id tracking and async progress, which contradicts the "bounded per-call" design.

### One job dir per call, deleted on exit

`trawl_batch` and `trawl_crawl` create a temp `$TMPDIR/trawl-mcp-{batch,crawl}-*` directory per call, run the job there, read the JSONL output back into memory, and `os.RemoveAll` the dir before returning. No cross-call state, no leftover frontier DBs. The persistent tier-learning cache at `$TRAWL_HOME/tier-cache` is still consulted (and updated) so MCP calls and CLI calls share learning.

### SDK choice: official `modelcontextprotocol/go-sdk`

Picked over `mark3labs/mcp-go` for v1.x semver stability, the published spec-compatibility table, and production validators (Google Cloud, Docker, Datadog, Anthropic). See `docs/DECISIONS.md` (2026-04-16 entry) for the full evaluation.

---

## Testing

Unit tests use the SDK's in-memory transport pair (`mcp.NewInMemoryTransports`) so the server boots without spawning a subprocess. Live in [`internal/mcp/server_test.go`](../internal/mcp/server_test.go) — eight tools-list / scrape / batch / map / sitemap / crawl tests covering happy paths plus the four input-validation paths.

End-to-end stdio is covered by [`cmd/trawl/mcp_subprocess_test.go`](../cmd/trawl/mcp_subprocess_test.go), which builds the binary and connects via `mcp.CommandTransport` — the same code path real agents take. Catches bugs the in-memory tests can't see (stdio framing, log-output channel, cobra wiring).

Run them all:

```bash
go test ./internal/mcp/... ./cmd/trawl/...
```

---

## Operational notes

- **Log output goes to stderr.** trawl's existing zerolog setup writes to `os.Stderr`. stdout is reserved for JSON-RPC framing.
- **Hermetic per-job dirs.** Each batch/crawl call uses an `os.MkdirTemp` directory, removed via `defer`. The persistent tier-learning cache at `$TRAWL_HOME/tier-cache` is still shared across calls.
- **PDF, screenshots, schemas, actions** all work the same as CLI — the args mirror the flags. Schemas and action files are referenced by path; the file must exist on the machine running the MCP server.
- **`TRAWL_HOME`** is honored, same as CLI.
