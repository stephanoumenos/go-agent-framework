package openai

import (
	"heart"

	openai "github.com/sashabaranov/go-openai"
)

func Inject(client *openai.Client) heart.DependencyInjector {
	return heart.NodesDependencyInject(client, func(c *openai.Client) *openai.Client { return c }, &createChatCompletionInitializer{})
}
