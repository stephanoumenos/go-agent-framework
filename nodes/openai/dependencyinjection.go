// ./nodes/openai/dependencyinjection.go
package openai

import (
	"heart"
	"heart/nodes/openai/clientiface"
	"heart/nodes/openai/internal"

	mcpclient "github.com/mark3labs/mcp-go/client"
)

// Inject creates a DependencyInjector for the OpenAI client.
// It takes an object implementing the clientiface.ClientInterface (like a real
// *openai.Client or a mock) and makes it available for injection into
// nodes that require it, such as CreateChatCompletion.
//
// Use this function with heart.Dependencies during application setup.
func Inject(client clientiface.ClientInterface) heart.DependencyInjector {
	return heart.NodesDependencyInject(
		client,
		// The constructor function simply returns the provided client.
		func(c clientiface.ClientInterface) clientiface.ClientInterface { return c },
		// List of initializers that will receive the client dependency.
		&createChatCompletionInitializer{},
	)
}

// InjectMCPClient creates a DependencyInjector for the MCP client.
// It takes an object implementing the mcpclient.MCPClient interface and
// makes it available for injection into internal nodes used by the
// WithMCP middleware (e.g., ListTools, CallTool).
//
// Use this function with heart.Dependencies during application setup if using
// the WithMCP middleware.
func InjectMCPClient(_client mcpclient.MCPClient) heart.DependencyInjector {
	return heart.NodesDependencyInject(
		_client,
		// The constructor function simply returns the provided client.
		func(c mcpclient.MCPClient) mcpclient.MCPClient { return c },
		// List of internal initializers that will receive the MCP client dependency.
		&internal.CallToolInitializer{},
		&internal.ListToolsInitializer{},
	)
}
