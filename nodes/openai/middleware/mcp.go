// ./nodes/openai/middleware/mcp.go
package middleware

import (
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai/internal"
	"strings" // Added for potential use in future logging/formatting if needed

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
					fmt.Printf("--- MCP Loop Iteration %d Start ---\n", iter) // Mark iteration start

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
						fmt.Printf("INFO: [MCP Loop Iter %d] Tools previously called, setting ToolChoice=nil (defaulting to auto).\n", iter)
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
							fmt.Printf("INFO: [MCP Loop Iter %d] Using original specific ToolChoice: %+v\n", iter, originalReq.ToolChoice)
							iterReq.ToolChoice = originalReq.ToolChoice
						} else if len(iterReq.Tools) > 0 {
							// Tools are available, and original choice wasn't specific, default to "auto".
							fmt.Printf("INFO: [MCP Loop Iter %d] Tools available, ToolChoice not specific, defaulting to 'auto'.\n", iter)
							iterReq.ToolChoice = "auto"
						} else {
							// No tools and no specific choice.
							fmt.Printf("INFO: [MCP Loop Iter %d] No tools available and ToolChoice not specific, setting to nil.\n", iter)
							iterReq.ToolChoice = nil
						}
					}

					// Final check: Ensure ToolChoice is nil if no tools ended up in the request.
					if len(iterReq.Tools) == 0 {
						if iterReq.ToolChoice != nil {
							fmt.Printf("WARN: [MCP Loop Iter %d] Overriding ToolChoice to nil because no tools are available.\n", iter)
							iterReq.ToolChoice = nil
						}
					}
					// --- End ToolChoice Handling ---

					// Log the messages being sent for this iteration
					fmt.Printf("DEBUG: [MCP Loop Iter %d] Sending %d messages to LLM:\n", iter, len(iterReq.Messages))
					for msgIdx, msg := range iterReq.Messages {
						// Basic logging to avoid excessive output if content is long
						toolCallInfo := "None"
						if len(msg.ToolCalls) > 0 {
							toolCallInfo = fmt.Sprintf("%d calls", len(msg.ToolCalls))
						}
						fmt.Printf("  [%d] Role: %-9s ToolCallID: %-15s ToolCalls: %-10s Content: %.60s...\n",
							msgIdx, msg.Role, msg.ToolCallID, toolCallInfo, strings.ReplaceAll(msg.Content, "\n", "\\n"))
					}
					fmt.Printf("DEBUG: [MCP Loop Iter %d] ToolChoice for LLM call: %+v\n", iter, iterReq.ToolChoice)

					// --- Execute LLM Call for this iteration ---
					llmHandle := nextNodeDef.Start(heart.Into(iterReq)) // Use loopCtx implicitly
					llmFuture := heart.FanIn(loopCtx, llmHandle)
					llmResponse, llmErr := llmFuture.Get() // Wait for this LLM call

					// --- **** ADDED DEBUG LOGGING **** ---
					fmt.Printf("DEBUG: [MCP Loop Iter %d] Received LLM Response.\n", iter)
					if llmErr != nil {
						// Log the framework error from the LLM node itself
						fmt.Printf("ERROR: [MCP Loop Iter %d] LLM call node returned framework error: %v\n", iter, llmErr)
						// Immediately wrap and return this framework error
						err := fmt.Errorf("LLM call node failed on iteration %d within MCP loop: %w", iter, llmErr)
						return heart.IntoError[openai.ChatCompletionResponse](err)
					}

					// Log details from the received response
					if len(llmResponse.Choices) > 0 {
						choice := llmResponse.Choices[0]
						fmt.Printf("DEBUG: [MCP Loop Iter %d] LLM FinishReason: '%s'\n", iter, choice.FinishReason)
						fmt.Printf("DEBUG: [MCP Loop Iter %d] LLM Response ID: '%s'\n", iter, llmResponse.ID) // Check if ID changes
						fmt.Printf("DEBUG: [MCP Loop Iter %d] LLM Response Content: %.60s...\n", iter, strings.ReplaceAll(choice.Message.Content, "\n", "\\n"))
						if len(choice.Message.ToolCalls) > 0 {
							fmt.Printf("DEBUG: [MCP Loop Iter %d] LLM requested %d tool call(s).\n", iter, len(choice.Message.ToolCalls))
							// Optional: Log details of requested calls
							// for tcIdx, tc := range choice.Message.ToolCalls {
							// 	fmt.Printf("  ToolCall %d: ID=%s Type=%s FuncName=%s\n", tcIdx, tc.ID, tc.Type, tc.Function.Name)
							// }
						} else {
							fmt.Printf("DEBUG: [MCP Loop Iter %d] LLM did not request tool calls.\n", iter)
						}
					} else {
						// This case indicates an issue with the LLM response format or an empty response
						fmt.Printf("WARN: [MCP Loop Iter %d] LLM response had no choices. Response Object: %+v\n", iter, llmResponse)
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
						fmt.Printf("INFO: [MCP Loop Iter %d] Loop condition met (no tool calls requested by LLM). Returning final response.\n", iter)
						return heart.Into(lastResponse)
					}

					// --- Tool Calls Exist: Process and Invoke ---
					fmt.Printf("INFO: [MCP Loop Iter %d] LLM requested %d tool call(s). Processing...\n", iter, len(toolCalls))

					// 1. Add the assistant's message (requesting tools) to the history
					// Ensure we only add if choices exist (already checked, but good practice)
					if len(llmResponse.Choices) > 0 {
						// Make a copy of the message to avoid potential side effects if the original response object is mutated elsewhere
						assistantMsg := llmResponse.Choices[0].Message
						currentMessages = append(currentMessages, assistantMsg)
						fmt.Printf("DEBUG: [MCP Loop Iter %d] Appended Assistant message requesting tools to history.\n", iter)
					}

					// --- Invoke Tools in Parallel ---
					toolCallFutures := make([]*heart.Future[internal.CallToolOutput], len(toolCalls))
					fmt.Printf("DEBUG: [MCP Loop Iter %d] Starting %d tool call node(s).\n", iter, len(toolCalls))
					for i, toolCall := range toolCalls {
						// Basic validation of the tool call structure
						if toolCall.Type != openai.ToolTypeFunction || toolCall.Function.Name == "" {
							errMsg := fmt.Sprintf("invalid tool call from LLM on iteration %d: type=%s, id=%s, function_name=%s", iter, toolCall.Type, toolCall.ID, toolCall.Function.Name)
							fmt.Printf("ERROR: [MCP Loop Iter %d] %s\n", iter, errMsg)
							// Create an error handle for this specific failed call
							errorHandle := heart.IntoError[internal.CallToolOutput](errors.New(errMsg))
							toolCallFutures[i] = heart.FanIn(loopCtx, errorHandle)
							continue // Skip starting the actual tool call node
						}

						// Ensure MCP tools were listed if needed (should be guaranteed by mcpToolsAvailable check earlier)
						if !mcpToolsAvailable && len(originalReq.Tools) == 0 { // Check if *any* tools were expected
							errMsg := fmt.Sprintf("LLM requested MCP tool '%s' but no tools were initially listed or provided", toolCall.Function.Name)
							fmt.Printf("ERROR: [MCP Loop Iter %d] %s\n", iter, errMsg)
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
					fmt.Printf("DEBUG: [MCP Loop Iter %d] Waiting for %d tool call node(s) to complete.\n", iter, len(toolCallFutures))
					toolMessages := make([]openai.ChatCompletionMessage, 0, len(toolCalls))
					for i, future := range toolCallFutures {
						callOutput, callErr := future.Get()        // Wait for the i-th tool call node
						toolCallID := toolCalls[i].ID              // Get the original Tool Call ID
						toolCallName := toolCalls[i].Function.Name // Get the original Tool Call Name

						// Handle framework errors from the callToolNode itself
						if callErr != nil {
							fmt.Printf("ERROR: [MCP Loop Iter %d] Framework error getting result for tool '%s' (ID: %s): %v\n", iter, toolCallName, toolCallID, callErr)
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
							fmt.Printf("ERROR: [MCP Loop Iter %d] callToolNode reported internal error for tool '%s' (ID: %s): %v\n", iter, toolCallName, toolCallID, callOutput.Error)
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
							fmt.Printf("WARN: [MCP Loop Iter %d] callToolNode result for '%s' missing ToolCallID. Setting from original: %s\n", iter, toolCallName, toolCallID)
							callOutput.ResultMsg.ToolCallID = toolCallID
						} else if callOutput.ResultMsg.ToolCallID != toolCallID {
							fmt.Printf("WARN: [MCP Loop Iter %d] ToolCallID mismatch in CallToolOutput for tool '%s'. Expected '%s', got '%s'. Using expected ID.\n", iter, toolCallName, toolCallID, callOutput.ResultMsg.ToolCallID)
							callOutput.ResultMsg.ToolCallID = toolCallID // Correct it to match the request
						}
						// Ensure Role is Tool
						callOutput.ResultMsg.Role = openai.ChatMessageRoleTool
						// Add Name field if not already present (often helpful for LLM)
						if callOutput.ResultMsg.Name == "" {
							callOutput.ResultMsg.Name = toolCallName
						}
						fmt.Printf("DEBUG: [MCP Loop Iter %d] Received result for tool '%s' (ID: %s): Content='%.60s...'\n", iter, toolCallName, toolCallID, strings.ReplaceAll(callOutput.ResultMsg.Content, "\n", "\\n"))
						toolMessages = append(toolMessages, callOutput.ResultMsg)
					}

					// 2. Add the collected tool results/errors to the message history
					currentMessages = append(currentMessages, toolMessages...)
					fmt.Printf("DEBUG: [MCP Loop Iter %d] Appended %d tool message(s) to history.\n", iter, len(toolMessages))

					// 3. Mark that tools have now been called in this run
					// This might affect ToolChoice logic in the *next* iteration if implemented that way.
					toolsHaveBeenCalled = true

					fmt.Printf("--- MCP Loop Iteration %d End ---\n", iter) // Mark iteration end

					// Loop continues...

				} // --- End of for loop ---

				// --- Exit Condition: Max Iterations Reached ---
				fmt.Printf("ERROR: [MCP Loop] Max iterations (%d) reached. Returning error.\n", maxToolIterations)
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
