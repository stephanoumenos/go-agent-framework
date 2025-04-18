// ./nodes/openai/middleware/internal/list_tools.go
package internal

import (
	"context"
	"fmt"
	"heart"

	// Corrected import path for MCPClient
	"github.com/mark3labs/mcp-go/client"

	"github.com/mark3labs/mcp-go/mcp"
	openai "github.com/sashabaranov/go-openai"
)

// Define a unique NodeTypeID for this internal node
const ListToolsNodeTypeID heart.NodeTypeID = "openai_mcp_middleware:listTools"

// listToolsResult holds the results from listing tools.
// We need both OpenAI format for the request and MCP format for invocation details.
type ListToolsResult struct {
	OpenAITools    []openai.Tool
	MCPToolsResult *mcp.ListToolsResult // Keep the original result for invocation lookup
	Error          error                // Propagate errors from listing
}

// --- Initializer for Dependency Injection ---

type ListToolsInitializer struct {
	// Use corrected type path
	client client.MCPClient
}

func (i *ListToolsInitializer) ID() heart.NodeTypeID {
	return ListToolsNodeTypeID
}

// Use corrected type path
func (i *ListToolsInitializer) DependencyInject(c client.MCPClient) {
	if c == nil {
		// Handle nil injection - Get will fail later
		return
	}
	i.client = c
}

// --- Resolver ---

type listToolsResolver struct {
	initializer *ListToolsInitializer
}

func (r *listToolsResolver) Init() heart.NodeInitializer {
	if r.initializer == nil {
		r.initializer = &ListToolsInitializer{}
	}
	return r.initializer
}

// Get lists tools from the injected MCPClient.
// Input is ignored (struct{}).
func (r *listToolsResolver) Get(ctx context.Context, _ struct{}) (ListToolsResult, error) {
	if r.initializer == nil || r.initializer.client == nil {
		return ListToolsResult{Error: heart.ErrDependencyNotSet}, heart.ErrDependencyNotSet
	}

	mcpToolsResult, err := r.initializer.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		errWrapped := fmt.Errorf("failed to list MCP tools: %w", err)
		return ListToolsResult{Error: errWrapped}, errWrapped
	}

	if mcpToolsResult == nil || len(mcpToolsResult.Tools) == 0 {
		return ListToolsResult{}, nil
	}

	openAITools := make([]openai.Tool, 0, len(mcpToolsResult.Tools))
	for _, mcpTool := range mcpToolsResult.Tools {
		// --- Corrected handling of mcp.ToolInputSchema ---
		paramsSchema := mcpTool.InputSchema // This is the struct mcp.ToolInputSchema

		// Create the map required by OpenAI Parameters
		openAIParamsMap := make(map[string]any)

		// Set the type - OpenAI requires "object" for the parameters block.
		// Use the type from MCP schema if present, otherwise default to "object".
		// It's safest to always set it to "object" as required by OpenAI.
		openAIParamsMap["type"] = "object"
		// if paramsSchema.Type != "" {
		//     openAIParamsMap["type"] = paramsSchema.Type
		// } else {
		//     openAIParamsMap["type"] = "object" // Default for OpenAI
		// }

		// Handle Properties - Check if the map inside the struct is nil
		if paramsSchema.Properties == nil {
			// OpenAI requires the "properties" field, even if empty.
			openAIParamsMap["properties"] = make(map[string]interface{})
		} else {
			openAIParamsMap["properties"] = paramsSchema.Properties
		}

		// Handle Required fields if they exist
		if len(paramsSchema.Required) > 0 {
			openAIParamsMap["required"] = paramsSchema.Required
		}
		// --- End of corrected handling ---

		openAITools = append(openAITools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        mcpTool.Name,
				Description: mcpTool.Description,
				Parameters:  openAIParamsMap, // Assign the correctly constructed map
			},
		})
	}

	return ListToolsResult{
		OpenAITools:    openAITools,
		MCPToolsResult: mcpToolsResult,
	}, nil
}

// DefineListToolsNode creates the NodeDefinition for listing MCP tools.
func DefineListToolsNode(nodeID heart.NodeID) heart.NodeDefinition[struct{}, ListToolsResult] {
	return heart.DefineNode[struct{}, ListToolsResult](nodeID, &listToolsResolver{})
}

// --- Compile-time checks ---
var _ heart.NodeResolver[struct{}, ListToolsResult] = (*listToolsResolver)(nil)
var _ heart.NodeInitializer = (*ListToolsInitializer)(nil)

// Use corrected type path
var _ heart.DependencyInjectable[client.MCPClient] = (*ListToolsInitializer)(nil)
