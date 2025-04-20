// ./nodes/openai/internal/call_tool.go
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"heart"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	openai "github.com/sashabaranov/go-openai"
)

// CallToolNodeTypeID is the NodeTypeID used for dependency injection lookup
// for the internal CallTool node.
const CallToolNodeTypeID heart.NodeTypeID = "openai_mcp_middleware:call_tool"

// CallToolNodeID is the default NodeID for the CallTool node definition.
const CallToolNodeID heart.NodeID = "call_tool"

var (
	// errUnsupportedToolCallType indicates the LLM requested a tool call type
	// other than 'function'.
	errUnsupportedToolCallType = errors.New("unsupported tool call type in response")
	// errDependencyNotSet indicates the MCP client dependency was required but not provided.
	// NOTE: Consider reusing a shared internal error if defined elsewhere.
	errDependencyNotSet = errors.New("MCP client not provided. Use heart.Dependencies(openai.InjectMCPClient(client)) to provide it")
)

// CallToolInput holds the necessary data to invoke a single MCP tool based on
// an LLM's request.
type CallToolInput struct {
	// ToolCall contains the specific tool call information (ID, function name, arguments)
	// received from the openai.ChatCompletionResponse.
	ToolCall openai.ToolCall
	// MCPToolsResult is the original result from the ListTools node, containing
	// details about available MCP tools needed for invocation. Can be nil if
	// ListTools failed or returned no tools, which should be handled gracefully.
	MCPToolsResult *mcp.ListToolsResult
}

// CallToolOutput holds the result of invoking a single MCP tool, formatted
// appropriately for inclusion in the next request to the LLM.
type CallToolOutput struct {
	// ResultMsg contains the formatted openai.ChatCompletionMessage (with RoleTool)
	// representing the tool's output or an error encountered during its execution.
	// This message is suitable for appending to the chat history.
	ResultMsg openai.ChatCompletionMessage
	// Error stores framework-level or invocation errors (e.g., dependency injection failure,
	// client call failure, invalid ToolCallInput). It does *not* typically store errors
	// that occurred within the tool's own logic; those should be formatted into ResultMsg.Content.
	Error error
}

// --- Initializer for Dependency Injection ---

// CallToolInitializer handles dependency injection for the CallTool node.
// It stores the injected MCPClient instance.
type CallToolInitializer struct {
	// client holds the injected MCP client instance.
	client mcpclient.MCPClient
}

// ID returns the NodeTypeID (openai_mcp_middleware:call_tool) for this initializer.
func (i *CallToolInitializer) ID() heart.NodeTypeID {
	return CallToolNodeTypeID
}

// DependencyInject receives the MCP client dependency (as mcpclient.MCPClient)
// and stores it in the initializer. Called by the framework during definition Start.
func (i *CallToolInitializer) DependencyInject(c mcpclient.MCPClient) {
	if c == nil {
		// If a nil client is injected, Get will fail later.
		return
	}
	i.client = c
}

// --- Resolver ---

// callToolResolver is the NodeResolver for the internal CallTool node.
// It uses the injected MCPClient to execute a specific tool call requested by the LLM.
type callToolResolver struct {
	initializer *CallToolInitializer
}

// Init initializes the resolver, creating the associated CallToolInitializer.
// Called by the framework during definition Start.
func (r *callToolResolver) Init() heart.NodeInitializer {
	if r.initializer == nil {
		r.initializer = &CallToolInitializer{}
	}
	return r.initializer
}

// formatToolResult converts the result or error from an MCP tool execution
// (*mcp.CallToolResult, error from client.CallTool) into a plain string.
// This string is intended to be placed in the `Content` field of the
// `openai.ChatMessageRoleTool` message sent back to the LLM.
//
// It handles:
//   - Errors during the MCP client call itself (`callErr`).
//   - Errors reported by the tool's execution (`result.IsError == true`).
//   - Successful execution with content (extracting text from `mcp.TextContent`).
//   - Successful execution with no content or unsupported content types.
//   - Nil results or empty content slices.
func formatToolResult(toolName string, result *mcp.CallToolResult, callErr error) string {
	// 1. Handle errors during the MCP client call (network, server unavailable, etc.)
	if callErr != nil {
		errMsg := fmt.Sprintf("Error invoking tool %s: %v", toolName, callErr)
		// Log the underlying error for debugging.
		fmt.Printf("ERROR: MCP client call failed for %s: %v\n", toolName, callErr)
		return errMsg // Return a user-facing error message to the LLM.
	}

	// 2. Handle errors reported *by the tool's logic* via IsError flag.
	if result != nil && result.IsError {
		// Tool ran but reported failure.
		if len(result.Content) == 0 {
			// If IsError is true but no content is provided, return a generic message.
			fmt.Printf("WARN: Tool %s reported IsError=true but provided no content details.\n", toolName)
			return fmt.Sprintf("Tool %s reported an error but provided no details.", toolName)
		}
		// If content *is* provided alongside IsError, format it below, likely containing the error details.
		fmt.Printf("INFO: Tool %s reported IsError=true. Formatting content as error details.\n", toolName)
		// Prepend message to indicate the content represents an error from the tool.
		return fmt.Sprintf("Error reported by tool %s: %s", toolName, formatMcpContent(toolName, result.Content))
	}

	// 3. Handle successful execution but nil result or no content.
	if result == nil || len(result.Content) == 0 {
		fmt.Printf("INFO: Tool %s executed successfully with no output content.\n", toolName)
		return fmt.Sprintf("Tool %s executed successfully with no output.", toolName) // Indicate success without output.
	}

	// 4. Handle successful execution with content.
	fmt.Printf("INFO: Successfully executed tool %s. Formatting content.\n", toolName)
	return formatMcpContent(toolName, result.Content)
}

// formatMcpContent extracts and formats text content from a slice of mcp.Content.
// It primarily looks for mcp.TextContent and joins the text parts.
// Unsupported types are logged and represented with placeholders in the output string.
func formatMcpContent(toolName string, content []mcp.Content) string {
	var textParts []string
	hasNonText := false

	for i, item := range content {
		// Use a type switch to check the actual underlying type.
		switch v := item.(type) {
		case mcp.TextContent:
			// Extract text from TextContent.
			textParts = append(textParts, v.Text)
		// Add cases for other expected content types (e.g., mcp.ImageContent)
		// if specific handling (like adding placeholders) is needed.
		default:
			// Handle unknown or unsupported content types.
			hasNonText = true
			contentType := fmt.Sprintf("%T", v) // Get the type name.
			fmt.Printf("WARN: Unsupported content type '%s' at index %d for tool %s.\n", contentType, i, toolName)

			// Fallback: Try to marshal the unknown item to JSON for inspection.
			jsonBytes, marshalErr := json.Marshal(v)
			if marshalErr != nil {
				textParts = append(textParts, fmt.Sprintf("[Unsupported Content Type: %s - Error marshaling: %v]", contentType, marshalErr))
			} else {
				textParts = append(textParts, fmt.Sprintf("[Unsupported Content Type: %s - Data: %s]", contentType, string(jsonBytes)))
			}
		}
	}

	if len(textParts) == 0 {
		// This could happen if all content items were unsupported types.
		fmt.Printf("WARN: Tool %s produced content, but no text parts found or successfully formatted.\n", toolName)
		return fmt.Sprintf("[Tool %s produced content, but no readable text was extracted]", toolName)
	}

	// Join the extracted/formatted text parts. Newline is a reasonable separator.
	finalContent := strings.Join(textParts, "\n")

	// Log summary.
	logMsg := fmt.Sprintf("Successfully formatted text content for tool %s.", toolName)
	if hasNonText {
		logMsg = fmt.Sprintf("Processed content for tool %s (included non-text/unsupported items).", toolName)
	}
	fmt.Printf("INFO: %s Final output length: %d\n", logMsg, len(finalContent))

	return finalContent
}

// Get executes the logic for calling a specific MCP tool. It takes the CallToolInput
// (containing the LLM's requested tool call and the list of available tools),
// calls the appropriate tool via the injected MCPClient, formats the result using
// formatToolResult, and returns it in the CallToolOutput struct.
func (r *callToolResolver) Get(ctx context.Context, in CallToolInput) (CallToolOutput, error) {
	// Check if the dependency was successfully injected.
	if r.initializer == nil || r.initializer.client == nil {
		err := errDependencyNotSet
		// Return error in the output struct's Error field and as the node execution error.
		return CallToolOutput{Error: err}, err
	}

	// Validate the tool call type from the LLM.
	if in.ToolCall.Type != openai.ToolTypeFunction {
		err := fmt.Errorf("%w: expected function, got %s (tool_id: %s)", errUnsupportedToolCallType, in.ToolCall.Type, in.ToolCall.ID)
		// Return error in the output struct and as the node error.
		return CallToolOutput{Error: err}, err
	}

	// --- Prepare for MCP Call ---
	toolName := in.ToolCall.Function.Name
	toolCallID := in.ToolCall.ID // Crucial for matching response back to the LLM request.

	// Parse arguments from the LLM's request string into a map.
	var inputArgs map[string]interface{}
	rawArgs := in.ToolCall.Function.Arguments
	if rawArgs != "" {
		err := json.Unmarshal([]byte(rawArgs), &inputArgs)
		if err != nil {
			// If the LLM provided invalid JSON arguments, report this back *in the content*
			// of the Tool message, rather than failing the node execution itself.
			errorMsg := fmt.Sprintf("Error: Invalid JSON arguments provided by LLM for tool %s (call_id: %s): %v. Raw Arguments: %s", toolName, toolCallID, err, rawArgs)
			fmt.Printf("WARN: Invalid JSON arguments from LLM for tool %s: %v\n", toolName, err)
			resultMsg := openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    errorMsg, // Error details go into the content for the LLM.
				ToolCallID: toolCallID,
			}
			return CallToolOutput{ResultMsg: resultMsg}, nil // Return nil for the node error.
		}
	} else {
		// If arguments string is empty, use an empty map.
		inputArgs = make(map[string]interface{})
	}

	// TODO: Validate required arguments against MCPToolsResult schema if necessary before calling.

	// Prepare the request object for the MCP client's CallTool method.
	mcpReq := mcp.CallToolRequest{
		// Request: mcp.Request{Method: "tool/call"}, // Method may not be needed depending on client impl.
		Params: struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments,omitempty"`
			Meta      *struct {
				ProgressToken mcp.ProgressToken `json:"progressToken,omitempty"`
			} `json:"_meta,omitempty"`
		}{
			Name:      toolName,
			Arguments: inputArgs,
			// Meta: nil, // Progress token handling not implemented here.
		},
	}

	// --- Invoke the Tool via MCPClient ---
	// invokeErr captures errors *during* the MCP call or framework issues on the server side.
	// mcpResult contains the tool's output *or* an error reported by the tool's logic (IsError=true).
	mcpResult, invokeErr := r.initializer.client.CallTool(ctx, mcpReq)

	// --- Format Result for LLM ---
	// Convert the MCP result/error into a plain string for the LLM's context.
	resultContent := formatToolResult(toolName, mcpResult, invokeErr)

	// --- Prepare Output Message ---
	// Create the message for the next LLM call.
	resultMsg := openai.ChatCompletionMessage{
		Role:       openai.ChatMessageRoleTool,
		Content:    resultContent, // Use the formatted plain text (or error string).
		ToolCallID: toolCallID,    // Use the original ID from the LLM's request.
		Name:       toolName,      // Include the function name.
	}

	// The node execution is considered successful if it reached this point,
	// even if the tool itself reported an error (which is handled by formatToolResult).
	// Return the formatted message and nil node error.
	return CallToolOutput{ResultMsg: resultMsg}, nil
}

// CallTool creates the NodeDefinition for the internal node responsible for
// calling a single MCP tool based on an LLM request. This definition is used
// by the WithMCP middleware.
func CallTool() heart.NodeDefinition[CallToolInput, CallToolOutput] {
	return heart.DefineNode(CallToolNodeID, &callToolResolver{})
}

// --- Compile-time checks ---

// Ensures callToolResolver implements NodeResolver.
var _ heart.NodeResolver[CallToolInput, CallToolOutput] = (*callToolResolver)(nil)

// Ensures CallToolInitializer implements NodeInitializer.
var _ heart.NodeInitializer = (*CallToolInitializer)(nil)

// Ensures CallToolInitializer implements DependencyInjectable with the MCPClient type.
var _ heart.DependencyInjectable[mcpclient.MCPClient] = (*CallToolInitializer)(nil)
