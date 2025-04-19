// ./nodes/openai/middleware/internal/calltool.go
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"heart"
	"strings"

	"github.com/mark3labs/mcp-go/client"

	"github.com/mark3labs/mcp-go/mcp"
	openai "github.com/sashabaranov/go-openai"
)

// Define a unique NodeTypeID for this internal node
const CallToolNodeTypeID heart.NodeTypeID = "openai_mcp_middleware:call_tool"
const CallToolNodeID heart.NodeID = "call_tool"

var (
	errUnsupportedToolCallType = errors.New("unsupported tool call type in response")
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

// formatToolResult formats the MCP tool result (or error) into a plain string for the LLM.
func formatToolResult(toolName string, result *mcp.CallToolResult, callErr error) string {
	if callErr != nil {
		// This is an error during the MCP call *itself* (network, server unavailable, etc.)
		// OR an error reported by the tool execution framework on the server side.
		errMsg := fmt.Sprintf("Error invoking or executing tool %s: %v", toolName, callErr)
		fmt.Printf("ERROR: MCP call/execution failed for %s: %v\n", toolName, callErr)
		return errMsg
	}

	// Check if the result object itself indicates an error reported *by the tool's logic*
	if result != nil && result.IsError {
		// Tool ran but reported failure. Format the content as the error message.
		// If content is empty, provide a generic error message based on IsError.
		if len(result.Content) == 0 {
			fmt.Printf("WARN: Tool %s reported IsError=true but provided no content details.\n", toolName)
			return fmt.Sprintf("Tool %s reported an error.", toolName) // Simple error message for LLM
		}
		// Proceed to format content below - it likely contains the tool's error description.
		fmt.Printf("INFO: Tool %s reported IsError=true. Formatting content as error details.\n", toolName)
	}

	// Handle case where the call was successful (callErr == nil and !result.IsError) but no content was returned.
	if result == nil || len(result.Content) == 0 {
		// This case might occur if IsError was true but Content was empty (handled above),
		// or if the call was successful but genuinely returned no content.
		if result != nil && result.IsError {
			// Already handled above, but defensive return
			return fmt.Sprintf("Tool %s reported an error.", toolName)
		}
		fmt.Printf("INFO: Tool %s executed successfully with no output content.\n", toolName)
		return fmt.Sprintf("Tool %s executed successfully with no output.", toolName) // Indicate success without output
	}

	// --- Process Content using Type Switch ---
	var textParts []string
	hasNonText := false

	for i, item := range result.Content {
		// Use a type switch to check the actual underlying type of the Content interface item
		switch v := item.(type) {
		case mcp.TextContent: // <<<=== ADJUST THIS TYPE NAME IF NEEDED (e.g., mcpschema.TextContent)
			// Successfully identified as TextContent
			textParts = append(textParts, v.Text) // Access the Text field from TextContent
		// Add cases for other expected content types if you need specific handling:
		// case mcp.ImageContent:
		//     hasNonText = true
		//     // Example: Append a placeholder or description
		//     textParts = append(textParts, fmt.Sprintf("[Image Content received: %s]", v.Description))
		default:
			// Handle unknown or unsupported content types
			hasNonText = true
			contentType := fmt.Sprintf("%T", v) // Get the type name
			fmt.Printf("WARN: Unsupported content type '%s' at index %d for tool %s.\n", contentType, i, toolName)

			// Fallback: Try to marshal the unknown item to JSON to see what it is
			jsonBytes, marshalErr := json.Marshal(v)
			if marshalErr != nil {
				textParts = append(textParts, fmt.Sprintf("[Unsupported Content Type: %s - Error marshaling: %v]", contentType, marshalErr))
			} else {
				textParts = append(textParts, fmt.Sprintf("[Unsupported Content Type: %s - Data: %s]", contentType, string(jsonBytes)))
			}
		}
	}

	if len(textParts) == 0 {
		// This could happen if all content items were of unsupported types that didn't even marshal.
		fmt.Printf("WARN: Tool %s produced content, but no text parts found or successfully formatted.\n", toolName)
		return fmt.Sprintf("[Tool %s produced content, but no readable text was extracted]", toolName)
	}

	// Join the extracted/formatted text parts
	finalContent := strings.Join(textParts, "\n") // Use newline as a separator

	// Log summary
	if hasNonText {
		fmt.Printf("INFO: Processed content for tool %s (included non-text/unsupported items). Final formatted output length: %d\n", toolName, len(finalContent))
	} else {
		fmt.Printf("INFO: Successfully formatted text content for tool %s. Final output length: %d\n", toolName, len(finalContent))
	}

	// If the result indicated an error, prepend a note for clarity? Optional.
	if result.IsError {
		return fmt.Sprintf("Error reported by tool %s: %s", toolName, finalContent)
	}

	return finalContent
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

	// Parse arguments (error handling remains the same)
	var inputArgs map[string]interface{}
	rawArgs := in.ToolCall.Function.Arguments
	if rawArgs != "" {
		err := json.Unmarshal([]byte(rawArgs), &inputArgs)
		if err != nil {
			// Argument parsing failed (LLM error). Report back in content.
			errorMsg := fmt.Sprintf("Error: Invalid JSON arguments provided for tool %s: %v. Raw Arguments: %s", toolName, err, rawArgs)
			fmt.Printf("WARN: Invalid JSON arguments from LLM for tool %s: %v\n", toolName, err)
			resultMsg := openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    errorMsg,
				ToolCallID: toolCallID,
			}
			return CallToolOutput{ResultMsg: resultMsg}, nil // No framework error
		}
	} else {
		inputArgs = make(map[string]interface{}) // Empty args
	}

	// Prepare MCP CallToolRequest (remains the same)
	mcpReq := mcp.CallToolRequest{
		Request: mcp.Request{Method: "tool/call"}, // Ensure method is set if needed by client impl
		Params: struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments,omitempty"`
			Meta      *struct {
				ProgressToken mcp.ProgressToken `json:"progressToken,omitempty"`
			} `json:"_meta,omitempty"`
		}{
			Name:      toolName,
			Arguments: inputArgs,
		},
	}

	// Invoke the tool via the MCPClient interface
	// invokeErr captures errors *during* the MCP call or tool execution framework (e.g., tool not found, server error)
	// mcpResult contains the tool's output *or* an error reported by the tool's logic (IsError=true)
	mcpResult, invokeErr := r.initializer.client.CallTool(ctx, mcpReq)

	// Format the result (or invokeErr, or IsError within mcpResult) into the string content for the LLM
	resultContent := formatToolResult(toolName, mcpResult, invokeErr)

	// Create the message for the next LLM call
	resultMsg := openai.ChatCompletionMessage{
		Role:       openai.ChatMessageRoleTool,
		Content:    resultContent, // Use the properly formatted plain text (or error)
		ToolCallID: toolCallID,
	}

	// The node succeeded if it could call the client and get a result/error back.
	// Tool execution errors (invokeErr or result.IsError) are handled by formatToolResult
	// and put into the resultMsg.Content, not returned as the node's error.
	return CallToolOutput{ResultMsg: resultMsg}, nil
}

// CallTool creates the NodeDefinition for calling a single MCP tool.
func CallTool() heart.NodeDefinition[CallToolInput, CallToolOutput] {
	return heart.DefineNode(CallToolNodeID, &callToolResolver{})
}

// --- Compile-time checks ---
var _ heart.NodeResolver[CallToolInput, CallToolOutput] = (*callToolResolver)(nil)
var _ heart.NodeInitializer = (*CallToolInitializer)(nil)

// Use corrected type path
var _ heart.DependencyInjectable[client.MCPClient] = (*CallToolInitializer)(nil)
