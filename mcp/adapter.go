// ./nodes/openai/middleware/mcp.go
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"heart" // Assuming heart is correctly imported
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp" // Import mcp types
	"github.com/mark3labs/mcp-go/server"
	openai "github.com/sashabaranov/go-openai"
)

const withMCPNodeTypeID heart.NodeTypeID = "openai_with_mcp_middleware"

var (
	errNoToolCalls         = errors.New("no tool calls present in the response")
	errMCPToolNotFound     = errors.New("MCP tool not found in the available tools map")
	errMCPInvocationError  = errors.New("error invoking MCP tool")
	errUnsupportedToolCall = errors.New("unsupported tool call type in response")
	errSchemaConversion    = errors.New("error converting MCP tool schema to OpenAI format")
	errDuplicateToolName   = errors.New("duplicate tool name found across MCP servers")
	errToolDiscoveryFailed = errors.New("failed to discover tools from one or more MCP servers")
	errFrameworkError      = errors.New("internal framework error during tool invocation")
)

// --- Helper Function: Convert MCP Tool to OpenAI FunctionDefinition ---

func mcpToolToOpenAIFunction(mcpTool mcp.Tool) (openai.FunctionDefinition, error) {
	var params map[string]any

	// Prefer RawInputSchema if available
	if mcpTool.RawInputSchema != nil {
		if mcpTool.InputSchema.Type != "" || len(mcpTool.InputSchema.Properties) > 0 {
			// Warn or error if both are somehow populated, prefer Raw
			fmt.Printf("WARN: MCP Tool '%s' has both InputSchema and RawInputSchema; using RawInputSchema.\n", mcpTool.Name)
		}
		err := json.Unmarshal(mcpTool.RawInputSchema, &params)
		if err != nil {
			return openai.FunctionDefinition{}, fmt.Errorf("%w: failed to unmarshal RawInputSchema for tool '%s': %v", errSchemaConversion, mcpTool.Name, err)
		}
	} else {
		// Use structured InputSchema - marshal then unmarshal to get map[string]any
		schemaBytes, err := json.Marshal(mcpTool.InputSchema)
		if err != nil {
			return openai.FunctionDefinition{}, fmt.Errorf("%w: failed to marshal structured InputSchema for tool '%s': %v", errSchemaConversion, mcpTool.Name, err)
		}
		err = json.Unmarshal(schemaBytes, &params)
		if err != nil {
			return openai.FunctionDefinition{}, fmt.Errorf("%w: failed to unmarshal marshaled structured InputSchema for tool '%s': %v", errSchemaConversion, mcpTool.Name, err)
		}
	}

	// Ensure required OpenAI structure for parameters
	if params == nil {
		params = make(map[string]any)
	}
	if _, ok := params["type"]; !ok {
		params["type"] = "object" // Default to object if not specified
	}
	if params["type"] == "object" {
		if _, ok := params["properties"]; !ok {
			params["properties"] = make(map[string]any) // Ensure properties map exists for object type
		}
	}
	// Note: OpenAI might require 'required' field even if empty list, but go-openai handles nil okay typically.

	return openai.FunctionDefinition{
		Name:        mcpTool.Name,
		Description: mcpTool.Description,
		Parameters:  params,
	}, nil
}

// --- Helper Function: Invoke MCP Tool ---

// invokeMCPTool finds the correct server via the map and calls the tool.
// It returns the string content for the LLM and a potential framework error.
// Tool execution errors are embedded within the content string.
func invokeMCPTool(
	ctx context.Context, // Use the context from the heart node
	toolCall openai.ToolCall,
	serverMap map[string]server.MCPServer, // Map tool name to server
) (content string, frameworkErr error) {

	if toolCall.Type != openai.ToolTypeFunction {
		return "", fmt.Errorf("%w: expected function, got %s", errUnsupportedToolCall, toolCall.Type)
	}

	toolName := toolCall.Function.Name
	targetServer, ok := serverMap[toolName]
	if !ok {
		// This is a framework error - the LLM hallucinated a tool or our map is wrong.
		return "", fmt.Errorf("%w: tool '%s' requested by LLM but not found in provided MCP servers map", errMCPToolNotFound, toolName)
	}

	var inputArgs map[string]any
	if toolCall.Function.Arguments != "" {
		err := json.Unmarshal([]byte(toolCall.Function.Arguments), &inputArgs)
		if err != nil {
			// LLM provided bad JSON. Report this *as content* for the LLM to potentially fix.
			errorMsg := fmt.Sprintf("Error: Invalid JSON arguments provided for tool %s: %v. Raw Input: %s", toolName, err, toolCall.Function.Arguments)
			fmt.Printf("WARN: invokeMCPTool JSON unmarshal failed for %s: %v\n", toolName, err)
			return errorMsg, nil // Return error message as content, no framework error
		}
	} else {
		inputArgs = make(map[string]any) // Empty arguments
	}

	// Invoke the tool via the server interface using the node's context
	mcpResult, err := targetServer.CallTool(ctx, toolName, inputArgs)
	if err != nil {
		// Error during the actual tool call on the MCP server.
		// Report this *as content* for the LLM.
		errMsg := fmt.Sprintf("Error invoking MCP tool '%s' on server '%s': %v", toolName, targetServer.Name(), err)
		fmt.Printf("ERROR: CallTool failed: %s\n", errMsg)
		// Return the error message as the result content for the LLM
		return fmt.Sprintf("Error running tool %s: %v", toolName, err), nil // No framework error here, the tool itself failed.
	}

	// Format the result according to OpenAI expectations (string content)
	// Check if the tool reported an error internally
	if mcpResult.IsError && len(mcpResult.Content) > 0 {
		// Try to format the error content if available
		jsonBytes, err := json.Marshal(mcpResult.Content)
		if err != nil {
			toolOutput := fmt.Sprintf("Tool %s reported an error, but failed to format error content: %v", toolName, err)
			fmt.Printf("WARN: Failed to marshal MCP error result for %s: %v\n", toolName, err)
			return toolOutput, nil
		}
		return fmt.Sprintf("Tool %s failed with error: %s", toolName, string(jsonBytes)), nil

	} else if mcpResult.IsError {
		// Tool reported an error but provided no content
		return fmt.Sprintf("Tool %s reported an unspecified error.", toolName), nil
	}

	// Process successful result content
	var toolOutput string
	switch len(mcpResult.Content) {
	case 0:
		// Handle empty content - maybe return an empty string or specific message?
		toolOutput = "" // Or "Tool executed successfully with no output."
	case 1:
		// Marshal the single content item directly
		jsonBytes, err := json.Marshal(mcpResult.Content[0])
		if err != nil {
			toolOutput = fmt.Sprintf("Error formatting tool result for %s: %v", toolName, err)
			fmt.Printf("WARN: Failed to marshal single MCP result item for %s: %v\n", toolName, err)
		} else {
			toolOutput = string(jsonBytes)
		}
	default: // > 1
		// Marshal the whole list of content items
		jsonBytes, err := json.Marshal(mcpResult.Content)
		if err != nil {
			toolOutput = fmt.Sprintf("Error formatting tool result array for %s: %v", toolName, err)
			fmt.Printf("WARN: Failed to marshal MCP result list for %s: %v\n", toolName, err)
		} else {
			toolOutput = string(jsonBytes)
		}
	}

	return toolOutput, nil // Success, return formatted content and nil error
}

// --- Workflow State and Result Structs ---

// getToolsResult holds the outcome of the tool discovery node.
type getToolsResult struct {
	Functions []openai.FunctionDefinition
	ServerMap map[string]server.MCPServer
	Error     error // To propagate discovery/setup errors
}

// mcpWorkflowState holds data passed between internal nodes of the WithMCP workflow.
type mcpWorkflowState struct {
	OriginalRequest    openai.ChatCompletionRequest
	ModifiedRequest    openai.ChatCompletionRequest // Request potentially with tools added
	FirstResponse      openai.ChatCompletionResponse
	ToolResultMessages []openai.ChatCompletionMessage // Messages generated from tool calls
	ToolServerMap      map[string]server.MCPServer    // Map tool name -> server, built once
	NeedsSecondCall    bool
	Error              error // To propagate errors between internal nodes (framework errors)
}

// --- Workflow Handler ---

// mcpWorkflowHandler defines the logic for the WithMCP middleware.
func mcpWorkflowHandler(
	nextNodeDef heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
	servers []server.MCPServer,
) heart.WorkflowHandlerFunc[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	return func(ctx heart.Context, originalReq openai.ChatCompletionRequest) heart.ExecutionHandle[openai.ChatCompletionResponse] {

		// --- Node 1: Get MCP Tools and Build Map ---
		getToolsNodeID := heart.NodeID("get_mcp_tools")
		getToolsNode := heart.NewNode(ctx, getToolsNodeID,
			func(toolsCtx heart.NewNodeContext) heart.ExecutionHandle[getToolsResult] {
				result := getToolsResult{
					Functions: make([]openai.FunctionDefinition, 0),
					ServerMap: make(map[string]server.MCPServer),
				}

				if len(servers) == 0 {
					return heart.Into(result) // No servers, no tools, no error
				}

				type serverResult struct {
					server server.MCPServer
					tools  []mcp.Tool
					err    error
				}

				resultsChan := make(chan serverResult, len(servers))
				var wg sync.WaitGroup

				for _, srv := range servers {
					wg.Add(1)
					go func(s server.MCPServer) {
						defer wg.Done()
						// Use toolsCtx.Context for cancellation propagation
						serverTools, err := s.ListTools(toolsCtx.Context()) // Use node context's underlying context.Context
						resultsChan <- serverResult{server: s, tools: serverTools, err: err}
					}(srv)
				}

				go func() {
					wg.Wait()
					close(resultsChan)
				}()

				var discoveryErrors []string
				processedToolNames := make(map[string]string) // Track tool names to detect duplicates

				for res := range resultsChan {
					if res.err != nil {
						errMsg := fmt.Sprintf("server '%s': %v", res.server.Name(), res.err)
						discoveryErrors = append(discoveryErrors, errMsg)
						continue // Skip processing tools from this server if listing failed
					}

					for _, mcpTool := range res.tools {
						// Check for duplicates
						if existingServerName, exists := processedToolNames[mcpTool.Name]; exists {
							errMsg := fmt.Sprintf("tool name '%s' conflict: found on server '%s' and server '%s'", mcpTool.Name, existingServerName, res.server.Name())
							// Decide how to handle duplicates: error out, ignore, or take first? Let's error.
							if result.Error == nil { // Only store the first duplicate error message clearly
								result.Error = fmt.Errorf("%w: %s", errDuplicateToolName, errMsg)
							} else { // Append to existing errors if needed
								result.Error = fmt.Errorf("%w; %s", result.Error, errMsg)
							}
							continue // Skip this duplicate tool
						}

						// Convert tool
						funcDef, err := mcpToolToOpenAIFunction(mcpTool)
						if err != nil {
							errMsg := fmt.Sprintf("tool '%s' on server '%s': %v", mcpTool.Name, res.server.Name(), err)
							// Collect conversion errors
							if result.Error == nil {
								result.Error = fmt.Errorf("%w: %s", errSchemaConversion, errMsg)
							} else {
								result.Error = fmt.Errorf("%w; %s", result.Error, errMsg)
							}
							continue // Skip tool if conversion failed
						}

						// Add to results if no errors so far for this tool
						result.Functions = append(result.Functions, funcDef)
						result.ServerMap[mcpTool.Name] = res.server
						processedToolNames[mcpTool.Name] = res.server.Name()
					}
				}

				// Aggregate discovery errors if they occurred and no other error is set yet
				if len(discoveryErrors) > 0 && result.Error == nil {
					result.Error = fmt.Errorf("%w: %s", errToolDiscoveryFailed, strings.Join(discoveryErrors, "; "))
				}

				// If any error occurred during discovery/conversion, return result containing the error
				if result.Error != nil {
					fmt.Printf("ERROR in get_mcp_tools node: %v\n", result.Error)
					// Return the result struct containing the error, not using IntoError
					// This allows downstream nodes to see the partial results if needed,
					// although typically they should check the error first.
					return heart.Into(result)
				}

				return heart.Into(result)
			})

		// --- Node 2: First LLM Call (potentially with tools) ---
		firstCallNodeID := heart.NodeID("first_llm_call")
		firstCallNode := heart.NewNode(ctx, firstCallNodeID,
			func(callCtx heart.NewNodeContext) heart.ExecutionHandle[mcpWorkflowState] {
				// Get tools result from previous node
				toolsResultFuture := heart.FanIn(callCtx, getToolsNode)
				toolsResult, err := toolsResultFuture.Get() // Blocks until getToolsNode completes
				if err != nil {
					// Error getting the result handle itself (framework issue)
					return heart.IntoError[mcpWorkflowState](fmt.Errorf("framework error getting result for %s: %w", getToolsNodeID, err))
				}
				// Check for errors *reported by* the getToolsNode
				if toolsResult.Error != nil {
					state := mcpWorkflowState{Error: fmt.Errorf("failed dependencies for %s: %w", firstCallNodeID, toolsResult.Error)}
					return heart.Into(state) // Return state containing the error
				}

				// Initialize state
				state := mcpWorkflowState{
					OriginalRequest: originalReq,
					ModifiedRequest: originalReq,           // Start with original
					ToolServerMap:   toolsResult.ServerMap, // *** Store the map ***
					NeedsSecondCall: false,
				}

				// Add tools to the request if any were found
				if len(toolsResult.Functions) > 0 {
					state.ModifiedRequest.Tools = make([]openai.Tool, len(toolsResult.Functions))
					for i, f := range toolsResult.Functions {
						state.ModifiedRequest.Tools[i] = openai.Tool{
							Type:     openai.ToolTypeFunction,
							Function: f,
						}
					}
					// Optionally set ToolChoice if needed (e.g., "auto" or specific tool)
					// state.ModifiedRequest.ToolChoice = "auto" // Example
				}

				// Execute the wrapped LLM node definition using the modified request
				// Use callCtx.Context() for cancellation
				llmHandle := nextNodeDef.Start(heart.Into(state.ModifiedRequest))
				responseFuture := heart.FanIn(callCtx, llmHandle)
				response, err := responseFuture.Get() // Blocks until LLM call completes

				if err != nil {
					state.Error = fmt.Errorf("first LLM call failed in %s: %w", firstCallNodeID, err)
					// Return state with error, not using IntoError directly here
					return heart.Into(state)
				}
				state.FirstResponse = response

				return heart.Into(state) // Pass state containing the first response and tool map
			})

		// --- Node 3: Process Response and Invoke Tools (if necessary) ---
		processNodeID := heart.NodeID("process_and_invoke")
		processNode := heart.NewNode(ctx, processNodeID,
			func(processCtx heart.NewNodeContext) heart.ExecutionHandle[mcpWorkflowState] {
				// Get state from the first LLM call node
				stateFuture := heart.FanIn(processCtx, firstCallNode)
				state, err := stateFuture.Get() // Blocks until firstCallNode completes
				if err != nil {
					// Error getting the result handle itself (framework issue)
					return heart.IntoError[mcpWorkflowState](fmt.Errorf("framework error getting result for %s: %w", firstCallNodeID, err))
				}
				// If state itself contains an error from previous steps, propagate it
				if state.Error != nil {
					return heart.Into(state) // Pass the state containing the error
				}

				// Check for tool calls in the response
				var toolCalls []openai.ToolCall
				if len(state.FirstResponse.Choices) > 0 &&
					state.FirstResponse.Choices[0].Message.Role == openai.ChatMessageRoleAssistant && // Ensure it's assistant msg
					len(state.FirstResponse.Choices[0].Message.ToolCalls) > 0 {
					toolCalls = state.FirstResponse.Choices[0].Message.ToolCalls
				}

				if len(toolCalls) == 0 {
					// No tool calls, the first response is the final one
					state.NeedsSecondCall = false
					return heart.Into(state)
				}

				// Tool calls exist, need to invoke them
				state.NeedsSecondCall = true
				state.ToolResultMessages = make([]openai.ChatCompletionMessage, 0, len(toolCalls))

				// *** Use the ToolServerMap stored in the state ***
				if state.ToolServerMap == nil {
					// Should not happen if getToolsNode succeeded without error, but safety check
					state.Error = errors.New("internal error: ToolServerMap is nil in process_and_invoke node")
					return heart.Into(state)
				}

				// --- Invoke tools sequentially for simplicity ---
				// Could potentially parallelize with care around context and error aggregation.
				var frameworkErrors []string
				for _, toolCall := range toolCalls {
					// Use processCtx.Context() which contains the cancellable context.Context
					resultContent, invokeFrameworkErr := invokeMCPTool(processCtx.Context(), toolCall, state.ToolServerMap)

					if invokeFrameworkErr != nil {
						// This indicates a framework/setup error (e.g., tool not found, bad tool type)
						// Log it and store it to potentially fail the workflow later.
						errMsg := fmt.Sprintf("framework error invoking tool %s (ID: %s): %v", toolCall.Function.Name, toolCall.ID, invokeFrameworkErr)
						frameworkErrors = append(frameworkErrors, errMsg)
						fmt.Printf("ERROR: %s\n", errMsg)

						// Provide a generic error message back to the LLM for this specific tool call.
						resultContent = fmt.Sprintf("Error: Failed to invoke tool %s due to internal framework error.", toolCall.Function.Name)
					}

					// Create the tool result message for the *next* LLM call
					state.ToolResultMessages = append(state.ToolResultMessages, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    resultContent, // Content is the string result (or error string) from invokeMCPTool
						ToolCallID: toolCall.ID,   // Link result back to the call
						// Name field is typically not needed/used for Tool role messages anymore
					})
				}

				// If any framework errors occurred during invocation, set the state error.
				// We still proceed to the second call because we have ToolResultMessages
				// (some might contain error strings), and the LLM needs to see them.
				if len(frameworkErrors) > 0 {
					state.Error = fmt.Errorf("%w in %s: %s", errFrameworkError, processNodeID, strings.Join(frameworkErrors, "; "))
				}

				return heart.Into(state) // Return state with ToolResultMessages populated (and potentially an error)
			})

		// --- Node 4: Second LLM Call (conditional) ---
		secondCallNodeID := heart.NodeID("second_llm_call")
		secondCallNode := heart.NewNode(ctx, secondCallNodeID,
			func(callCtx heart.NewNodeContext) heart.ExecutionHandle[openai.ChatCompletionResponse] {
				// Get state from the processing node
				stateFuture := heart.FanIn(callCtx, processNode)
				state, err := stateFuture.Get() // Blocks until processNode completes
				if err != nil {
					return heart.IntoError[openai.ChatCompletionResponse](fmt.Errorf("framework error getting result for %s: %w", processNodeID, err))
				}
				// If state itself contains a framework error from previous steps, fail the workflow.
				// Tool execution errors were handled by putting error strings in ToolResultMessages.
				if state.Error != nil {
					return heart.IntoError[openai.ChatCompletionResponse](fmt.Errorf("aborting before second LLM call due to previous error in %s: %w", secondCallNodeID, state.Error))
				}

				// Decide whether to make the second call
				if !state.NeedsSecondCall {
					// No second call needed, return the first response
					return heart.Into(state.FirstResponse)
				}

				// --- Prepare request for the second call ---
				// Start with the *original* request's messages + options (Model, Temp etc remain same)
				secondReq := state.OriginalRequest

				// Build the message history for the second call:
				// 1. Original user/system messages
				// 2. First assistant response (with tool call requests)
				// 3. Tool result messages
				secondReq.Messages = make([]openai.ChatCompletionMessage, 0, len(state.OriginalRequest.Messages)+1+len(state.ToolResultMessages))
				secondReq.Messages = append(secondReq.Messages, state.OriginalRequest.Messages...)
				if len(state.FirstResponse.Choices) > 0 {
					secondReq.Messages = append(secondReq.Messages, state.FirstResponse.Choices[0].Message)
				}
				secondReq.Messages = append(secondReq.Messages, state.ToolResultMessages...)

				// Crucially, REMOVE tools definition for the second call.
				// The LLM should synthesize a final response now, not call more tools (usually).
				secondReq.Tools = nil
				secondReq.ToolChoice = nil // Reset tool choice as well

				// --- Execute the second LLM call ---
				// Use callCtx.Context() for cancellation
				llmHandle := nextNodeDef.Start(heart.Into(secondReq))
				responseFuture := heart.FanIn(callCtx, llmHandle)
				finalResponse, err := responseFuture.Get() // Blocks until second LLM call completes
				if err != nil {
					return heart.IntoError[openai.ChatCompletionResponse](fmt.Errorf("second LLM call failed in %s: %w", secondCallNodeID, err))
				}

				// Return the final response from the second call
				return heart.Into(finalResponse)
			})

		// Return the handle to the last node in the chain. Resolving this triggers the workflow.
		return secondCallNode
	}
}

// WithMCP creates a NodeDefinition that wraps an existing OpenAI ChatCompletion node
// definition, adding support for discovering and invoking tools via the Model Controller Protocol (MCP).
//
// Workflow:
//  1. Fetches tools concurrently from all provided MCPServers.
//  2. Converts MCP tool definitions to OpenAI format and builds a map for invocation.
//  3. Makes the first LLM call with the discovered tools included in the request.
//  4. If the LLM response includes tool calls:
//     a. Invokes the requested tools using the appropriate MCPServer via the stored map.
//     b. Handles tool execution errors by returning error messages as content to the LLM.
//     c. Handles framework errors (e.g., tool not found, bad JSON) - may fail the workflow.
//     d. Constructs messages containing the tool results.
//     e. Makes a second LLM call including the original messages, the first assistant response, and the tool results.
//  5. Returns the final LLM response (either from the first or second call).
//
// Parameters:
//   - nodeID: A unique identifier for this middleware workflow instance. Must be non-empty.
//   - nextNodeDef: The heart.NodeDefinition of the underlying OpenAI CreateChatCompletion node to wrap. Must be non-nil.
//   - servers: A slice of server.MCPServer instances from which to fetch and invoke tools. Can be empty (no tools added).
//
// Returns:
//
//	A heart.NodeDefinition that behaves like an OpenAI ChatCompletion node but includes
//	the MCP tool interaction logic. Its input is openai.ChatCompletionRequest and
//	output is openai.ChatCompletionResponse.
func WithMCP(
	nodeID heart.NodeID,
	nextNodeDef heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
	servers []server.MCPServer,
) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	if nodeID == "" {
		panic("WithMCP requires a non-empty nodeID")
	}
	if nextNodeDef == nil {
		panic("WithMCP requires a non-nil nextNodeDef")
	}
	// servers slice can be nil or empty.

	// Generate the handler function, capturing the nextNodeDef and servers.
	handler := mcpWorkflowHandler(nextNodeDef, servers)

	// Create a workflow resolver using the user-provided nodeID for this specific instance.
	workflowResolver := heart.NewWorkflowResolver(nodeID, handler)

	// Define and return the NodeDefinition for this workflow.
	return heart.DefineNode(
		nodeID,           // Use the user-provided nodeID for the definition instance
		workflowResolver, // The resolver containing the workflow logic
	)
}
