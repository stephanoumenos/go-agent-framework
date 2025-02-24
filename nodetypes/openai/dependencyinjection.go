package openai

import (
	"heart"

	openai "github.com/sashabaranov/go-openai"
)

func Init(client *openai.Client) error {
	return heart.DependencyInject(client, structuredOutputNodeTypeID)
}
