// ./nodes/openai/middleware/mcp.go
package middleware

import (
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai/internal"

	openai "github.com/sashabaranov/go-openai"
	// MCP client interface is implicitly required via DI for internal nodes
)

var (
	errMaxIterationsReached = errors.New("maximum tool invocation iterations reached")
)

const (
	// maxToolIterations limits the number of LLM <-> Tool rounds to prevent infinite loops.
	maxToolIterations = 10
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
					iterReq.Messages = make([]openai.ChatCompletionMessage, len(currentMessages))
					copy(iterReq.Messages, currentMessages) // Use the current message history (important: use a copy)

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

					// --- ToolChoice Handling (Conditional Reset) ---
					if toolsHaveBeenCalled {
						// If tools were invoked previously in this run, reset ToolChoice to nil.
						// This allows the model to decide ("auto" mode) based on the tool results.
						iterReq.ToolChoice = nil
					} else {
						// Tools have not been called yet in this run. Apply original/derived logic.
						// Check if the original ToolChoice was specific.
						toolChoiceIsSpecific := false
						toolChoiceStr, isString := originalReq.ToolChoice.(string)
						if isString {
							// Treat "auto", "required", or other valid/invalid strings as specific intent already set by user.
							if toolChoiceStr != "none" && toolChoiceStr != "" {
								toolChoiceIsSpecific = true
							}
						} else if originalReq.ToolChoice != nil {
							// If it's not a string but also not nil, assume it's the specific *openai.ToolChoice struct.
							toolChoiceIsSpecific = true
						}

						if mcpToolsAvailable && !toolChoiceIsSpecific {
							// If MCP tools were added AND the original choice wasn't specific (nil or "none"),
							// default to "auto".
							iterReq.ToolChoice = "auto"
						} else {
							// Otherwise, retain the original tool choice (which might be nil, "none", "auto", "required", or specific).
							iterReq.ToolChoice = originalReq.ToolChoice
						}
					}

					// Ensure ToolChoice is nil if no tools are actually available.
					if len(iterReq.Tools) == 0 {
						iterReq.ToolChoice = nil
					}

					// --- Execute LLM Call for this iteration ---
					llmHandle := nextNodeDef.Start(heart.Into(iterReq)) // Use loopCtx implicitly
					llmFuture := heart.FanIn(loopCtx, llmHandle)
					llmResponse, llmErr := llmFuture.Get() // Wait for this LLM call

					if llmErr != nil {
						err := fmt.Errorf("LLM call failed on iteration %d within MCP loop: %w", iter, llmErr)
						return heart.IntoError[openai.ChatCompletionResponse](err)
					}

					lastResponse = llmResponse // Store this response

					// --- Check if Tool Calls are present in the response ---
					toolCalls := []openai.ToolCall{} // Initialize empty slice
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
					if len(llmResponse.Choices) > 0 { // Safety check
						currentMessages = append(currentMessages, llmResponse.Choices[0].Message)
					}

					// --- Invoke Tools in Parallel --- (Using the *same* listTools result from before the loop)
					// [Existing tool invocation logic remains the same]
					toolCallFutures := make([]*heart.Future[internal.CallToolOutput], len(toolCalls))
					for i, toolCall := range toolCalls {
						if toolCall.Type != openai.ToolTypeFunction || toolCall.Function.Name == "" {
							err := fmt.Errorf("invalid tool call from LLM on iteration %d: type=%s, id=%s, function_name=%s", iter, toolCall.Type, toolCall.ID, toolCall.Function.Name)
							errorHandle := heart.IntoError[internal.CallToolOutput](err)
							toolCallFutures[i] = heart.FanIn(loopCtx, errorHandle)
							continue
						}
						// We still need MCPToolsResult map, guaranteed to exist if mcpToolsAvailable was true
						if !mcpToolsAvailable {
							// Should not happen if toolCalls were generated based on MCP tools, but defensive check.
							err := fmt.Errorf("LLM requested MCP tool '%s' but no MCP tools were initially listed", toolCall.Function.Name)
							errorHandle := heart.IntoError[internal.CallToolOutput](err)
							toolCallFutures[i] = heart.FanIn(loopCtx, errorHandle)
							continue
						}
						callInput := internal.CallToolInput{
							ToolCall:       toolCall,
							MCPToolsResult: toolsResult.MCPToolsResult,
						}
						callHandle := callToolNodeDef.Start(heart.Into(callInput))
						toolCallFutures[i] = heart.FanIn(loopCtx, callHandle)
					}

					// --- Collect Tool Results ---
					// [Existing tool result collection logic remains the same]
					toolMessages := make([]openai.ChatCompletionMessage, 0, len(toolCalls))
					for i, future := range toolCallFutures {
						callOutput, callErr := future.Get()        // Wait for the i-th tool call
						toolCallID := toolCalls[i].ID              // Original Tool Call ID
						toolCallName := toolCalls[i].Function.Name // Original Tool Call Name

						if callErr != nil {
							toolMessages = append(toolMessages, openai.ChatCompletionMessage{
								Role:       openai.ChatMessageRoleTool,
								Content:    fmt.Sprintf("Error: Failed to invoke tool %s due to internal framework/node error.", toolCallName),
								ToolCallID: toolCallID,
							})
							continue
						}
						if callOutput.Error != nil {
							errorContent := fmt.Sprintf("Error executing tool %s: %s", toolCallName, callOutput.Error.Error())
							toolMessages = append(toolMessages, openai.ChatCompletionMessage{
								Role:       openai.ChatMessageRoleTool,
								Content:    errorContent,
								ToolCallID: toolCallID,
								Name:       toolCallName,
							})
							continue
						}
						// Success: Append the result message
						if callOutput.ResultMsg.ToolCallID != toolCallID {
							fmt.Printf("WARN: ToolCallID mismatch in CallToolOutput for tool '%s' on iter %d. Expected '%s', got '%s'\n", toolCallName, iter, toolCallID, callOutput.ResultMsg.ToolCallID)
							callOutput.ResultMsg.ToolCallID = toolCallID // Correct it
						}
						callOutput.ResultMsg.Role = openai.ChatMessageRoleTool // Ensure role
						toolMessages = append(toolMessages, callOutput.ResultMsg)
					}

					// 2. Add the tool results/errors to the message history
					currentMessages = append(currentMessages, toolMessages...)

					// 3. *** Mark that tools have now been called in this run ***
					// This will affect ToolChoice in the *next* iteration.
					toolsHaveBeenCalled = true

					// 4. Log invocation errors if any, but continue the loop
					// if len(invocationErrors) > 0 {
					// 	fmt.Printf("WARN: MCP middleware encountered errors invoking tools on iteration %d for node '%s': %s\n",
					// 		iter, loopCtx.BasePath, strings.Join(invocationErrors, "; "))
					// }

					// Loop continues...

				} // --- End of for loop ---

				// --- Exit Condition: Max Iterations Reached ---
				return heart.IntoError[openai.ChatCompletionResponse](errMaxIterationsReached) // Return the last thing the LLM said

			}) // End NewNode function definition

		// Return the handle for the loop node.
		return loopHandle
	}
}

// WithMCP function remains the same.
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
	listToolsNodeDef := internal.DefineListToolsNode(heart.NodeID(string(nodeID) + "_listTools"))
	callToolNodeDef := internal.DefineCallToolNode(heart.NodeID(string(nodeID) + "_callTool"))

	// Create the specific workflow handler function, capturing the necessary node definitions.
	handler := mcpWorkflowHandler(nextNodeDef, listToolsNodeDef, callToolNodeDef)

	// Create and return the workflow definition for this middleware instance.
	return heart.WorkflowFromFunc(nodeID, handler)
}
