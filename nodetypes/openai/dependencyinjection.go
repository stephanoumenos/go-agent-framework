package openai

import (
	"heart"

	openai "github.com/sashabaranov/go-openai"
)

func Init(client *openai.Client) heart.NodeConstructor {
	return heart.DefineNodeConstructor(client, func(c *openai.Client) *openai.Client { return c }, &structuredOutputDefiner[any]{})
}
