// ./nodes/openai/createchatcompletion.go
package openai

import (
	"context"
	"heart" // Import heart package for NodeDefinition, DefineNode, etc.
	"heart/nodes/openai/clientiface"

	openai "github.com/sashabaranov/go-openai"
)

const createChatCompletionNodeTypeID heart.NodeTypeID = "openai:createChatCompletion"

// CreateChatCompletion defines a node that calls the OpenAI CreateChatCompletion API endpoint.
// It no longer requires heart.Context during definition.
func CreateChatCompletion(nodeID heart.NodeID) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	// Call the updated DefineNode which only takes nodeID and the resolver.
	return heart.DefineNode[openai.ChatCompletionRequest, openai.ChatCompletionResponse](nodeID, &createChatCompletion{})
}

// createChatCompletionInitializer handles dependency injection for the chat completion node.
type createChatCompletionInitializer struct {
	// Store the client *interface*
	client clientiface.ClientInterface // Use the interface type
}

// ID returns the type identifier for this initializer.
func (c *createChatCompletionInitializer) ID() heart.NodeTypeID {
	return createChatCompletionNodeTypeID
}

// DependencyInject receives the OpenAI client *interface*.
func (c *createChatCompletionInitializer) DependencyInject(client clientiface.ClientInterface) { // Accept the interface
	if client == nil {
		// It's good practice to handle nil injection, maybe log a warning or panic
		// depending on whether the dependency is strictly required.
		// For now, we allow it, but Get will fail later if client is nil.
		return
	}
	c.client = client
}

// createChatCompletion is the resolver for the chat completion node.
type createChatCompletion struct {
	// Embed the initializer to hold the injected dependency.
	// Use a pointer to allow lazy initialization in Init().
	initializer *createChatCompletionInitializer
}

// Init sets up the initializer for dependency injection.
// This is called during the definition phase (specifically by definition.Start).
func (c *createChatCompletion) Init() heart.NodeInitializer {
	// Lazily create the initializer only when Init is called.
	if c.initializer == nil {
		c.initializer = &createChatCompletionInitializer{}
	}
	return c.initializer
}

// Get executes the OpenAI API call using the interface.
// This is called during the execution phase.
func (c *createChatCompletion) Get(ctx context.Context, in openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// Ensure initializer and client were set up correctly via Init and DependencyInject.
	if c.initializer == nil || c.initializer.client == nil {
		// Use the standard error defined in the heart package.
		return openai.ChatCompletionResponse{}, heart.ErrDependencyNotSet
	}
	// Call the method via the interface - this works for both real and mock clients.
	return c.initializer.client.CreateChatCompletion(ctx, in)
}

// --- Compile-time Interface Checks ---

// Ensure the resolver implements NodeResolver.
var _ heart.NodeResolver[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*createChatCompletion)(nil)

// Ensure the initializer implements required interfaces (including the updated DependencyInjectable).
var _ heart.NodeInitializer = (*createChatCompletionInitializer)(nil)

// Ensure it implements DependencyInjectable with the *interface* type.
var _ heart.DependencyInjectable[clientiface.ClientInterface] = (*createChatCompletionInitializer)(nil) // Use interface
