package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/jeffdhooton/trawl/internal/output"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordResult builds a CallToolResult from one or more output.Records.
// Each record is emitted as a JSON-encoded TextContent block — agents
// can parse them as JSONL or pluck individual fields with jq-style
// path expressions. Failed records are still returned (with their
// failure_category populated); they're not lifted to MCP-level errors
// because per-record failures are routine and the agent often wants
// to see them.
//
// StructuredContent is also populated with the records as a list, so
// agents that prefer structured output (and want to skip the JSON
// parsing step) can read it directly.
func recordResult(records []output.Record) (*mcp.CallToolResult, error) {
	contents := make([]mcp.Content, 0, len(records))
	for _, rec := range records {
		body, err := json.Marshal(rec)
		if err != nil {
			return nil, fmt.Errorf("marshal record: %w", err)
		}
		contents = append(contents, &mcp.TextContent{Text: string(body)})
	}
	return &mcp.CallToolResult{
		Content:           contents,
		StructuredContent: map[string]any{"records": records, "count": len(records)},
	}, nil
}

// errorResult lifts a setup-time error (bad URL, oversize batch, etc)
// into a CallToolResult with IsError=true. This is the right shape
// when the agent needs to see the error and self-correct — e.g. a
// "URL list too large, drop to ≤50" message that the LLM can act on.
//
// Per-fetch failures should NOT use this path — they live in the
// record's failure_category field and flow through recordResult.
func errorResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(format, args...)},
		},
		IsError: true,
	}
}

// stringListResult builds a result for tools that return a flat list
// of strings (map, sitemap). Each URL is its own TextContent line so
// the agent can iterate naturally; StructuredContent carries the
// full list for agents that want a single payload.
func stringListResult(items []string) *mcp.CallToolResult {
	contents := make([]mcp.Content, 0, len(items))
	for _, s := range items {
		contents = append(contents, &mcp.TextContent{Text: s})
	}
	return &mcp.CallToolResult{
		Content:           contents,
		StructuredContent: map[string]any{"urls": items, "count": len(items)},
	}
}
