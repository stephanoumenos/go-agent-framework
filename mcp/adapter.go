package mcp

import (
	"context"
	"heart"

	"github.com/mark3labs/mcp-go/mcp"
)

type MCPTool interface { // exported so users can add tools later
	Definition() mcp.Tool // schema + description
	toolHandler           // request executor (unexported in package)
}

// IntoTool turns any NodeDefinition into an MCP tool.
func IntoTool[In, Out any](
	def heart.NodeDefinition[In, Out],
	schema mcp.Tool, // fully‑built Tool metadata
	mapReq func(context.Context, mcp.CallToolRequest) (In, error),
	mapResp func(context.Context, Out) (*mcp.CallToolResult, error),
) MCPTool {
	return &tool[In, Out]{
		nodeDef: def,
		schema:  schema,
		mapReq:  mapReq,
		mapResp: mapResp,
	}
}

type toolHandler interface {
	handleRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

type tool[In, Out any] struct {
	nodeDef heart.NodeDefinition[In, Out]
	schema  mcp.Tool
	mapReq  func(context.Context, mcp.CallToolRequest) (In, error)
	mapResp func(context.Context, Out) (*mcp.CallToolResult, error)
}

func (t *tool[In, Out]) Definition() mcp.Tool { return t.schema }

func (t *tool[In, Out]) handleRequest(
	ctx context.Context,
	req mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	in, err := t.mapReq(ctx, req)
	if err != nil {
		return nil, err
	}

	// start the node lazily
	handle := t.nodeDef.Start(heart.Into(in))

	out, err := heart.Execute(ctx, handle) // heart‑native call
	if err != nil {
		return nil, err
	}

	return t.mapResp(ctx, out)
}
