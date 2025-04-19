// ./nodes/openai/clientiface/clientiface.go
package clientiface

import (
	"context"

	openai "github.com/sashabaranov/go-openai"
)

// ClientInterface defines the set of OpenAI client methods used by the
// heart/nodes/openai package and its subpackages.
//
// This interface serves two primary purposes:
//  1. Breaking import cycles: Allows nodes (like CreateChatCompletion) and
//     dependency injection logic to refer to the required client methods without
//     directly importing the full `openai` package structure where dependencies
//     might be defined, preventing circular imports.
//  2. Mocking for tests: Enables easy mocking of the OpenAI client during unit
//     and integration testing by providing a mock implementation of this interface.
//
// Implementations of this interface include the real *openai.Client from the
// github.com/sashabaranov/go-openai library and mock clients used in tests.
type ClientInterface interface {
	// CreateChatCompletion corresponds to the method in the go-openai client.
	CreateChatCompletion(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
	// Add other methods from openai.Client here if other heart/nodes/openai
	// components require them (e.g., CreateEmbedding, CreateImage).
}
