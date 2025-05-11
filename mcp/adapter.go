// ./mcp/adapter.go
package mcp

import (
	"context"
	"fmt"

	gaf "go-agent-framework"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AddTools registers one or more MCPTool implementations with an MCPServer.
// It wraps each MCPTool's definition and handler into the format expected
// by the underlying github.com/mark3labs/mcp-go/server.
//
// Parameters:
//   - s: The MCPServer instance to add the tools to.
//   - tools: A variadic list of MCPTool implementations to register.
func AddTools(s *server.MCPServer, tools ...MCPTool) {
	serverTools := make([]server.ServerTool, 0, len(tools))
	for _, tool := range tools {
		serverTools = append(serverTools, server.ServerTool{
			Tool:    tool.Definition(),  // Get the mcp.Tool schema.
			Handler: tool.handleRequest, // Use the internal handler method.
		})
	}
	s.AddTools(serverTools...) // Register with the underlying server.
}

// MCPTool represents a tool conforming to the Multi-Capability Protocol (MCP),
// adapted from a gaf.NodeDefinition. It provides the tool's schema definition
// and encapsulates the logic for handling MCP requests by executing the underlying
// go-agent-framework node.
//
// Instances are typically created using the IntoTool function.
type MCPTool interface {
	// Definition returns the MCP schema (mcp.Tool) for this tool,
	// describing its name, description, input parameters, etc.
	Definition() mcp.Tool
	// toolHandler is an unexported interface satisfied by the internal tool struct,
	// allowing AddTools to access the handleRequest method.
	toolHandler
}

// IntoTool adapts a gaf.WorkflowDefinition into an MCPTool.
// It takes the workflow definition, its corresponding MCP schema, and mapping functions
// to bridge the gap between MCP request/response formats and the go-agent-framework workflow's
// input/output types.
//
// Parameters:
//   - def: The gaf.WorkflowDefinition representing the core logic of the tool.
//   - schema: The mcp.Tool struct fully describing the tool for the MCP server.
//   - mapReq: A function to convert an incoming mcp.CallToolRequest (specifically its arguments)
//     into the input type `In` required by the gaf.WorkflowDefinition.
//   - mapResp: A function to convert the output `Out` from the gaf.WorkflowDefinition
//     execution into the *mcp.CallToolResult format expected by the MCP server.
//
// Returns:
//   - An MCPTool implementation that can be registered with an MCP server using AddTools.
func IntoTool[In, Out any](
	def gaf.WorkflowDefinition[In, Out],
	schema mcp.Tool, // The full MCP metadata for the tool.
	mapReq func(context.Context, mcp.CallToolRequest) (In, error), // Maps MCP request args -> go-agent-framework node input.
	mapResp func(context.Context, Out) (*mcp.CallToolResult, error), // Maps go-agent-framework node output -> MCP result.
) MCPTool {
	return &tool[In, Out]{
		workflowDef: def,
		schema:      schema,
		mapReq:      mapReq,
		mapResp:     mapResp,
	}
}

// toolHandler is an unexported interface defining the request handling method.
// This allows AddTools to call the specific handler implementation without
// exposing it publicly or needing type assertions.
type toolHandler interface {
	handleRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

// tool is the internal implementation of MCPTool created by IntoTool.
// It holds the workflow definition, schema, and mapping functions.
type tool[In, Out any] struct {
	workflowDef gaf.WorkflowDefinition[In, Out]                         // Changed from nodeDef
	schema      mcp.Tool                                                // The MCP description.
	mapReq      func(context.Context, mcp.CallToolRequest) (In, error)  // Request mapper.
	mapResp     func(context.Context, Out) (*mcp.CallToolResult, error) // Response mapper.
}

// Definition implements the MCPTool interface, returning the stored schema.
func (t *tool[In, Out]) Definition() mcp.Tool { return t.schema }

// handleRequest implements the toolHandler interface. This method is called by
// the MCPServer when a request for this tool is received.
// It orchestrates the conversion of the MCP request, execution of the go-agent-framework workflow,
// and conversion of the go-agent-framework workflow's response back to the MCP format.
func (t *tool[In, Out]) handleRequest(
	ctx context.Context, // Context from the MCP server request.
	req mcp.CallToolRequest, // The incoming MCP tool call request.
) (*mcp.CallToolResult, error) {
	// 1. Map MCP request arguments to the go-agent-framework workflow's input type.
	in, err := t.mapReq(ctx, req)
	if err != nil {
		// Return mapping errors directly, as the workflow cannot execute.
		// Consider wrapping the error for more context if needed.
		return nil, fmt.Errorf("failed to map MCP request for tool '%s': %w", t.schema.Name, err)
	}

	// 2. Execute the go-agent-framework workflow and wait for the result.
	// ExecuteWorkflow handles the actual running of the workflow and its dependencies.
	// Use the request context for potential cancellation during execution.
	// Note: WorkflowOptions are not passed here, matching previous behavior of not passing them to gaf.Execute.
	out, err := gaf.ExecuteWorkflow(ctx, t.workflowDef, in)
	if err != nil {
		// Return errors from the go-agent-framework workflow execution.
		// Consider wrapping the error.
		return nil, fmt.Errorf("error executing gaf workflow for tool '%s': %w", t.schema.Name, err)
	}

	// 3. Map the go-agent-framework workflow's output to the MCP result format.
	mcpResult, err := t.mapResp(ctx, out)
	if err != nil {
		// Return errors encountered during response mapping.
		// Consider wrapping the error.
		return nil, fmt.Errorf("failed to map response for tool '%s': %w", t.schema.Name, err)
	}

	// 4. Return the successfully mapped MCP result.
	return mcpResult, nil
}
