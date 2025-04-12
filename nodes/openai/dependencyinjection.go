// ./nodes/openai/dependencyinjection.go
package openai

import (
	// remove context import if ClientInterface doesn't need it directly here
	"heart"
	"heart/nodes/openai/clientiface" // <-- IMPORT the new interface package
	// openaimiddleware "heart/nodes/openai/middleware"
	// Don't need the main openai import here if ClientInterface is defined elsewhere
	// openai "github.com/sashabaranov/go-openai"
)

// Inject now accepts the interface from the new package
func Inject(client clientiface.ClientInterface) heart.DependencyInjector { // Use clientiface.ClientInterface
	// Adjust type arguments for NodesDependencyInject
	return heart.NodesDependencyInject[clientiface.ClientInterface, clientiface.ClientInterface]( // Use clientiface.ClientInterface
		client,
		func(c clientiface.ClientInterface) clientiface.ClientInterface { return c },
		// Initializers list remains the same, but they must now expect clientiface.ClientInterface
		&createChatCompletionInitializer{},
		// &openaimiddleware.ToolsMiddlewareInitializer{}, TODO: Add this back
	)
}

// No need for the ClientInterface definition here anymore
