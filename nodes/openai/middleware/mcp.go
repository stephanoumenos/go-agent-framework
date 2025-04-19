// ./nodes/openai/middleware/mcp.go
package middleware

import (
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai/internal"

	// Added for potential use in future logging/formatting if needed
	openai "github.com/sashabaranov/go-openai"
	// MCP client interface is implicitly required via DI for internal nodes
)

var (
	errMaxIterationsReached = errors.New("maximum tool invocation iterations reached")
)

const (
	// maxToolIterations limits the number of LLM <-> Tool rounds to prevent infinite loops.
	// Reduced to 5 from default 10 for potentially faster debugging if it occurs.
	maxToolIterations = 5
)

// mcpWorkflowHandler defines the core logic for the WithMCP middleware using heart nodes,
// supporting multiple rounds of tool calls within a loop, mirroring openai-agent-sdk default behavior.
// It orchestrates the flow: List Tools -> (Loop: LLM Call -> Process Response -> Maybe Call Tools -> Update History).
func mcpWorkflowHandler(
	nextNodeDef heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse], // The wrapped LLM call node definition
	listToolsNodeDef heart.NodeDefinition[struct{}, internal.ListToolsResult], // Definition for listing tools
	callToolNodeDef heart.NodeDefinition[internal.CallToolInput, internal.CallToolOutput], // Definition for calling a tool
) heart.WorkflowHandlerFunc[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	// This is the function that will be executed declaratively when the workflow node runs.
	return func(ctx heart.Context, originalReq openai.ChatCompletionRequest) heart.ExecutionHandle[openai.ChatCompletionResponse] {

		// --- Step 1: Start Listing Available MCP Tools (outside the loop) ---
		listToolsHandle := listToolsNodeDef.Start(heart.Into(struct{}{}))

		// --- Step 2: Define the Main Tool Interaction Loop Node ---
		loopHandle := heart.NewNode(ctx, heart.NodeID("mcp_tool_loop"),
			func(loopCtx heart.NewNodeContext) heart.ExecutionHandle[openai.ChatCompletionResponse] {

				// --- Get Tool List Result (needed for potentially every LLM call & all tool calls) ---
				toolsFuture := heart.FanIn(loopCtx, listToolsHandle)
				toolsResult, toolsErr := toolsFuture.Get() // Wait for listToolsHandle to complete

				// Handle potential errors from the heart framework executing listToolsHandle
				if toolsErr != nil {
					err := fmt.Errorf("failed to get MCP tool list result: %w", toolsErr)
					return heart.IntoError[openai.ChatCompletionResponse](err)
				}
				// Handle errors reported *by* the listTools node's internal logic
				if toolsResult.Error != nil {
					err := fmt.Errorf("error listing MCP tools: %w", toolsResult.Error)
					return heart.IntoError[openai.ChatCompletionResponse](err)
				}
				// Check if the necessary MCPToolsResult map exists if tools were found
				mcpToolsAvailable := len(toolsResult.OpenAITools) > 0
				if mcpToolsAvailable && toolsResult.MCPToolsResult == nil {
					err := errors.New("internal error: MCPToolsResult map is nil after successful ListTools execution which found tools")
					return heart.IntoError[openai.ChatCompletionResponse](err)
				}

				// --- Loop Initialization ---
				currentMessages := originalReq.Messages        // Start with the initial message history
				var lastResponse openai.ChatCompletionResponse // To store the final response
				toolsHaveBeenCalled := false                   // Track if tools have been invoked in this run

				// --- The Tool Interaction Loop ---
				for iter := 0; iter < maxToolIterations; iter++ {
					// --- Prepare LLM Request for this iteration ---
					iterReq := originalReq // Start with original request settings (model, temp, etc.)
					// Create a fresh slice for messages in this request
					iterReq.Messages = make([]openai.ChatCompletionMessage, len(currentMessages))
					copy(iterReq.Messages, currentMessages) // Use the current message history

					// --- Tool List Handling: Always include available tools ---
					// Start with tools from the original request, if any.
					iterReq.Tools = nil // Reset for this iteration before appending
					if len(originalReq.Tools) > 0 {
						iterReq.Tools = append(iterReq.Tools, originalReq.Tools...)
					}
					// Append discovered MCP tools if they exist.
					if mcpToolsAvailable {
						iterReq.Tools = append(iterReq.Tools, toolsResult.OpenAITools...)
					}

					// --- ToolChoice Handling ---
					// Determine ToolChoice based on whether tools were called previously.
					// Keep the logic simple: first call uses original/auto, subsequent calls use nil/auto.
					if toolsHaveBeenCalled {
						// If tools were invoked previously, let the model decide ("auto" mode) based on the tool results.
						// Setting to nil typically defaults to "auto" in the API.
						iterReq.ToolChoice = nil
					} else {
						// First iteration or tools not called yet. Determine initial ToolChoice.
						toolChoiceIsSpecific := false
						toolChoiceStr, isString := originalReq.ToolChoice.(string)
						if isString {
							if toolChoiceStr != "none" && toolChoiceStr != "" {
								toolChoiceIsSpecific = true
							}
						} else if tc, ok := originalReq.ToolChoice.(*openai.ToolChoice); ok && tc != nil {
							toolChoiceIsSpecific = true
						}

						if toolChoiceIsSpecific {
							iterReq.ToolChoice = originalReq.ToolChoice
						} else if len(iterReq.Tools) > 0 {
							// Tools are available, and original choice wasn't specific, default to "auto".
							iterReq.ToolChoice = "auto"
						} else {
							// No tools and no specific choice.
							iterReq.ToolChoice = nil
						}
					}

					// Final check: Ensure ToolChoice is nil if no tools ended up in the request.
					if len(iterReq.Tools) == 0 {
						if iterReq.ToolChoice != nil {
							iterReq.ToolChoice = nil
						}
					}
					// --- End ToolChoice Handling ---

					// --- Execute LLM Call for this iteration ---
					llmHandle := nextNodeDef.Start(heart.Into(iterReq)) // Use loopCtx implicitly
					llmFuture := heart.FanIn(loopCtx, llmHandle)
					llmResponse, llmErr := llmFuture.Get() // Wait for this LLM call

					if llmErr != nil {
						// Immediately wrap and return this framework error
						err := fmt.Errorf("LLM call node failed on iteration %d within MCP loop: %w", iter, llmErr)
						return heart.IntoError[openai.ChatCompletionResponse](err)
					}

					// Log details from the received response
					if len(llmResponse.Choices) == 0 {
						// This case indicates an issue with the LLM response format or an empty response
						// Potentially return an error here, as processing cannot continue normally.
						err := fmt.Errorf("LLM response contained no choices on iteration %d", iter)
						return heart.IntoError[openai.ChatCompletionResponse](err)
					}
					// --- **** END ADDED DEBUG LOGGING **** ---

					// Store the received response
					lastResponse = llmResponse

					// --- Check if Tool Calls are present in the response ---
					toolCalls := []openai.ToolCall{} // Initialize empty slice
					// Use the already checked choice from logging block if possible, otherwise re-check
					if len(llmResponse.Choices) > 0 && llmResponse.Choices[0].Message.ToolCalls != nil {
						toolCalls = llmResponse.Choices[0].Message.ToolCalls
					}

					// --- Exit Condition: No Tool Calls Requested ---
					if len(toolCalls) == 0 {
						// The LLM did not request any tools, this is the final answer.
						return heart.Into(lastResponse)
					}

					// --- Tool Calls Exist: Process and Invoke ---

					// 1. Add the assistant's message (requesting tools) to the history
					// Ensure we only add if choices exist (already checked, but good practice)
					if len(llmResponse.Choices) > 0 {
						// Make a copy of the message to avoid potential side effects if the original response object is mutated elsewhere
						assistantMsg := llmResponse.Choices[0].Message
						currentMessages = append(currentMessages, assistantMsg)
					}

					// --- Invoke Tools in Parallel ---
					toolCallFutures := make([]*heart.Future[internal.CallToolOutput], len(toolCalls))
					for i, toolCall := range toolCalls {
						// Basic validation of the tool call structure
						if toolCall.Type != openai.ToolTypeFunction || toolCall.Function.Name == "" {
							errMsg := fmt.Sprintf("invalid tool call from LLM on iteration %d: type=%s, id=%s, function_name=%s", iter, toolCall.Type, toolCall.ID, toolCall.Function.Name)
							// Create an error handle for this specific failed call
							errorHandle := heart.IntoError[internal.CallToolOutput](errors.New(errMsg))
							toolCallFutures[i] = heart.FanIn(loopCtx, errorHandle)
							continue // Skip starting the actual tool call node
						}

						// Ensure MCP tools were listed if needed (should be guaranteed by mcpToolsAvailable check earlier)
						if !mcpToolsAvailable && len(originalReq.Tools) == 0 { // Check if *any* tools were expected
							errMsg := fmt.Sprintf("LLM requested MCP tool '%s' but no tools were initially listed or provided", toolCall.Function.Name)
							errorHandle := heart.IntoError[internal.CallToolOutput](errors.New(errMsg))
							toolCallFutures[i] = heart.FanIn(loopCtx, errorHandle)
							continue
						}

						// Prepare input for the callToolNode
						callInput := internal.CallToolInput{
							ToolCall:       toolCall,
							MCPToolsResult: toolsResult.MCPToolsResult, // Pass the map from listTools
						}
						// Start the node to execute this specific tool call
						callHandle := callToolNodeDef.Start(heart.Into(callInput))
						toolCallFutures[i] = heart.FanIn(loopCtx, callHandle)
					}

					// --- Collect Tool Results ---
					toolMessages := make([]openai.ChatCompletionMessage, 0, len(toolCalls))
					for i, future := range toolCallFutures {
						callOutput, callErr := future.Get()        // Wait for the i-th tool call node
						toolCallID := toolCalls[i].ID              // Get the original Tool Call ID
						toolCallName := toolCalls[i].Function.Name // Get the original Tool Call Name

						// Handle framework errors from the callToolNode itself
						if callErr != nil {
							toolMessages = append(toolMessages, openai.ChatCompletionMessage{
								Role:       openai.ChatMessageRoleTool,
								Content:    fmt.Sprintf("Error: Failed to get result for tool %s due to internal framework error.", toolCallName),
								ToolCallID: toolCallID,
							})
							continue // Continue collecting other results
						}

						// Handle errors reported *by* the callToolNode's internal logic (e.g., DI failure)
						// or errors formatted *from* the tool execution itself via formatToolResult
						if callOutput.Error != nil {
							// This 'Error' field in CallToolOutput is primarily for framework/DI errors within the node.
							// Tool execution errors should ideally be in ResultMsg.Content already.
							errorContent := fmt.Sprintf("Error processing tool %s: %s", toolCallName, callOutput.Error.Error())
							toolMessages = append(toolMessages, openai.ChatCompletionMessage{
								Role:       openai.ChatMessageRoleTool,
								Content:    errorContent,
								ToolCallID: toolCallID,
								Name:       toolCallName, // Name might be useful for LLM if content indicates an error
							})
							continue
						}

						// Success: Append the result message from CallToolOutput
						// Ensure the ToolCallID matches (should always if generated correctly)
						if callOutput.ResultMsg.ToolCallID == "" {
							callOutput.ResultMsg.ToolCallID = toolCallID
						} else if callOutput.ResultMsg.ToolCallID != toolCallID {
							// Correct it to match the request
							callOutput.ResultMsg.ToolCallID = toolCallID
						}
						// Ensure Role is Tool
						callOutput.ResultMsg.Role = openai.ChatMessageRoleTool
						// Add Name field if not already present (often helpful for LLM)
						if callOutput.ResultMsg.Name == "" {
							callOutput.ResultMsg.Name = toolCallName
						}
						toolMessages = append(toolMessages, callOutput.ResultMsg)
					}

					// 2. Add the collected tool results/errors to the message history
					currentMessages = append(currentMessages, toolMessages...)

					// 3. Mark that tools have now been called in this run
					// This might affect ToolChoice logic in the *next* iteration if implemented that way.
					toolsHaveBeenCalled = true

					// Loop continues...

				} // --- End of for loop ---

				// --- Exit Condition: Max Iterations Reached ---
				// Return the error indicating max iterations were hit
				return heart.IntoError[openai.ChatCompletionResponse](errMaxIterationsReached)

			}) // End NewNode function definition

		// Return the handle for the loop node.
		return loopHandle
	}
}

// WithMCP function creates the middleware node definition.
func WithMCP(
	nodeID heart.NodeID,
	nextNodeDef heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	if nodeID == "" {
		panic("WithMCP requires a non-empty nodeID")
	}
	if nextNodeDef == nil {
		panic("WithMCP requires a non-nil nextNodeDef")
	}

	// Define the internal node *blueprints* needed by the workflow handler ONCE.
	// Use specific IDs based on the middleware instance's nodeID to avoid conflicts
	// if the same middleware is used multiple times in a workflow.
	listToolsNodeDef := internal.DefineListToolsNode(heart.NodeID(fmt.Sprintf("%s_listTools", nodeID)))
	callToolNodeDef := internal.DefineCallToolNode(heart.NodeID(fmt.Sprintf("%s_callTool", nodeID)))

	// Create the specific workflow handler function, capturing the necessary node definitions.
	handler := mcpWorkflowHandler(nextNodeDef, listToolsNodeDef, callToolNodeDef)

	// Create and return the workflow definition for this middleware instance.
	return heart.WorkflowFromFunc(nodeID, handler)
}
