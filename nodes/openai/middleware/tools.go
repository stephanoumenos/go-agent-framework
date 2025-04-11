// ./nodes/openai/middleware/tools.go
package middleware

import (
	"context"
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai/clientiface"

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
	client clientiface.ClientInterface // CHANGED: Use the interface type
}

// ID returns the type identifier for this initializer.
func (i *ToolsMiddlewareInitializer) ID() heart.NodeTypeID {
	return toolsMiddlewareNodeTypeID
}

// DependencyInject receives the OpenAI client *interface*.
func (i *ToolsMiddlewareInitializer) DependencyInject(client clientiface.ClientInterface) { // CHANGED: Accept the interface
	i.client = client
}

// Ensure the initializer implements the necessary interfaces.
var _ heart.NodeInitializer = (*ToolsMiddlewareInitializer)(nil)

// Ensure it implements DependencyInjectable with the *interface* type
var _ heart.DependencyInjectable[clientiface.ClientInterface] = (*ToolsMiddlewareInitializer)(nil) // CHANGED: Use interface

// toolsMiddlewareResolver implements NodeResolver and MiddlewareExecutor.
type toolsMiddlewareResolver struct {
	tools       []Tool
	toolMap     map[string]Tool // Map tool name to Tool for quick lookup
	initializer *ToolsMiddlewareInitializer
	nodeTypeID  heart.NodeTypeID
	// nextDefinition is no longer used for execution flow but kept for potential future config.
	nextDefinition heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]
}

// Ensure interfaces are implemented.
var _ heart.NodeResolver[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*toolsMiddlewareResolver)(nil)
var _ heart.MiddlewareExecutor[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*toolsMiddlewareResolver)(nil)

// Init creates the initializer which handles dependency injection.
func (r *toolsMiddlewareResolver) Init() heart.NodeInitializer {
	// ... (toolMap initialization is the same) ...
	r.toolMap = make(map[string]Tool, len(r.tools))
	for _, t := range r.tools {
		def := t.DefineTool()
		r.toolMap[def.Name] = t
	}

	// Create and return the initializer that will receive the client dependency.
	r.initializer = &ToolsMiddlewareInitializer{}
	return r.initializer
}

// Get implements NodeResolver, delegating to ExecuteMiddleware.
func (r *toolsMiddlewareResolver) Get(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return r.ExecuteMiddleware(ctx, req)
}

// ExecuteMiddleware contains the core tool-handling logic.
func (r *toolsMiddlewareResolver) ExecuteMiddleware(ctx context.Context, originalReq openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// 1. Check if the client dependency was injected.
	if r.initializer == nil || r.initializer.client == nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w", r.nodeTypeID, errDependencyNotSet)
	}
	// Get the client via the interface from the initializer
	client := r.initializer.client // client is now type parent_openai.ClientInterface

	// ... (rest of the ExecuteMiddleware logic remains largely the same) ...
	// ... just ensure all calls like client.CreateChatCompletion(...) are made through the interface variable ...

	// 2. Check if tools are already defined... (same)
	if len(originalReq.Tools) > 0 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w", r.nodeTypeID, errToolsAlreadyDefined)
	}

	// 3. Prepare the initial API request... (same)
	currentReq := originalReq
	currentReq.Messages = copyMessages(originalReq.Messages) // Work on a copy.
	// ... format apiTools ... (same)
	apiTools := make([]openai.Tool, 0, len(r.tools))
	// ... (loop through r.tools) ...
	currentReq.Tools = apiTools

	// 4. Start the tool execution loop... (same logic)
	const maxToolIterations = 10
	var finalResp openai.ChatCompletionResponse

	for i := 0; i < maxToolIterations; i++ {
		callCtx := ctx

		// Make the actual API call using the injected client *interface*.
		resp, err := client.CreateChatCompletion(callCtx, currentReq) // THIS CALL REMAINS THE SAME CODE
		// ... (rest of loop logic: check response, process tool calls, etc.) ...
		if err != nil {
			return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s during OpenAI call: %w", r.nodeTypeID, err)
		}
		finalResp = resp // Store the latest response

		if len(resp.Choices) == 0 {
			return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: OpenAI response contained no choices", r.nodeTypeID)
		}
		message := resp.Choices[0].Message

		toolCalls := message.ToolCalls
		if len(toolCalls) == 0 {
			// No tool calls, this is the final response
			return finalResp, nil // Exit loop
		}

		// --- Process Tool Calls --- (same logic)
		currentReq.Messages = append(currentReq.Messages, message)
		toolResponseMessages := make([]openai.ChatCompletionMessage, 0, len(toolCalls))
		// ... (loop through toolCalls, parse args, call tool.Call, append response messages) ...
		currentReq.Messages = append(currentReq.Messages, toolResponseMessages...)

		// Remove Tools/ToolChoice for subsequent calls (same)
		currentReq.Tools = nil
		currentReq.ToolChoice = nil
	}

	// If loop finishes due to max iterations... (same)
	fmt.Printf("WARN: Tool execution loop reached max iterations (%d) in %s.\n", maxToolIterations, r.nodeTypeID)
	return finalResp, nil
}

// copyMessages creates a new slice and copies message content.
// Important to avoid modifying the original request's message slice.
func copyMessages(messages []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	newMessages := make([]openai.ChatCompletionMessage, len(messages))
	copy(newMessages, messages)
	return newMessages
}

// --- Middleware Constructor Function ---

// WithTools defines a NodeDefinition that injects tool calling capabilities.
// It manages the loop of calling the LLM and tools internally.
func WithTools(
	ctx heart.Context, // Context for definition.
	nodeID heart.NodeID, // ID for this middleware node instance.
	// Keep 'next' for potential config sourcing, but document its limited role.
	next heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
	tools ...Tool, // Tools to make available.
) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] { // Input/Output types remain the same.

	if len(tools) == 0 {
		// If no tools are provided, this middleware is a no-op.
		// Log a warning and return the original 'next' definition.
		// TODO: Use a proper logger from context or options if available.
		fmt.Printf("WARN: WithTools called with no tools for nodeID %s. Middleware is bypassed.\n", nodeID)
		return next // Pass through if no tools
	}

	resolver := &toolsMiddlewareResolver{
		tools:          tools,
		nodeTypeID:     toolsMiddlewareNodeTypeID,
		nextDefinition: next, // Store for potential config use, though execution is direct.
	}

	// Use heart.DefineNode to create the middleware node definition.
	middlewareNodeDefinition := heart.DefineNode[openai.ChatCompletionRequest, openai.ChatCompletionResponse](
		ctx,
		nodeID,
		resolver,
	)

	return middlewareNodeDefinition
}
