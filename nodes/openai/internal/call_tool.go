// ./nodes/openai/middleware/internal/calltool.go
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"heart"

	// Corrected import path for MCPClient
	"github.com/mark3labs/mcp-go/client"

	"github.com/mark3labs/mcp-go/mcp"
	openai "github.com/sashabaranov/go-openai"
)

// Define a unique NodeTypeID for this internal node
const CallToolNodeTypeID heart.NodeTypeID = "openai_mcp_middleware:callTool"

var (
	errMCPToolDefinitionNotFound = errors.New("MCP tool definition not found in provided list")
	errUnsupportedToolCallType   = errors.New("unsupported tool call type in response")
)

// callToolInput holds the necessary data to invoke a single MCP tool.
type CallToolInput struct {
	ToolCall       openai.ToolCall      // The specific tool call from the LLM
	MCPToolsResult *mcp.ListToolsResult // The list of available MCP tools (for validation/lookup)
}

// callToolOutput holds the result of invoking a single MCP tool, formatted for the LLM.
type CallToolOutput struct {
	ResultMsg openai.ChatCompletionMessage // Formatted message for the next LLM request
	Error     error                        // Framework/invocation errors, NOT tool execution errors shown to LLM
}

// --- Initializer for Dependency Injection ---

type CallToolInitializer struct {
	// Use corrected type path
	client client.MCPClient
}

func (i *CallToolInitializer) ID() heart.NodeTypeID {
	return CallToolNodeTypeID
}

// Use corrected type path
func (i *CallToolInitializer) DependencyInject(c client.MCPClient) {
	if c == nil {
		return
	}
	i.client = c
}

// --- Resolver ---

type callToolResolver struct {
	initializer *CallToolInitializer
}

func (r *callToolResolver) Init() heart.NodeInitializer {
	if r.initializer == nil {
		r.initializer = &CallToolInitializer{}
	}
	return r.initializer
}

// formatToolResult formats the MCP tool result (or error) into a string for the LLM.
func formatToolResult(toolName string, result *mcp.CallToolResult, callErr error) string {
	if callErr != nil {
		// This is an error during the execution *of the tool itself* (e.g., invalid input).
		// Format this error message to be sent back to the LLM.
		errMsg := fmt.Sprintf("Error running tool %s: %v", toolName, callErr)
		fmt.Printf("INFO: Tool execution failed for %s: %v\n", toolName, callErr) // Log locally
		return errMsg
	}

	if result == nil || len(result.Content) == 0 {
		return "" // Or "Tool executed successfully with no output."
	}

	// Format the result content (mimics original python logic)
	var toolOutput string
	var marshalErr error
	var jsonBytes []byte

	if len(result.Content) == 1 {
		jsonBytes, marshalErr = json.Marshal(result.Content[0])
	} else {
		jsonBytes, marshalErr = json.Marshal(result.Content)
	}

	if marshalErr != nil {
		toolOutput = fmt.Sprintf("Error formatting tool result for %s: %v", toolName, marshalErr)
		fmt.Printf("WARN: Failed to marshal MCP result content for %s: %v\n", toolName, marshalErr)
	} else {
		toolOutput = string(jsonBytes)
	}
	return toolOutput
}

// Get calls a specific MCP tool based on the input.
func (r *callToolResolver) Get(ctx context.Context, in CallToolInput) (CallToolOutput, error) {
	if r.initializer == nil || r.initializer.client == nil {
		err := heart.ErrDependencyNotSet
		// Return error in output struct and as node error
		return CallToolOutput{Error: err}, err
	}

	if in.ToolCall.Type != openai.ToolTypeFunction {
		err := fmt.Errorf("%w: expected function, got %s", errUnsupportedToolCallType, in.ToolCall.Type)
		return CallToolOutput{Error: err}, err
	}

	toolName := in.ToolCall.Function.Name
	toolCallID := in.ToolCall.ID

	// Find the MCP tool definition (optional, but good for validation if needed)
	// For now, we assume the tool exists as the LLM called it.
	// We primarily need the client to make the call.

	// Parse arguments
	var inputArgs map[string]interface{} // Use interface{} as per mcp.CallToolRequest
	rawArgs := in.ToolCall.Function.Arguments
	if rawArgs != "" {
		err := json.Unmarshal([]byte(rawArgs), &inputArgs)
		if err != nil {
			// Argument parsing failed. This is an error *from* the LLM.
			// Report it back as the content of the tool message.
			errorMsg := fmt.Sprintf("Error: Invalid JSON arguments provided for tool %s: %v. Raw Arguments: %s", toolName, err, rawArgs)
			fmt.Printf("WARN: Invalid JSON arguments from LLM for tool %s: %v\n", toolName, err)
			resultMsg := openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    errorMsg,
				ToolCallID: toolCallID,
			}
			// No framework error here, return the message
			return CallToolOutput{ResultMsg: resultMsg}, nil
		}
	} else {
		inputArgs = make(map[string]interface{}) // Empty args if none provided
	}

	// Prepare MCP CallToolRequest - **FIXED INITIALIZATION**
	mcpReq := mcp.CallToolRequest{
		Request: mcp.Request{ // Initialize embedded Request struct if needed (e.g., method)
			Method: "tool/call", // Set appropriate method name
		},
		Params: struct { // Initialize nested Params struct
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments,omitempty"`
			Meta      *struct {
				ProgressToken mcp.ProgressToken `json:"progressToken,omitempty"`
			} `json:"_meta,omitempty"`
		}{
			Name:      toolName,
			Arguments: inputArgs,
			// Meta: nil, // Explicitly nil or omit if not needed
		},
	}

	// Invoke the tool via the MCPClient interface
	mcpResult, invokeErr := r.initializer.client.CallTool(ctx, mcpReq)
	// Note: invokeErr here represents errors *executing* the tool on the server,
	// including invalid inputs reported by the server, or server-side execution errors.

	// Format the result (or invokeErr) into the string content for the LLM
	resultContent := formatToolResult(toolName, mcpResult, invokeErr)

	// Create the message for the next LLM call
	resultMsg := openai.ChatCompletionMessage{
		Role:       openai.ChatMessageRoleTool,
		Content:    resultContent,
		ToolCallID: toolCallID,
	}

	// If invokeErr occurred, we've formatted it into resultContent.
	// We don't return invokeErr as the *node's* error, because the node itself
	// succeeded in calling the client and getting a response (even if that response was an error).
	// Framework errors (like DI not set, bad tool type) are returned as the node's error.
	return CallToolOutput{ResultMsg: resultMsg}, nil
}

// DefineCallToolNode creates the NodeDefinition for calling a single MCP tool.
func DefineCallToolNode(nodeID heart.NodeID) heart.NodeDefinition[CallToolInput, CallToolOutput] {
	return heart.DefineNode[CallToolInput, CallToolOutput](nodeID, &callToolResolver{})
}

// --- Compile-time checks ---
var _ heart.NodeResolver[CallToolInput, CallToolOutput] = (*callToolResolver)(nil)
var _ heart.NodeInitializer = (*CallToolInitializer)(nil)

// Use corrected type path
var _ heart.DependencyInjectable[client.MCPClient] = (*CallToolInitializer)(nil)
