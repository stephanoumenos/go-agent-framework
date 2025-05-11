// ./nodes/openai/middleware/mcp.go
package middleware

import (
	"errors"
	"fmt"

	"go-agent-framework/nodes/openai/internal"

	gaf "go-agent-framework"

	openai "github.com/sashabaranov/go-openai"
	// MCP client interface is implicitly required via DI for internal nodes
)

// mcpWorkflowID is the NodeID used for the node definition created by WithMCP.
const mcpWorkflowID gaf.NodeID = "openai_mcp"

var (
	// errMaxIterationsReached indicates the tool interaction loop exceeded maxToolIterations.
	errMaxIterationsReached = errors.New("maximum tool invocation iterations reached")
	// errMCPNilNextNodeDef indicates WithMCP was called with a nil nextNodeDef.
	errMCPNilNextNodeDef = errors.New("MCP requires a non-nil nextNodeDef")
)

const (
	// maxToolIterations limits the number of LLM <-> Tool rounds to prevent infinite loops.
	maxToolIterations = 10
)

// mcpWorkflowHandler defines the core WorkflowHandlerFunc for the WithMCP middleware.
// It orchestrates a multi-step interaction loop to handle tool calls using an MCP-compatible client:
//  1. List available tools using the internal ListTools node.
//  2. Start a loop (up to maxToolIterations):
//     a. Prepare the request for the wrapped LLM node (`nextNodeDef`), including available tools and appropriate ToolChoice.
//     b. Execute the LLM node.
//     c. Process the LLM response.
//     d. If no tool calls are requested, return the response and exit the loop.
//     e. If tool calls are requested:
//     i. Add the assistant's message (requesting tools) to the history.
//     ii. Execute the requested tool calls in parallel using the internal CallTool node.
//     iii. Collect the tool results (or errors).
//     iv. Add the tool result messages to the history.
//     v. Continue the loop.
//  3. If the loop finishes due to reaching maxToolIterations, return errMaxIterationsReached.
func mcpWorkflowHandler(
	nextNodeDef gaf.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse], // The wrapped LLM call node definition
) gaf.WorkflowHandlerFunc[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	// Define the internal node blueprints needed by the workflow handler ONCE.
	// These definitions are used within the handler function below.
	listToolsNodeDef := internal.ListTools()
	callToolNodeDef := internal.CallTool()

	// This is the function that will be executed declaratively when the workflow node runs.
	return func(ctx gaf.Context, originalReq openai.ChatCompletionRequest) gaf.ExecutionHandle[openai.ChatCompletionResponse] {
		// Use NewNode to encapsulate the loop logic.
		loopHandle := gaf.NewNode(ctx, gaf.NodeID("mcp_tool_loop"),
			func(loopCtx gaf.NewNodeContext) gaf.ExecutionHandle[openai.ChatCompletionResponse] {
				if nextNodeDef == nil {
					// Handle the case where the wrapped node definition is nil.
					err := fmt.Errorf("middleware node '%s': %w", loopCtx.BasePath, errMCPNilNextNodeDef)
					return gaf.IntoError[openai.ChatCompletionResponse](err)
				}

				// --- List Available Tools ---
				// Start the ListTools node. Input is ignored (empty struct).
				listToolsHandle := listToolsNodeDef.Start(gaf.Into(struct{}{}))

				// --- Get Tool List Result ---
				// Use FanIn to wait for the listToolsHandle to complete.
				toolsFuture := gaf.FanIn(loopCtx, listToolsHandle)
				toolsResult, toolsErr := toolsFuture.Get()

				// Handle potential errors from the gaf framework executing listToolsHandle.
				if toolsErr != nil {
					err := fmt.Errorf("middleware node '%s': failed to get MCP tool list result: %w", loopCtx.BasePath, toolsErr)
					return gaf.IntoError[openai.ChatCompletionResponse](err)
				}
				// Handle errors reported *by* the listTools node's internal logic.
				if toolsResult.Error != nil {
					err := fmt.Errorf("middleware node '%s': error listing MCP tools: %w", loopCtx.BasePath, toolsResult.Error)
					return gaf.IntoError[openai.ChatCompletionResponse](err)
				}
				// Check if the necessary MCPToolsResult map exists if tools were found.
				mcpToolsAvailable := len(toolsResult.OpenAITools) > 0
				if mcpToolsAvailable && toolsResult.MCPToolsResult == nil {
					err := fmt.Errorf("internal error in middleware node '%s': MCPToolsResult map is nil after successful ListTools execution which found tools", loopCtx.BasePath)
					return gaf.IntoError[openai.ChatCompletionResponse](err)
				}

				// --- Loop Initialization ---
				currentMessages := originalReq.Messages        // Start with the initial message history.
				var lastResponse openai.ChatCompletionResponse // Stores the most recent LLM response.
				toolsHaveBeenCalled := false                   // Tracks if tools have been invoked in this run.

				// --- The Tool Interaction Loop ---
				for iter := 0; iter < maxToolIterations; iter++ {
					// --- Prepare LLM Request for this iteration ---
					iterReq := originalReq // Start with original request settings (model, temp, etc.).
					// Create a fresh slice for messages in this request.
					iterReq.Messages = make([]openai.ChatCompletionMessage, len(currentMessages))
					copy(iterReq.Messages, currentMessages) // Use the current message history.

					// --- Tool List Handling: Always include available tools ---
					// Start with tools from the original request, if any.
					iterReq.Tools = nil // Reset for this iteration before appending.
					if len(originalReq.Tools) > 0 {
						iterReq.Tools = append(iterReq.Tools, originalReq.Tools...)
					}
					// Append discovered MCP tools if they exist.
					if mcpToolsAvailable {
						iterReq.Tools = append(iterReq.Tools, toolsResult.OpenAITools...)
					}

					// --- ToolChoice Handling ---
					// Determine ToolChoice based on whether tools were called previously.
					if toolsHaveBeenCalled {
						// If tools were invoked previously, let the model decide ("auto" mode) based on the tool results.
						// Setting to nil typically defaults to "auto" in the API.
						iterReq.ToolChoice = nil
					} else {
						// First iteration or tools not called yet. Determine initial ToolChoice.
						toolChoiceIsSpecific := false
						toolChoiceStr, isString := originalReq.ToolChoice.(string)
						if isString {
							// "none", "auto", "", or specific tool name string.
							if toolChoiceStr != "none" && toolChoiceStr != "auto" && toolChoiceStr != "" {
								toolChoiceIsSpecific = true // It's a specific tool name.
							}
						} else if tc, ok := originalReq.ToolChoice.(*openai.ToolChoice); ok && tc != nil {
							toolChoiceIsSpecific = true // It's a specific *openai.ToolChoice struct.
						}

						if toolChoiceIsSpecific {
							// If the original request forced a specific tool or "none", respect it on the first call.
							iterReq.ToolChoice = originalReq.ToolChoice
						} else if len(iterReq.Tools) > 0 {
							// Tools are available, and original choice wasn't specific (or was "auto"), default to "auto".
							iterReq.ToolChoice = "auto"
						} else {
							// No tools and no specific choice. ToolChoice must be nil.
							iterReq.ToolChoice = nil
						}
					}

					// Final check: Ensure ToolChoice is nil if no tools ended up in the request.
					if len(iterReq.Tools) == 0 {
						iterReq.ToolChoice = nil
					}
					// --- End ToolChoice Handling ---

					// --- Execute LLM Call for this iteration ---
					// Use the loop's context implicitly when starting nodes.
					llmHandle := nextNodeDef.Start(gaf.Into(iterReq))
					llmFuture := gaf.FanIn(loopCtx, llmHandle)
					llmResponse, llmErr := llmFuture.Get() // Wait for this LLM call.

					if llmErr != nil {
						// Immediately wrap and return this framework or node error.
						err := fmt.Errorf("LLM call node failed on iteration %d within MCP loop ('%s'): %w", iter, loopCtx.BasePath, llmErr)
						return gaf.IntoError[openai.ChatCompletionResponse](err)
					}

					// Store the received response.
					lastResponse = llmResponse

					// Check response validity.
					if len(llmResponse.Choices) == 0 {
						err := fmt.Errorf("LLM response contained no choices on iteration %d within MCP loop ('%s')", iter, loopCtx.BasePath)
						return gaf.IntoError[openai.ChatCompletionResponse](err)
					}

					// --- Check if Tool Calls are present in the response ---
					toolCalls := llmResponse.Choices[0].Message.ToolCalls

					// --- Exit Condition: No Tool Calls Requested ---
					if len(toolCalls) == 0 {
						// The LLM did not request any tools, this is the final answer.
						return gaf.Into(lastResponse)
					}

					// --- Tool Calls Exist: Process and Invoke ---

					// 1. Add the assistant's message (requesting tools) to the history.
					// Make a copy to avoid potential side effects if the original response object is mutated elsewhere.
					assistantMsg := llmResponse.Choices[0].Message
					currentMessages = append(currentMessages, assistantMsg)

					// --- Invoke Tools in Parallel ---
					toolCallFutures := make([]*gaf.Future[internal.CallToolOutput], len(toolCalls))
					for i, toolCall := range toolCalls {
						// Basic validation of the tool call structure received from the LLM.
						if toolCall.Type != openai.ToolTypeFunction || toolCall.Function.Name == "" {
							errMsg := fmt.Sprintf("invalid tool call from LLM on iteration %d: type=%s, id=%s, function_name=%s", iter, toolCall.Type, toolCall.ID, toolCall.Function.Name)
							// Create an error handle for this specific failed call.
							errorHandle := gaf.IntoError[internal.CallToolOutput](errors.New(errMsg))
							// Wrap it in a future for consistent collection logic.
							toolCallFutures[i] = gaf.FanIn(loopCtx, errorHandle)
							continue // Skip starting the actual tool call node.
						}

						// Ensure MCP tools were listed if needed (should be guaranteed by mcpToolsAvailable check earlier).
						if !mcpToolsAvailable && len(originalReq.Tools) == 0 { // Check if *any* tools were expected.
							errMsg := fmt.Sprintf("LLM requested tool '%s' but no tools were listed or provided initially in MCP loop ('%s')", toolCall.Function.Name, loopCtx.BasePath)
							errorHandle := gaf.IntoError[internal.CallToolOutput](errors.New(errMsg))
							toolCallFutures[i] = gaf.FanIn(loopCtx, errorHandle)
							continue
						}

						// Prepare input for the CallTool node.
						callInput := internal.CallToolInput{
							ToolCall:       toolCall,
							MCPToolsResult: toolsResult.MCPToolsResult, // Pass the map from listTools.
						}
						// Start the node to execute this specific tool call.
						callHandle := callToolNodeDef.Start(gaf.Into(callInput))
						// Collect the future for the result.
						toolCallFutures[i] = gaf.FanIn(loopCtx, callHandle)
					}

					// --- Collect Tool Results ---
					toolMessages := make([]openai.ChatCompletionMessage, 0, len(toolCalls))
					for i, future := range toolCallFutures {
						callOutput, callErr := future.Get() // Wait for the i-th tool call node.
						toolCallID := toolCalls[i].ID       // Get the original Tool Call ID.
						toolCallName := toolCalls[i].Function.Name

						// Handle framework errors from the callToolNode itself.
						if callErr != nil {
							toolMessages = append(toolMessages, openai.ChatCompletionMessage{
								Role:       openai.ChatMessageRoleTool,
								Content:    fmt.Sprintf("Error: Failed to get result for tool %s due to internal framework error: %v", toolCallName, callErr),
								ToolCallID: toolCallID,
							})
							continue // Continue collecting other results.
						}

						// Handle errors reported *by* the callToolNode's internal logic (e.g., DI failure).
						// Also includes errors formatted *from* the tool execution itself via formatToolResult.
						if callOutput.Error != nil {
							// This 'Error' field in CallToolOutput is primarily for framework/DI errors within the node.
							// Tool execution errors should ideally be in ResultMsg.Content already.
							errorContent := fmt.Sprintf("Error processing tool %s: %s", toolCallName, callOutput.Error.Error())
							toolMessages = append(toolMessages, openai.ChatCompletionMessage{
								Role:       openai.ChatMessageRoleTool,
								Content:    errorContent,
								ToolCallID: toolCallID,
								Name:       toolCallName, // Name might be useful for LLM if content indicates an error.
							})
							continue
						}

						// Success: Append the result message from CallToolOutput.
						// Ensure the ToolCallID matches the request ID.
						if callOutput.ResultMsg.ToolCallID == "" || callOutput.ResultMsg.ToolCallID != toolCallID {
							callOutput.ResultMsg.ToolCallID = toolCallID // Correct or set the ID.
						}
						// Ensure Role is Tool.
						callOutput.ResultMsg.Role = openai.ChatMessageRoleTool
						// Add Name field if not already present (often helpful for LLM).
						if callOutput.ResultMsg.Name == "" {
							callOutput.ResultMsg.Name = toolCallName
						}
						toolMessages = append(toolMessages, callOutput.ResultMsg)
					}

					// 2. Add the collected tool results/errors to the message history.
					currentMessages = append(currentMessages, toolMessages...)

					// 3. Mark that tools have now been called in this run.
					toolsHaveBeenCalled = true

					// Loop continues...

				} // --- End of for loop ---

				// --- Exit Condition: Max Iterations Reached ---
				// If the loop finished because max iterations were hit, return an error.
				err := fmt.Errorf("middleware node '%s': %w (limit: %d)", loopCtx.BasePath, errMaxIterationsReached, maxToolIterations)
				return gaf.IntoError[openai.ChatCompletionResponse](err)
			}) // End NewNode function definition

		// Return the handle for the loop node. Execution starts when this handle is resolved.
		return loopHandle
	}
}

// WithMCP creates a NodeDefinition that wraps another `NodeDefinition` (typically an
// OpenAI chat completion node) to add support for MCP (Multi-Capability Protocol) tool calls.
//
// The middleware intercepts the request and response flow to:
//   - List available tools from an MCP-compatible client (injected via `openai.InjectMCPClient`).
//   - Include the tool definitions in the request to the LLM.
//   - Handle tool call requests from the LLM by invoking the appropriate tools via the MCP client.
//   - Format tool results and include them in the message history for subsequent LLM calls.
//   - Manage the interaction loop between the LLM and tools.
//
// The resulting NodeDefinition takes an `openai.ChatCompletionRequest` as input
// and produces the final `openai.ChatCompletionResponse` after the tool interaction loop
// completes (either with a final text response or an error).
//
// Parameters:
//   - nextNodeDef: The NodeDefinition of the LLM call node to wrap. It must take
//     `openai.ChatCompletionRequest` as input and produce `openai.ChatCompletionResponse`.
//
// Returns:
//   - A WorkflowDefinition that encapsulates the MCP tool handling logic.
func WithMCP(
	nextNodeDef gaf.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
) gaf.WorkflowDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	// Create the specific workflow handler function, capturing the necessary node definitions.
	handler := mcpWorkflowHandler(nextNodeDef)

	// Create and return the workflow definition for this middleware instance.
	return gaf.WorkflowFromFunc(mcpWorkflowID, handler)
}
