// ./nodes/openai/middleware/mcp.go
package middleware

/*
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"heart"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/server"
	openai "github.com/sashabaranov/go-openai"
)

const withMCPNodeTypeID heart.NodeTypeID = "openai_with_mcp_middleware"

var (
	errNoToolCalls         = errors.New("no tool calls present in the response")
	errMCPToolNotFound     = errors.New("MCP tool not found")
	errMCPInvocationError  = errors.New("error invoking MCP tool")
	errUnsupportedToolCall = errors.New("unsupported tool call type in response")
)

// --- Helper Function: Convert MCP Tool to OpenAI FunctionDefinition ---

func mcpToolToOpenAIFunction(mcpTool heart.MCPTool) (openai.FunctionDefinition, error) {
	// Basic conversion, might need more robust schema handling depending on MCP spec details
	params := mcpTool.InputSchema
	if params == nil {
		params = make(map[string]any)
	}
	// Ensure 'properties' exists as OpenAI spec requires it, even if empty.
	if _, ok := params["properties"]; !ok {
		params["properties"] = make(map[string]any)
	}
	// Ensure 'type' is 'object' if properties are defined. MCP might not enforce this.
	if _, ok := params["type"]; !ok {
		params["type"] = "object"
	}

	return openai.FunctionDefinition{
		Name:        mcpTool.Name,
		Description: mcpTool.Description,
		Parameters:  params,
	}, nil
}

// --- Helper Function: Invoke MCP Tool ---

// invokeMCPTool finds the correct server and calls the tool.
func invokeMCPTool(
	ctx context.Context,
	toolCall openai.ToolCall,
	servers []server.MCPServer,
	serverMap map[string]server.MCPServer, // Map tool name to server for quick lookup
) (string, error) {
	if toolCall.Type != openai.ToolTypeFunction {
		return "", fmt.Errorf("%w: expected function, got %s", errUnsupportedToolCall, toolCall.Type)
	}

	toolName := toolCall.Function.Name
	server, ok := serverMap[toolName]
	if !ok {
		return "", fmt.Errorf("%w: %s", errMCPToolNotFound, toolName)
	}

	var inputArgs map[string]any
	if toolCall.Function.Arguments != "" {
		err := json.Unmarshal([]byte(toolCall.Function.Arguments), &inputArgs)
		if err != nil {
			// Return error message indicating bad JSON from LLM
			errorMsg := fmt.Sprintf("Error: Invalid JSON input for tool %s: %v. Raw Input: %s", toolName, err, toolCall.Function.Arguments)
			// Log the error server-side if needed
			fmt.Printf("WARN: invokeMCPTool JSON unmarshal failed for %s: %v\n", toolName, err)
			return errorMsg, nil // Return error message *as the result* for the LLM to see
			// Alternatively, could return a specific error type here and handle it upstream
			// return "", fmt.Errorf("invalid JSON input for tool %s: %w. Raw Input: %s", toolName, err, toolCall.Function.Arguments)
		}
	} else {
		inputArgs = make(map[string]any) // Empty arguments if none provided
	}

	// Invoke the tool via the server interface
	mcpResult, err := server.CallTool(ctx, toolName, inputArgs)
	if err != nil {
		errMsg := fmt.Sprintf("Error invoking MCP tool %s on server %s: %v", toolName, server.Name(), err)
		// Log the error server-side
		fmt.Printf("ERROR: %s\n", errMsg)
		// Return error message *as the result* for the LLM to see
		return fmt.Sprintf("Error running tool %s: %v", toolName, err), nil
		// Alternatively, return a specific error type:
		// return "", fmt.Errorf("%w: %s on server %s: %v", errMCPInvocationError, toolName, server.Name(), err)
	}

	// Format the result according to OpenAI expectations (string content)
	// Mimics Python's logic: single item as JSON, multiple items as JSON array.
	var toolOutput string
	if len(mcpResult.Content) == 1 {
		// Marshal the single content item directly
		jsonBytes, err := json.Marshal(mcpResult.Content[0])
		if err != nil {
			toolOutput = fmt.Sprintf("Error formatting tool result for %s: %v", toolName, err)
			fmt.Printf("WARN: Failed to marshal single MCP result item for %s: %v\n", toolName, err)
		} else {
			toolOutput = string(jsonBytes)
		}
	} else if len(mcpResult.Content) > 0 {
		// Marshal the whole list of content items
		jsonBytes, err := json.Marshal(mcpResult.Content)
		if err != nil {
			toolOutput = fmt.Sprintf("Error formatting tool result array for %s: %v", toolName, err)
			fmt.Printf("WARN: Failed to marshal MCP result list for %s: %v\n", toolName, err)
		} else {
			toolOutput = string(jsonBytes)
		}
	} else {
		// Handle empty content - maybe return an empty string or specific message?
		toolOutput = "" // Or "Tool executed successfully with no output."
	}

	return toolOutput, nil
}

// --- Workflow Handler ---

// mcpWorkflowState holds data passed between internal nodes of the WithMCP workflow.
type mcpWorkflowState struct {
	OriginalRequest    openai.ChatCompletionRequest
	ModifiedRequest    openai.ChatCompletionRequest // Request potentially with tools added
	FirstResponse      openai.ChatCompletionResponse
	ToolResultMessages []openai.ChatCompletionMessage // Messages generated from tool calls
	NeedsSecondCall    bool
	Error              error // To propagate errors between internal nodes
}

// mcpWorkflowHandler defines the logic for the WithMCP middleware.
func mcpWorkflowHandler(
	nextNodeDef heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
	servers []server.MCPServer,
) heart.WorkflowHandlerFunc[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	return func(ctx heart.Context, originalReq openai.ChatCompletionRequest) heart.ExecutionHandle[openai.ChatCompletionResponse] {

		// --- Node 1: Get MCP Tools ---
		getToolsNodeID := heart.NodeID("get_mcp_tools")
		getToolsNode := heart.NewNode(ctx, getToolsNodeID,
			func(toolsCtx heart.NewNodeContext) heart.ExecutionHandle[[]openai.FunctionDefinition] {
				var allFuncs []openai.FunctionDefinition
				toolNameToServer := make(map[string]server.MCPServer) // For duplicate check and invocation lookup
				var wg sync.WaitGroup
				var mu sync.Mutex
				var errs []error

				if len(servers) == 0 {
					return heart.Into(allFuncs) // No servers, no tools
				}

				resultsChan := make(chan []heart.MCPTool, len(servers))
				errChan := make(chan error, len(servers))

				for _, server := range servers {
					wg.Add(1)
					go func(s server.MCPServer) {
						defer wg.Done()
						// Use toolsCtx.Context which contains the cancellable context.Context
						serverTools, err := s.ListTools(toolsCtx.Context)
						if err != nil {
							errChan <- fmt.Errorf("failed to list tools from MCP server '%s': %w", s.Name(), err)
							return
						}
						resultsChan <- serverTools
					}(server)
				}

				go func() {
					wg.Wait()
					close(resultsChan)
					close(errChan)
				}()

				// Collect results and errors
				serverIndex := 0 // Keep track for associating tools back to servers if needed later
				serverToolsList := make([][]heart.MCPTool, len(servers))
				for tools := range resultsChan {
					// It's safer to store results associated with their server if order isn't guaranteed
					// For now, we just collect them. A map[string][]MCPTool might be better.
					if serverIndex < len(servers) {
						serverToolsList[serverIndex] = tools
						serverIndex++
					} else {
						// This case shouldn't happen with the current setup, but good to guard
						mu.Lock()
						errs = append(errs, errors.New("received more tool results than servers"))
						mu.Unlock()
					}
				}
				for err := range errChan {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}

				if len(errs) > 0 {
					// Combine errors or return the first one
					combinedErr := fmt.Errorf("errors fetching MCP tools: %v", errs)
					return heart.IntoError[[]openai.FunctionDefinition](combinedErr)
				}

				// Process collected tools, check duplicates, and convert
				serverIndex = 0 // Reset index for mapping
				for _, serverTools := range serverToolsList {
					server := servers[serverIndex] // Assumes order is maintained or mapped correctly
					for _, mcpTool := range serverTools {
						mu.Lock()
						if existingServer, exists := toolNameToServer[mcpTool.Name]; exists {
							errs = append(errs, fmt.Errorf("duplicate tool name '%s' found on servers '%s' and '%s'", mcpTool.Name, existingServer.Name(), server.Name()))
						} else {
							funcDef, err := mcpToolToOpenAIFunction(mcpTool)
							if err != nil {
								errs = append(errs, fmt.Errorf("failed to convert MCP tool '%s' from server '%s': %w", mcpTool.Name, server.Name(), err))
							} else {
								allFuncs = append(allFuncs, funcDef)
								toolNameToServer[mcpTool.Name] = server // Store mapping
							}
						}
						mu.Unlock()
					}
					serverIndex++
				}

				if len(errs) > 0 {
					combinedErr := fmt.Errorf("errors processing MCP tools: %v", errs)
					return heart.IntoError[[]openai.FunctionDefinition](combinedErr)
				}

				// Store the server map in the context for the next node? No, pass via closure or intermediate result.
				// For now, just return the function definitions. The map will be recreated or passed.
				return heart.Into(allFuncs)
			})

		// --- Node 2: First LLM Call (potentially with tools) ---
		firstCallNodeID := heart.NodeID("first_llm_call")
		firstCallNode := heart.NewNode(ctx, firstCallNodeID,
			func(callCtx heart.NewNodeContext) heart.ExecutionHandle[mcpWorkflowState] {
				// Get tools from previous node
				toolsFuture := heart.FanIn(callCtx, getToolsNode)
				tools, err := toolsFuture.Get()
				if err != nil {
					return heart.IntoError[mcpWorkflowState](fmt.Errorf("failed dependencies for %s: %w", firstCallNodeID, err))
				}

				state := mcpWorkflowState{
					OriginalRequest: originalReq,
					NeedsSecondCall: false, // Default
				}
				state.ModifiedRequest = originalReq // Start with original

				// Add tools to the request if any were found
				if len(tools) > 0 {
					state.ModifiedRequest.Tools = make([]openai.Tool, len(tools))
					for i, f := range tools {
						state.ModifiedRequest.Tools[i] = openai.Tool{
							Type:     openai.ToolTypeFunction,
							Function: f, // Already FunctionDefinition type
						}
					}
				}

				// Execute the wrapped LLM node definition
				llmHandle := nextNodeDef.Start(heart.Into(state.ModifiedRequest))
				responseFuture := heart.FanIn(callCtx, llmHandle)
				response, err := responseFuture.Get()
				if err != nil {
					state.Error = fmt.Errorf("first LLM call failed for %s: %w", firstCallNodeID, err)
					// Return state with error, not using IntoError directly here
					return heart.Into(state)
				}
				state.FirstResponse = response

				return heart.Into(state) // Pass state containing the first response
			})

		// --- Node 3: Process Response and Invoke Tools (if necessary) ---
		processNodeID := heart.NodeID("process_and_invoke")
		processNode := heart.NewNode(ctx, processNodeID,
			func(processCtx heart.NewNodeContext) heart.ExecutionHandle[mcpWorkflowState] {
				// Get state from the first LLM call
				stateFuture := heart.FanIn(processCtx, firstCallNode)
				state, err := stateFuture.Get()
				if err != nil {
					// Error occurred during first call or its dependencies
					return heart.IntoError[mcpWorkflowState](fmt.Errorf("failed dependencies for %s: %w", processNodeID, err))
				}
				// If state itself contains an error from the first call node, propagate it
				if state.Error != nil {
					return heart.Into(state) // Pass the state containing the error
				}

				// Check for tool calls in the response
				if len(state.FirstResponse.Choices) == 0 || state.FirstResponse.Choices[0].Message.ToolCalls == nil {
					// No tool calls, the first response is the final one
					state.NeedsSecondCall = false
					return heart.Into(state)
				}

				// Tool calls exist, need to invoke them
				state.NeedsSecondCall = true
				toolCalls := state.FirstResponse.Choices[0].Message.ToolCalls
				state.ToolResultMessages = make([]openai.ChatCompletionMessage, 0, len(toolCalls))

				// Recreate tool name to server map (or pass it differently if performance matters)
				toolNameToServer := make(map[string]server.MCPServer)
				var toolFetchWg sync.WaitGroup
				var toolFetchErr error
				toolFetchWg.Add(1)
				go func() { // Fetch tools again to build the map - less efficient but simpler for now
					defer toolFetchWg.Done()
					var mu sync.Mutex // Mutex for map access within this goroutine
					var wg sync.WaitGroup
					errChan := make(chan error, len(servers))
					for _, server := range servers {
						wg.Add(1)
						go func(s server.MCPServer) {
							defer wg.Done()
							serverTools, err := s.ListTools(processCtx.Context)
							if err != nil {
								errChan <- fmt.Errorf("process_and_invoke: failed list tools for %s: %w", s.Name(), err)
								return
							}
							mu.Lock()
							for _, tool := range serverTools {
								// Could check for duplicates again, but getToolsNode should have caught it
								toolNameToServer[tool.Name] = s
							}
							mu.Unlock()
						}(server)
					}
					wg.Wait()
					close(errChan)
					// Collect errors
					for err := range errChan {
						if toolFetchErr == nil {
							toolFetchErr = err
						} // Store first error
					}
				}()
				toolFetchWg.Wait() // Wait for map population

				if toolFetchErr != nil {
					state.Error = fmt.Errorf("failed to rebuild tool map in %s: %w", processNodeID, toolFetchErr)
					return heart.Into(state)
				}

				// --- Invoke tools sequentially for simplicity, could parallelize ---
				invocationErrors := []string{}
				for _, toolCall := range toolCalls {
					// Use processCtx.Context which contains the cancellable context.Context
					resultContent, invokeErr := invokeMCPTool(processCtx.Context, toolCall, servers, toolNameToServer)
					if invokeErr != nil {
						// This indicates a framework/setup error, not a tool execution error shown to LLM
						invocationErrors = append(invocationErrors, fmt.Sprintf("framework error invoking tool %s (ID: %s): %v", toolCall.Function.Name, toolCall.ID, invokeErr))
						// Continue processing other tools? Or fail fast? Let's collect errors for now.
						// We still need to provide *some* result message back to the LLM.
						resultContent = fmt.Sprintf("Error: Failed to invoke tool %s due to internal error.", toolCall.Function.Name)
					}

					// Create the tool result message for the *next* LLM call
					state.ToolResultMessages = append(state.ToolResultMessages, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    resultContent, // Content is the string result from invokeMCPTool
						ToolCallID: toolCall.ID,   // Link result back to the call
						// Name is deprecated/optional for tool role messages in some versions
					})
				}

				if len(invocationErrors) > 0 {
					state.Error = fmt.Errorf("errors occurred during MCP tool invocation: %s", strings.Join(invocationErrors, "; "))
					// Even with errors, we have ToolResultMessages (containing error strings), so proceed to second call
				}

				return heart.Into(state) // Return state with ToolResultMessages populated
			})

		// --- Node 4: Second LLM Call (conditional) ---
		secondCallNodeID := heart.NodeID("second_llm_call")
		secondCallNode := heart.NewNode(ctx, secondCallNodeID,
			func(callCtx heart.NewNodeContext) heart.ExecutionHandle[openai.ChatCompletionResponse] {
				// Get state from the processing node
				stateFuture := heart.FanIn(callCtx, processNode)
				state, err := stateFuture.Get()
				if err != nil {
					return heart.IntoError[openai.ChatCompletionResponse](fmt.Errorf("failed dependencies for %s: %w", secondCallNodeID, err))
				}
				// If state itself contains an error from previous steps, propagate it
				if state.Error != nil {
					// Construct a response indicating the error? Or just fail?
					// Let's fail the workflow clearly.
					return heart.IntoError[openai.ChatCompletionResponse](fmt.Errorf("error before second LLM call in %s: %w", secondCallNodeID, state.Error))
				}

				// Decide whether to make the second call
				if !state.NeedsSecondCall {
					// No second call needed, return the first response
					return heart.Into(state.FirstResponse)
				}

				// --- Prepare request for the second call ---
				secondReq := state.OriginalRequest // Start with the *original* request messages

				// Append the first response's assistant message (containing the tool calls)
				if len(state.FirstResponse.Choices) > 0 {
					secondReq.Messages = append(secondReq.Messages, state.FirstResponse.Choices[0].Message)
				}

				// Append the tool result messages
				secondReq.Messages = append(secondReq.Messages, state.ToolResultMessages...)

				// Crucially, REMOVE tools definition for the second call,
				// unless you specifically want the LLM to call tools *again* based on the first tool results.
				// Standard pattern is to let the LLM synthesize a final response after getting tool results.
				secondReq.Tools = nil
				secondReq.ToolChoice = nil // Reset tool choice as well

				// --- Execute the second LLM call ---
				llmHandle := nextNodeDef.Start(heart.Into(secondReq))
				responseFuture := heart.FanIn(callCtx, llmHandle)
				finalResponse, err := responseFuture.Get()
				if err != nil {
					return heart.IntoError[openai.ChatCompletionResponse](fmt.Errorf("second LLM call failed for %s: %w", secondCallNodeID, err))
				}

				// Return the final response from the second call
				return heart.Into(finalResponse)
			})

		// Return the handle to the last node in the chain
		return secondCallNode
	}
}

// WithMCP creates a NodeDefinition that wraps an existing OpenAI ChatCompletion node
// definition, adding support for discovering and invoking tools via the Model Controller Protocol (MCP).
//
// It fetches tools from the provided MCPServers, adds them to the initial LLM request.
// If the LLM response includes tool calls for these MCP tools, it invokes them via the
// respective MCPServer and sends the results back to the LLM in a subsequent call
// to get the final response.
//
// Parameters:
//   - nodeID: A unique identifier for this middleware workflow instance.
//   - nextNodeDef: The heart.NodeDefinition of the underlying OpenAI CreateChatCompletion node to wrap.
//   - servers: A slice of server.MCPServer instances from which to fetch and invoke tools.
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
	// Servers slice can be empty, which means no tools will be added.

	// Generate the handler function, capturing the nextNodeDef and servers.
	handler := mcpWorkflowHandler(nextNodeDef, servers)

	// Create a workflow resolver. Use a specific type ID for this middleware pattern.
	workflowResolver := heart.NewWorkflowResolver(nodeID, handler) // Use user-provided nodeID for the instance

	// Define and return the NodeDefinition for this workflow.
	return heart.DefineNode(
		nodeID,           // Use the user-provided nodeID for the definition instance
		workflowResolver, // The resolver containing the workflow logic
	)
}
*/
