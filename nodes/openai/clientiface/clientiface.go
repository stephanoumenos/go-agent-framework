package clientiface

import (
	"context"

	openai "github.com/sashabaranov/go-openai"
)

// ClientInterface defines the OpenAI client methods used by heart nodes.
// By placing it here, other openai subpackages can import it without cycles.
type ClientInterface interface {
	CreateChatCompletion(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
	// Add other methods from openai.Client if your nodes use them
}
