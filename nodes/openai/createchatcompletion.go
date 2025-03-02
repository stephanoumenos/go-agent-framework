package openai

import (
	"context"
	"heart"

	openai "github.com/sashabaranov/go-openai"
)

const createChatCompletionNodeTypeID heart.NodeTypeID = "openai:createChatCompletion"

func CreateChatCompletion(nodeID heart.NodeID) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	return heart.DefineNode(nodeID, &createChatCompletion{})
}

type createChatCompletionInitializer struct {
	client *openai.Client
}

func (c *createChatCompletionInitializer) ID() heart.NodeTypeID {
	return createChatCompletionNodeTypeID
}

func (c *createChatCompletionInitializer) DependencyInject(client *openai.Client) {
	c.client = client
}

type createChatCompletion struct {
	*createChatCompletionInitializer
}

func (c *createChatCompletion) Init() heart.NodeInitializer {
	c.createChatCompletionInitializer = &createChatCompletionInitializer{}
	return c.createChatCompletionInitializer
}

func (c *createChatCompletion) Get(ctx context.Context, in openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return c.client.CreateChatCompletion(ctx, in)
}
