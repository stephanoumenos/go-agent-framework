// ./nodes/openai/createchatcompletion.go
package openai

import (
	"context"
	"heart"
	"heart/nodes/openai/clientiface"

	openai "github.com/sashabaranov/go-openai"
)

const createChatCompletionNodeTypeID heart.NodeTypeID = "openai:createChatCompletion"

// CreateChatCompletion defines a node that calls the OpenAI CreateChatCompletion API endpoint.
func CreateChatCompletion(ctx heart.Context, nodeID heart.NodeID) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	return heart.DefineNode(ctx, nodeID, &createChatCompletion{})
}

// createChatCompletionInitializer handles dependency injection for the chat completion node.
type createChatCompletionInitializer struct {
	// Store the client *interface*
	client clientiface.ClientInterface // CHANGED: Use the interface type
}

// ID returns the type identifier for this initializer.
func (c *createChatCompletionInitializer) ID() heart.NodeTypeID {
	return createChatCompletionNodeTypeID
}

// DependencyInject receives the OpenAI client *interface*.
func (c *createChatCompletionInitializer) DependencyInject(client clientiface.ClientInterface) { // CHANGED: Accept the interface
	c.client = client
}

// createChatCompletion is the resolver for the chat completion node.
type createChatCompletion struct {
	// Embed the initializer to hold the injected dependency.
	*createChatCompletionInitializer
}

// Init sets up the initializer for dependency injection.
func (c *createChatCompletion) Init() heart.NodeInitializer {
	c.createChatCompletionInitializer = &createChatCompletionInitializer{}
	return c.createChatCompletionInitializer
}

// Get executes the OpenAI API call using the interface.
func (c *createChatCompletion) Get(ctx context.Context, in openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if c.createChatCompletionInitializer == nil || c.client == nil {
		// Use the existing error or define a more specific one
		return openai.ChatCompletionResponse{}, heart.ErrDependencyNotSet
	}
	// Call the method via the interface - this works for both real and mock clients
	return c.client.CreateChatCompletion(ctx, in)
}

// Ensure the resolver implements NodeResolver.
var _ heart.NodeResolver[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*createChatCompletion)(nil)

// Ensure the initializer implements required interfaces (including the updated DependencyInjectable).
var _ heart.NodeInitializer = (*createChatCompletionInitializer)(nil)

// Ensure it implements DependencyInjectable with the *interface* type
var _ heart.DependencyInjectable[clientiface.ClientInterface] = (*createChatCompletionInitializer)(nil) // CHANGED: Use interface
