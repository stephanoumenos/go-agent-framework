// ./nodes/openai/middleware/tools.go
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"heart" // Use heart types

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

// --- Tool Definition Interfaces/Structs (Mostly Unchanged) ---

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
	// This will hold the injected client.
	client *openai.Client
}

func (i *ToolsMiddlewareInitializer) ID() heart.NodeTypeID {
	return toolsMiddlewareNodeTypeID
}

// DependencyInject receives the OpenAI client.
func (i *ToolsMiddlewareInitializer) DependencyInject(client *openai.Client) {
	i.client = client
}

// Ensure the initializer implements the necessary interfaces.
var _ heart.NodeInitializer = (*ToolsMiddlewareInitializer)(nil)
var _ heart.DependencyInjectable[*openai.Client] = (*ToolsMiddlewareInitializer)(nil)

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
	// Initialize tool map here for efficiency
	r.toolMap = make(map[string]Tool, len(r.tools))
	for _, t := range r.tools {
		// TODO: Handle potential dependency injection for Tools themselves here if needed.
		// Defer for v0.1.0.
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
		// This indicates a setup error (DI failed or Init wasn't called correctly).
		return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w", r.nodeTypeID, errDependencyNotSet)
	}
	client := r.initializer.client

	// 2. Check if tools are already defined in the input request.
	if len(originalReq.Tools) > 0 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w", r.nodeTypeID, errToolsAlreadyDefined)
	}

	// 3. Prepare the initial API request.
	currentReq := originalReq                                // Start with the request passed to the middleware.
	currentReq.Messages = copyMessages(originalReq.Messages) // Work on a copy.

	// Format tools for the *first* API call.
	apiTools := make([]openai.Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		toolDef := tool.DefineTool()
		openAIFuncDef := openai.FunctionDefinition{
			Name:        toolDef.Name,
			Description: toolDef.Description,
			Parameters:  toolDef.Parameters,
		}
		apiTools = append(apiTools, openai.Tool{
			Type:     openai.ToolTypeFunction,
			Function: &openAIFuncDef,
		})
	}
	currentReq.Tools = apiTools
	// ToolChoice remains as set by the user in originalReq.

	// 4. Start the tool execution loop.
	const maxToolIterations = 10 // Safety break
	var finalResp openai.ChatCompletionResponse

	for i := 0; i < maxToolIterations; i++ {
		// Use the provided context `ctx` for the API call.
		callCtx := ctx // Can potentially derive context here if needed.

		// Make the actual API call using the injected client.
		resp, err := client.CreateChatCompletion(callCtx, currentReq)
		if err != nil {
			return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s during OpenAI call: %w", r.nodeTypeID, err)
		}
		finalResp = resp // Store the latest response

		if len(resp.Choices) == 0 {
			return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: OpenAI response contained no choices", r.nodeTypeID)
		}
		message := resp.Choices[0].Message

		// Check if the response contains tool calls.
		toolCalls := message.ToolCalls
		if len(toolCalls) == 0 {
			// No tool calls, this is the final response from the LLM.
			return finalResp, nil // Exit the loop and return the response.
		}

		// --- Process Tool Calls ---

		// Append the assistant's message (containing tool calls) to the history.
		currentReq.Messages = append(currentReq.Messages, message)

		// Execute tools and collect results.
		toolResponseMessages := make([]openai.ChatCompletionMessage, 0, len(toolCalls))
		for _, toolCall := range toolCalls {
			if toolCall.Type != openai.ToolTypeFunction {
				continue
			}
			functionCall := toolCall.Function
			tool, exists := r.toolMap[functionCall.Name]
			if !exists {
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w: %s", r.nodeTypeID, errUnknownToolFromOpenAI, functionCall.Name)
			}

			// Parse arguments string into a map.
			var arguments map[string]any
			err := json.Unmarshal([]byte(functionCall.Arguments), &arguments)
			if err != nil {
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: failed to unmarshal arguments for tool %s: %w. Arguments: %s",
					r.nodeTypeID, functionCall.Name, err, functionCall.Arguments)
			}

			// Use the tool's specific parser.
			parsedParams, err := tool.ParseParams(arguments)
			if err != nil {
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w for tool %s: %w", r.nodeTypeID, errToolArgumentParsingFailed, functionCall.Name, err)
			}

			// Execute the tool, passing the loop's context.
			toolResult, err := tool.Call(callCtx, parsedParams)
			if err != nil {
				return openai.ChatCompletionResponse{}, fmt.Errorf("error in %s: %w for tool %s: %w", r.nodeTypeID, errToolExecutionFailed, functionCall.Name, err)

			}

			// Append the tool's result message.
			toolResponseMessages = append(toolResponseMessages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    toolResult,
				Name:       functionCall.Name,
				ToolCallID: toolCall.ID,
			})
		}

		// Add all tool responses to the message history for the next iteration.
		currentReq.Messages = append(currentReq.Messages, toolResponseMessages...)

		// Remove Tools and ToolChoice for subsequent calls as per OpenAI guidance.
		currentReq.Tools = nil
		currentReq.ToolChoice = nil

		// Continue loop...
	}

	// If loop finishes due to max iterations, return the last response received.
	fmt.Printf("WARN: Tool execution loop reached max iterations (%d) in %s.\n", maxToolIterations, r.nodeTypeID)
	return finalResp, nil
}

// copyMessages creates a new slice and copies message content.
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
