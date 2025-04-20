// ./nodes/openai/internal/list_tools.go
package internal

import (
	"context"
	"fmt"

	"heart"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	openai "github.com/sashabaranov/go-openai"
)

// ListToolsNodeTypeID is the NodeTypeID used for dependency injection lookup
// for the internal ListTools node.
const ListToolsNodeTypeID heart.NodeTypeID = "openai_mcp_middleware:listTools"

// ListToolsNodeID is the default NodeID for the ListTools node definition.
const ListToolsNodeID heart.NodeID = "openai_list_tools"

// ListToolsResult holds the results from listing tools via an MCP client.
// It contains the tools formatted for OpenAI API requests and optionally the
// original MCP result for detailed invocation information, along with any error
// encountered during the listing process.
type ListToolsResult struct {
	// OpenAITools is a slice of tools formatted according to the openai.Tool schema,
	// suitable for inclusion in an openai.ChatCompletionRequest.
	OpenAITools []openai.Tool
	// MCPToolsResult holds the original, detailed result from the MCP client's
	// ListTools method. This might be needed by the CallTool node to correctly
	// invoke the selected tool. Can be nil if no tools were found or listing failed.
	MCPToolsResult *mcp.ListToolsResult
	// Error stores any error that occurred during the MCP client's ListTools call.
	// This is distinct from framework errors during node execution.
	Error error
}

// --- Initializer for Dependency Injection ---

// ListToolsInitializer handles dependency injection for the ListTools node.
// It stores the injected MCPClient instance.
type ListToolsInitializer struct {
	// client holds the injected MCP client instance.
	client mcpclient.MCPClient
}

// ID returns the NodeTypeID (openai_mcp_middleware:listTools) for this initializer.
func (i *ListToolsInitializer) ID() heart.NodeTypeID {
	return ListToolsNodeTypeID
}

// DependencyInject receives the MCP client dependency (as mcpclient.MCPClient)
// and stores it in the initializer. Called by the framework during definition Start.
func (i *ListToolsInitializer) DependencyInject(c mcpclient.MCPClient) {
	if c == nil {
		// If a nil client is injected, Get will fail later.
		return
	}
	i.client = c
}

// --- Resolver ---

// listToolsResolver is the NodeResolver for the internal ListTools node.
// It uses the injected MCPClient to list available tools.
type listToolsResolver struct {
	initializer *ListToolsInitializer
}

// Init initializes the resolver, creating the associated ListToolsInitializer.
// Called by the framework during definition Start.
func (r *listToolsResolver) Init() heart.NodeInitializer {
	if r.initializer == nil {
		r.initializer = &ListToolsInitializer{}
	}
	return r.initializer
}

// Get executes the logic for listing tools. It calls the ListTools method on the
// injected MCPClient and transforms the result into the ListToolsResult format,
// including converting MCP tool schemas into the format expected by the OpenAI API.
// The input `_ struct{}` is ignored.
func (r *listToolsResolver) Get(ctx context.Context, _ struct{}) (ListToolsResult, error) {
	// Check if the dependency was successfully injected.
	if r.initializer == nil || r.initializer.client == nil {
		// Use the shared ErrDependencyNotSet from the internal package.
		err := errDependencyNotSet // Assuming errDependencyNotSet is defined in this package or imported.
		// Return error in both the result struct and the function return for clarity.
		return ListToolsResult{Error: err}, err
	}

	// Call the MCP client to list tools.
	mcpToolsResult, err := r.initializer.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		// Wrap the error for context.
		errWrapped := fmt.Errorf("failed to list MCP tools: %w", err)
		return ListToolsResult{Error: errWrapped}, errWrapped
	}

	// If no tools were found, return an empty (but successful) result.
	if mcpToolsResult == nil || len(mcpToolsResult.Tools) == 0 {
		return ListToolsResult{}, nil
	}

	// Convert MCP tools to OpenAI tool format.
	openAITools := make([]openai.Tool, 0, len(mcpToolsResult.Tools))
	for _, mcpTool := range mcpToolsResult.Tools {
		// Extract the input schema definition from the MCP tool.
		paramsSchema := mcpTool.InputSchema // This is the struct mcp.ToolInputSchema

		// Create the map required by OpenAI Parameters.
		openAIParamsMap := make(map[string]any)

		// Set the type - OpenAI requires "object" for the parameters block.
		openAIParamsMap["type"] = "object"

		// Handle Properties - OpenAI requires the "properties" field, even if empty.
		if paramsSchema.Properties == nil {
			openAIParamsMap["properties"] = make(map[string]interface{}) // Empty map
		} else {
			openAIParamsMap["properties"] = paramsSchema.Properties // Use provided properties
		}

		// Handle Required fields if they exist.
		if len(paramsSchema.Required) > 0 {
			openAIParamsMap["required"] = paramsSchema.Required
		}

		// Append the converted tool to the list.
		openAITools = append(openAITools, openai.Tool{
			Type: openai.ToolTypeFunction, // Assuming all MCP tools map to function types for OpenAI.
			Function: &openai.FunctionDefinition{
				Name:        mcpTool.Name,
				Description: mcpTool.Description,
				Parameters:  openAIParamsMap, // Assign the correctly constructed map.
			},
		})
	}

	// Return the successful result containing both formats.
	return ListToolsResult{
		OpenAITools:    openAITools,
		MCPToolsResult: mcpToolsResult, // Include the original MCP result.
	}, nil
}

// ListTools creates the NodeDefinition for the internal node responsible for
// listing available MCP tools. This definition is used by the WithMCP middleware.
func ListTools() heart.NodeDefinition[struct{}, ListToolsResult] {
	return heart.DefineNode(ListToolsNodeID, &listToolsResolver{})
}

// --- Compile-time checks ---

// Ensures listToolsResolver implements NodeResolver.
var _ heart.NodeResolver[struct{}, ListToolsResult] = (*listToolsResolver)(nil)

// Ensures ListToolsInitializer implements NodeInitializer.
var _ heart.NodeInitializer = (*ListToolsInitializer)(nil)

// Ensures ListToolsInitializer implements DependencyInjectable with the MCPClient type.
var _ heart.DependencyInjectable[mcpclient.MCPClient] = (*ListToolsInitializer)(nil)
