// ./nodes/openai/middleware/tools.go
package middleware

/* Comment out for now. We will fix it soon. Needs fixing for new lazy style execution.

import (
	"context"
	"encoding/json" // Needed for unmarshaling arguments in ExecuteMiddleware
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai/clientiface" // Import the client interface

	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// --- Errors ---

var (
	// errToolsAlreadyDefined is returned if the input request already has tools defined.
	errToolsAlreadyDefined = errors.New("tools already defined in the request")
	// errUnknownToolFromOpenAI is returned if the LLM requests a tool not provided to the middleware.
	errUnknownToolFromOpenAI = errors.New("openAI responded with an unknown tool function name")
	// errToolExecutionFailed indicates an error during the execution of a defined tool.
	errToolExecutionFailed = errors.New("tool execution failed")
	// errToolArgumentParsingFailed indicates an error parsing arguments for a tool.
	errToolArgumentParsingFailed = errors.New("failed to parse tool arguments")
	// errDependencyNotSet indicates the OpenAI client dependency was not injected.
	errDependencyNotSet = errors.New("openai client dependency not set in tools middleware")
)

// --- Tool Definition Interfaces/Structs ---

// Tool represents a function callable by the LLM.
type Tool interface {
	// ParseParams parses the arguments map (usually string->any from JSON) into the specific parameter struct/type for the tool.
	ParseParams(arguments map[string]any) (params any, err error)
	// DefineTool returns the definition (name, description, parameters) used to register the tool with OpenAI.
	DefineTool() ToolDefinition
	// Call executes the tool's logic with the parsed parameters. Context is passed for potential cancellation/deadlines.
	Call(ctx context.Context, params any) (result string, err error)
	// Implement heart.NodeInitializer if the tool itself needs dependencies.
	// Init() heart.NodeInitializer // Optional: Add if tools need DI
}

// ToolDefinition describes a tool for the OpenAI API.
// Note: This definition is internal to the middleware/tool implementation.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  jsonschema.Definition // Use go-openai's jsonschema type directly
}

// --- Tools Middleware Resolver ---

const toolsMiddlewareNodeTypeID heart.NodeTypeID = "openai:toolsMiddleware"

// ToolsMiddlewareInitializer handles dependency injection for the resolver.
type ToolsMiddlewareInitializer struct {
	// Store the client *interface*.
	client clientiface.ClientInterface // Use the interface type
}

// ID returns the type identifier for this initializer.
func (i *ToolsMiddlewareInitializer) ID() heart.NodeTypeID {
	return toolsMiddlewareNodeTypeID
}

// DependencyInject receives the OpenAI client *interface*.
func (i *ToolsMiddlewareInitializer) DependencyInject(client clientiface.ClientInterface) { // Accept the interface
	i.client = client
}

// Ensure the initializer implements the necessary interfaces.
var _ heart.NodeInitializer = (*ToolsMiddlewareInitializer)(nil)

// Ensure it implements DependencyInjectable with the *interface* type
var _ heart.DependencyInjectable[clientiface.ClientInterface] = (*ToolsMiddlewareInitializer)(nil) // Use interface

// toolsMiddlewareResolver implements NodeResolver and MiddlewareExecutor.
type toolsMiddlewareResolver struct {
	tools       []Tool
	toolMap     map[string]Tool // Map tool name to Tool for quick lookup
	initializer *ToolsMiddlewareInitializer
	nodeTypeID  heart.NodeTypeID
	// nextDefinition is kept for potential future config sourcing but not used for direct execution flow.
	nextDefinition heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]
}

// Ensure interfaces are implemented.
var _ heart.NodeResolver[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*toolsMiddlewareResolver)(nil)
var _ heart.MiddlewareExecutor[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*toolsMiddlewareResolver)(nil)

// Init creates the initializer which handles dependency injection and builds the tool map.
func (r *toolsMiddlewareResolver) Init() heart.NodeInitializer {
	// Initialize the toolMap for quick lookup during execution.
	r.toolMap = make(map[string]Tool, len(r.tools))
	for _, t := range r.tools {
		def := t.DefineTool() // Get the internal ToolDefinition
		if def.Name == "" {
			// Handle error: Tool must have a name
			// This should ideally return an error, but Init() doesn't support it easily.
			// Log or panic might be options depending on severity.
			fmt.Printf("ERROR: Tool provided to WithTools middleware (type %T) has an empty name.\n", t)
			continue // Skip adding this tool
		}
		r.toolMap[def.Name] = t
	}

	// Create and return the initializer that will receive the client dependency.
	r.initializer = &ToolsMiddlewareInitializer{}
	return r.initializer
}

// Get implements NodeResolver, delegating to ExecuteMiddleware.
func (r *toolsMiddlewareResolver) Get(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// Ensure Init() has been called (should be handled by heart library)
	if r.initializer == nil {
		// This indicates an internal library issue if Init wasn't called before Get.
		return openai.ChatCompletionResponse{}, fmt.Errorf("internal error in %s: initializer not set before Get call", r.nodeTypeID)
	}
	return r.ExecuteMiddleware(ctx, req)
}

// ExecuteMiddleware contains the core tool-handling logic.
func (r *toolsMiddlewareResolver) ExecuteMiddleware(ctx context.Context, originalReq openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// 1. Check if the client dependency was injected via Init.
	if r.initializer == nil || r.initializer.client == nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w", r.nodeTypeID, errDependencyNotSet)
	}
	// Get the client via the interface from the initializer
	client := r.initializer.client // client is type clientiface.ClientInterface

	// 2. Check if tools are already defined in the incoming request (usually an error).
	if len(originalReq.Tools) > 0 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w", r.nodeTypeID, errToolsAlreadyDefined)
	}

	// 3. Prepare the initial API request by adding available tools.
	currentReq := originalReq                                // Start with a copy (shallow is okay for top level)
	currentReq.Messages = copyMessages(originalReq.Messages) // Deep copy messages to avoid mutation issues

	// --- Correctly format apiTools using goopenai types ---
	apiTools := make([]openai.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		middlewareToolDef := t.DefineTool() // Gets our internal ToolDefinition

		// Create the expected go-openai FunctionDefinition
		openAIFuncDef := &openai.FunctionDefinition{ // Use the go-openai type
			Name:        middlewareToolDef.Name,
			Description: middlewareToolDef.Description,
			Parameters:  middlewareToolDef.Parameters, // Directly assignable as both are jsonschema.Definition
		}
		// Append the correctly structured goopenai.Tool
		apiTools = append(apiTools, openai.Tool{
			Type:     openai.ToolTypeFunction,
			Function: openAIFuncDef, // Assign the pointer to the goopenai type
		})
	}
	currentReq.Tools = apiTools // Assign the correctly formatted slice to the request
	// --- End Tool Formatting Fix ---

	// 4. Start the tool execution loop. Handles multiple rounds of tool calls if needed.
	const maxToolIterations = 10 // Prevent infinite loops
	var finalResp openai.ChatCompletionResponse

	for i := 0; i < maxToolIterations; i++ {
		// Use the parent context for each API call.
		// TODO: Consider adding per-call timeouts if needed.
		callCtx := ctx

		// --- Make the OpenAI API call ---
		// fmt.Printf("DEBUG [Tools Middleware %s]: Making API call #%d with Messages: %+v\n", r.nodeTypeID, i+1, currentReq.Messages) // Debug messages
		// fmt.Printf("DEBUG [Tools Middleware %s]: Making API call #%d with Tools: %+v\n", r.nodeTypeID, i+1, currentReq.Tools)       // Debug tools

		resp, err := client.CreateChatCompletion(callCtx, currentReq)
		if err != nil {
			// Handle API call errors
			return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s during OpenAI call #%d: %w", r.nodeTypeID, i+1, err)
		}
		finalResp = resp // Store the latest response, could be the final one or one with tool calls

		// --- Check Response ---
		if len(resp.Choices) == 0 {
			// Should not happen with successful API call, but handle defensively.
			return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: OpenAI response contained no choices", r.nodeTypeID)
		}
		message := resp.Choices[0].Message // Get the assistant's message

		toolCalls := message.ToolCalls
		if len(toolCalls) == 0 {
			// No tool calls requested by the LLM. This is the final response.
			// fmt.Printf("DEBUG [Tools Middleware %s]: No tool calls in response. Finishing loop.\n", r.nodeTypeID) // Debug
			return finalResp, nil // Exit the loop and return the response
		}

		// --- Process Tool Calls ---
		// fmt.Printf("DEBUG [Tools Middleware %s]: Detected %d tool call(s).\n", r.nodeTypeID, len(toolCalls)) // Debug

		// Add the assistant's message (containing the tool call requests) to the history for the next call.
		currentReq.Messages = append(currentReq.Messages, message)

		// Prepare messages containing the results of each tool call.
		toolResponseMessages := make([]openai.ChatCompletionMessage, 0, len(toolCalls))

		// Execute each requested tool call sequentially.
		// TODO: Consider parallel execution of tool calls if independent.
		for _, tc := range toolCalls {
			// fmt.Printf("DEBUG [Tools Middleware %s]: Processing tool call ID %s, Function %s\n", r.nodeTypeID, tc.ID, tc.Function.Name) // Debug

			if tc.Type != openai.ToolTypeFunction {
				// fmt.Printf("WARN [Tools Middleware %s]: Skipping tool call with non-function type: %s\n", r.nodeTypeID, tc.Type) // Debug/Warn
				continue // Skip non-function tool calls if any occur
			}

			tool, exists := r.toolMap[tc.Function.Name]
			if !exists {
				// LLM requested a tool that wasn't provided or registered correctly.
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w: '%s'", r.nodeTypeID, errUnknownToolFromOpenAI, tc.Function.Name)
			}

			// Parse arguments string into a map.
			var args map[string]any
			err := json.Unmarshal([]byte(tc.Function.Arguments), &args)
			if err != nil {
				// Failed to parse the arguments JSON provided by the LLM.
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w for tool '%s': %v. Arguments: %s",
					r.nodeTypeID, errToolArgumentParsingFailed, tc.Function.Name, err, tc.Function.Arguments)
			}

			// Let the specific tool parse the map into its required parameter struct.
			parsedParams, err := tool.ParseParams(args)
			if err != nil {
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: failed to parse parameters for tool '%s': %w", r.nodeTypeID, tc.Function.Name, err)
			}

			// Execute the tool's logic.
			// fmt.Printf("DEBUG [Tools Middleware %s]: Executing tool '%s' with params: %+v\n", r.nodeTypeID, tc.Function.Name, parsedParams) // Debug
			result, err := tool.Call(callCtx, parsedParams) // `result` is the string returned by the tool
			if err != nil {
				// Tool execution itself failed.
				// Consider how much detail to expose. Maybe wrap the error.
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w for tool '%s': %w", r.nodeTypeID, errToolExecutionFailed, tc.Function.Name, err)
			}
			// fmt.Printf("DEBUG [Tools Middleware %s]: Tool '%s' executed successfully. Result: %s\n", r.nodeTypeID, tc.Function.Name, result) // Debug

			// Create the tool result message for the next API call.
			toolDef := tool.DefineTool() // Get definition again to ensure correct Name
			toolResponseMsg := openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    result,       // Content is the result string from tool.Call
				Name:       toolDef.Name, // Use name from the tool's definition
				ToolCallID: tc.ID,        // Use the ID from the assistant's tool call request
			}
			toolResponseMessages = append(toolResponseMessages, toolResponseMsg)
		} // End loop through toolCalls

		// Add all the tool result messages to the conversation history.
		currentReq.Messages = append(currentReq.Messages, toolResponseMessages...)

		// Remove Tools and ToolChoice fields for the subsequent API call,
		// as we are now providing tool results, not asking the LLM to choose tools.
		currentReq.Tools = nil
		currentReq.ToolChoice = nil

		// Continue the loop to make the next API call with the updated messages.

	} // End main loop (maxToolIterations)

	// If the loop finishes due to reaching max iterations without a final response.
	fmt.Printf("WARN: Tool execution loop reached max iterations (%d) in %s. Returning last response received.\n", maxToolIterations, r.nodeTypeID)
	return finalResp, nil // Return the last response received, even if it might contain tool calls
}

// --- Middleware Constructor Function ---

// WithTools defines a NodeDefinition that injects tool calling capabilities.
// It wraps another NodeDefinition (primarily for config) and manages the loop
// of calling the LLM and executing tools internally.
func WithTools(
	ctx heart.Context, // Context for definition.
	nodeID heart.NodeID, // ID for this middleware node instance.
	// 'next' NodeDefinition might be used later for sourcing configuration (e.g., model, temp from underlying def).
	// Document that its resolver's Get/ExecuteMiddleware is NOT called directly by this middleware.
	next heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
	tools ...Tool, // The list of tools to make available to the LLM.
) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] { // Input/Output types remain the same.

	if len(tools) == 0 {
		// If no tools are provided, this middleware doesn't add value.
		// Log a warning and return the original 'next' definition, effectively bypassing the middleware.
		// TODO: Use a proper logger from context or options if available.
		fmt.Printf("WARN: WithTools called with no tools for nodeID %s at path %s. Middleware is bypassed.\n", nodeID, ctx.BasePath)
		return next // Pass through if no tools defined
	}

	// Validate tools minimally (e.g., non-empty name) - already handled in Init now.

	resolver := &toolsMiddlewareResolver{
		tools:          tools,                     // Store provided tools
		nodeTypeID:     toolsMiddlewareNodeTypeID, // Use the constant type ID
		nextDefinition: next,                      // Store underlying definition for potential config use
		// toolMap and initializer are populated in Init()
	}

	// Use heart.DefineNode to create the NodeDefinition for this middleware instance.
	// This registers the resolver and allows the heart library to manage its lifecycle.
	middlewareNodeDefinition := heart.DefineNode[openai.ChatCompletionRequest, openai.ChatCompletionResponse](
		ctx,      // The context provided (determines the node's path)
		nodeID,   // The local ID for this middleware node
		resolver, // The resolver containing the tool-calling logic
	)

	return middlewareNodeDefinition
}

*/
