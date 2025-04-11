// ./nodes/openai/createchatcompletion.go
package openai

import (
	"context"
	"heart"

	openai "github.com/sashabaranov/go-openai"
)

const createChatCompletionNodeTypeID heart.NodeTypeID = "openai:createChatCompletion"

// CreateChatCompletion defines a node that calls the OpenAI CreateChatCompletion API endpoint.
func CreateChatCompletion(ctx heart.Context, nodeID heart.NodeID) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	return heart.DefineNode(ctx, nodeID, &createChatCompletion{})
}

// createChatCompletionInitializer handles dependency injection for the chat completion node.
type createChatCompletionInitializer struct {
	client *openai.Client
}

// ID returns the type identifier for this initializer.
func (c *createChatCompletionInitializer) ID() heart.NodeTypeID {
	return createChatCompletionNodeTypeID
}

// DependencyInject receives the OpenAI client.
func (c *createChatCompletionInitializer) DependencyInject(client *openai.Client) {
	c.client = client
}

// createChatCompletion is the resolver for the chat completion node.
type createChatCompletion struct {
	// Embed the initializer to hold the injected dependency.
	*createChatCompletionInitializer
}

// Init sets up the initializer for dependency injection.
func (c *createChatCompletion) Init() heart.NodeInitializer {
	// Create the initializer instance when the node is initialized.
	c.createChatCompletionInitializer = &createChatCompletionInitializer{}
	return c.createChatCompletionInitializer
}

// Get executes the OpenAI API call.
func (c *createChatCompletion) Get(ctx context.Context, in openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// Check if the client was injected correctly.
	if c.createChatCompletionInitializer == nil || c.client == nil {
		// This should ideally not happen if DI setup is correct.
		return openai.ChatCompletionResponse{}, heart.ErrDependencyNotSet // TODO: Define this error
	}
	return c.client.CreateChatCompletion(ctx, in)
}

// Ensure the resolver implements NodeResolver.
var _ heart.NodeResolver[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*createChatCompletion)(nil)

// Ensure the initializer implements required interfaces.
var _ heart.NodeInitializer = (*createChatCompletionInitializer)(nil)
var _ heart.DependencyInjectable[*openai.Client] = (*createChatCompletionInitializer)(nil)
