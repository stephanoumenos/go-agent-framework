// ./nodes/openai/middleware/structuredoutput.go
package middleware

import (
	"context" // Keep standard context for Get method signature
	"encoding/json"
	"errors"
	"fmt"
	"heart" // Use heart types

	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// TODO: Maybe export this from heart
type genericNodeInitializer struct {
	id heart.NodeTypeID
}

func (g genericNodeInitializer) ID() heart.NodeTypeID {
	return g.id
}

// Keep error local or move to a shared openai errors file if needed elsewhere
var errDuplicatedResponseFormat = errors.New("response format already provided")
var errNoContentFromLLM = errors.New("no content received from LLM response")

// --- Define the Resolver for Structured Output Middleware ---

// structuredOutputResolver implements both heart.NodeResolver and heart.MiddlewareExecutor.
type structuredOutputResolver[SOut any] struct {
	nextDefinition heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]
	nodeTypeID     heart.NodeTypeID
}

// Ensure interfaces are implemented (compile-time check)
var _ heart.NodeResolver[openai.ChatCompletionRequest, any] = (*structuredOutputResolver[any])(nil)
var _ heart.MiddlewareExecutor[openai.ChatCompletionRequest, any] = (*structuredOutputResolver[any])(nil)

// Init returns the initializer providing the NodeTypeID for this middleware.
func (r *structuredOutputResolver[SOut]) Init() heart.NodeInitializer {
	// Use a generic initializer with the stored NodeTypeID
	return genericNodeInitializer{id: r.nodeTypeID}
}

// Get implements the standard NodeResolver interface.
// This method should ideally not be called directly if the MiddlewareExecutor interface is detected by node.get.
// It returns zero values and an error to indicate misuse.
func (r *structuredOutputResolver[SOut]) Get(ctx context.Context, in openai.ChatCompletionRequest) (SOut, error) {
	var zero SOut
	// Log or handle this unexpected call if necessary
	// fmt.Printf("WARN: structuredOutputResolver.Get called directly for node type %s\n", r.nodeTypeID)
	return zero, fmt.Errorf("internal error: structuredOutputResolver.Get called directly for middleware type %s", r.nodeTypeID)
}

// ExecuteMiddleware implements the MiddlewareExecutor interface.
// This is where the actual middleware logic resides, using the provided ResolverContext.
func (r *structuredOutputResolver[SOut]) ExecuteMiddleware(rctx heart.ResolverContext, in openai.ChatCompletionRequest) (SOut, error) {
	var sOut SOut // Target struct for the output

	// 1. Check for existing ResponseFormat
	if in.ResponseFormat != nil {
		// Returning an error here will halt the node's execution.
		return sOut, fmt.Errorf("error in %s: %w", r.nodeTypeID, errDuplicatedResponseFormat)
	}

	// 2. Generate JSON Schema for the target output struct
	// TODO: Consider caching schema generation if SOut type is constant and generation is expensive.
	schema, err := jsonschema.GenerateSchemaForType(sOut)
	if err != nil {
		return sOut, fmt.Errorf("error in %s generating schema for type %T: %w", r.nodeTypeID, sOut, err)
	}

	// 3. Modify the *incoming* request (`in`) to include the schema.
	// NOTE: This modifies the `in` struct that was passed.
	// If the original request object needs to be preserved elsewhere, a deep copy should be made before modification.
	// For typical middleware chains, modifying is often intended.
	in.ResponseFormat = &openai.ChatCompletionResponseFormat{
		Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
		JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
			Name:   "output", // This name likely needs to match instruction in prompt
			Schema: schema,
			Strict: true,
		},
	}

	// 4. Execute the *next* node in the chain.
	// Bind the *modified* input request (`in`) to the `next` node's definition.
	// This creates the runtime instance (Noder) for the actual LLM call.
	llmNoder := r.nextDefinition.Bind(heart.Into(in))

	// Call Out on the next node, passing the crucial ResolverContext (rctx).
	// This triggers the execution of the underlying LLM call node.
	llmResponse, err := llmNoder.Out(rctx)
	if err != nil {
		// If the underlying LLM call fails, propagate the error.
		return sOut, fmt.Errorf("error from underlying node wrapped by %s: %w", r.nodeTypeID, err)
	}

	// 5. Process the response from the next node.
	if len(llmResponse.Choices) == 0 || llmResponse.Choices[0].Message.Content == "" {
		// Handle cases where the LLM didn't return content as expected.
		// FinishReason might be useful here (e.g., content filter, length).
		finishReason := ""
		if len(llmResponse.Choices) > 0 {
			finishReason = string(llmResponse.Choices[0].FinishReason)
		}
		return sOut, fmt.Errorf("error in %s: %w (finish reason: %s)", r.nodeTypeID, errNoContentFromLLM, finishReason)
	}

	// 6. Unmarshal the JSON content from the LLM response into the target struct `sOut`.
	llmContent := llmResponse.Choices[0].Message.Content
	err = json.Unmarshal([]byte(llmContent), &sOut)
	if err != nil {
		// If unmarshaling fails, it means the LLM didn't adhere to the schema.
		// You might want to log the invalid content here for debugging.
		// fmt.Printf("DEBUG: Failed to unmarshal content in %s: %s\n", r.nodeTypeID, llmContent)
		return sOut, fmt.Errorf("error in %s: failed to unmarshal LLM response into %T: %w", r.nodeTypeID, sOut, err)
	}

	// 7. Return the successfully parsed struct.
	return sOut, nil
}

// --- Define the Middleware Constructor Function ---

// WithStructuredOutput defines a NodeDefinition that wraps another ChatCompletion node
// to enforce structured JSON output matching the SOut type.
// It now requires a heart.Context and a heart.NodeID to place the middleware node correctly in the graph.
func WithStructuredOutput[SOut any](
	ctx heart.Context, // Context to define the node within (provides BasePath)
	nodeID heart.NodeID, // Local ID for this middleware node instance
	next heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse], // The node definition this middleware wraps
) heart.NodeDefinition[openai.ChatCompletionRequest, SOut] { // Returns a definition for Input -> SOut

	// Create the specific resolver instance for this middleware.
	resolver := &structuredOutputResolver[SOut]{
		nextDefinition: next,                                // Store the definition of the node to call next
		nodeTypeID:     "openai:structuredOutputMiddleware", // Assign a unique type ID for this kind of middleware
	}

	// Use heart.DefineNode to create the actual NodeDefinition for this middleware instance.
	// This correctly sets up the node's path, context, and associates the resolver.
	// The input type is ChatCompletionRequest, the output type is SOut.
	middlewareNodeDefinition := heart.DefineNode[openai.ChatCompletionRequest, SOut](ctx, nodeID, resolver)

	return middlewareNodeDefinition
}

// Remove the old DefineThinMiddleware and related structs from this file if they were here.
// The new approach uses DefineNode with a custom resolver implementing MiddlewareExecutor.

/*
// OLD CODE TO BE REMOVED from this file (if present):
func DefineThinMiddleware(...) ...
type thinMiddlewareDefinition struct { ... }
type thinMiddleware struct { ... }
func structuredOutputMiddleware(...) ... // Logic is moved into structuredOutputResolver.ExecuteMiddleware
*/
