// ./nodes/openai/dependencyinjection.go
package openai

import (
	// remove context import if ClientInterface doesn't need it directly here
	"heart"
	"heart/nodes/openai/clientiface" // <-- IMPORT the new interface package
	"heart/nodes/openai/internal"

	"github.com/mark3labs/mcp-go/client"
	// openaimiddleware "heart/nodes/openai/middleware"
	// Don't need the main openai import here if ClientInterface is defined elsewhere
	// openai "github.com/sashabaranov/go-openai"
)

// Inject now accepts the interface from the new package
func Inject(client clientiface.ClientInterface) heart.DependencyInjector { // Use clientiface.ClientInterface
	// Adjust type arguments for NodesDependencyInject
	return heart.NodesDependencyInject( // Use clientiface.ClientInterface
		client,
		func(c clientiface.ClientInterface) clientiface.ClientInterface { return c },
		// Initializers list remains the same, but they must now expect clientiface.ClientInterface
		&createChatCompletionInitializer{},
	)
}

// No need for the ClientInterface definition here anymore
func InjectMCPClient(_client client.MCPClient) heart.DependencyInjector {
	return heart.NodesDependencyInject(
		_client,
		func(c client.MCPClient) client.MCPClient { return c },
		// Initializers list remains the same, but they must now expect clientiface.ClientInterface
		&internal.CallToolInitializer{},
		&internal.ListToolsInitializer{},
	)
}
