// ./nodes/openai/dependencyinjection.go
package openai

import (
	"heart"
	openaimiddleware "heart/nodes/openai/middleware" // Import middleware package

	openai "github.com/sashabaranov/go-openai"
)

// Inject registers the OpenAI client for all relevant initializers in this package scope.
func Inject(client *openai.Client) heart.DependencyInjector {
	// Register the *openai.Client dependency for all nodes/initializers that need it.
	return heart.NodesDependencyInject(client, func(c *openai.Client) *openai.Client { return c },
		// List all initializers requiring *openai.Client:
		&createChatCompletionInitializer{},
		&openaimiddleware.ToolsMiddlewareInitializer{},
		// Add any other future initializers from the openai package/subpackages here...
	)
}
