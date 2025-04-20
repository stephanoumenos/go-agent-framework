// ./nodes/openai/createchatcompletion.go
package openai

import (
	"context"
	"errors"

	"heart"
	"heart/nodes/openai/clientiface"

	openai "github.com/sashabaranov/go-openai"
)

// createChatCompletionNodeTypeID is the NodeTypeID used for dependency injection
// lookup for the CreateChatCompletion node.
const createChatCompletionNodeTypeID heart.NodeTypeID = "openai:createChatCompletion"

// ErrDependencyNotSet indicates that the OpenAI client dependency was required but
// not provided via heart.Dependencies(openai.Inject(client)) during setup.
var ErrDependencyNotSet = errors.New("OpenAI client not provided. Use heart.Dependencies(openai.Inject(client)) to provide it")

// CreateChatCompletion defines a node blueprint that calls the OpenAI CreateChatCompletion API endpoint.
// It returns a NodeDefinition which can be used within workflows.
//
// Parameters:
//   - nodeID: A heart.NodeID that uniquely identifies this node blueprint within its definition scope.
func CreateChatCompletion(nodeID heart.NodeID) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	// Call the updated DefineNode which only takes nodeID and the resolver.
	return heart.DefineNode(nodeID, &createChatCompletion{})
}

// createChatCompletionInitializer handles dependency injection for the chat completion node.
// It stores the injected client (as a clientiface.ClientInterface) and provides
// the necessary NodeTypeID for the injection mechanism.
type createChatCompletionInitializer struct {
	// Store the client interface.
	client clientiface.ClientInterface // Use the interface type
}

// ID returns the NodeTypeID (`openai:createChatCompletion`) for this initializer,
// allowing the dependency injection system to associate it with the correct dependency.
func (c *createChatCompletionInitializer) ID() heart.NodeTypeID {
	return createChatCompletionNodeTypeID
}

// DependencyInject receives the OpenAI client dependency (as a clientiface.ClientInterface)
// and stores it in the initializer. This method is called by the framework
// during the `Start` phase of the node definition.
func (c *createChatCompletionInitializer) DependencyInject(client clientiface.ClientInterface) { // Accept the interface
	if client == nil {
		// If a nil client is injected, Get will fail later with ErrDependencyNotSet.
		// Log a warning or handle as appropriate for your application's strictness.
		return
	}
	c.client = client
}

// createChatCompletion is the NodeResolver for the chat completion node.
// It holds the initializer (which contains the injected client) and implements
// the logic for calling the OpenAI API in its Get method.
type createChatCompletion struct {
	// Embed a pointer to the initializer to hold the injected dependency.
	// The initializer is created lazily during the Init() phase.
	initializer *createChatCompletionInitializer
}

// Init initializes the node resolver. It's called by the framework during the
// definition's `Start` phase (before execution). It creates the associated
// `createChatCompletionInitializer` instance, which will then be used for
// dependency injection. Returns the initializer instance.
func (c *createChatCompletion) Init() heart.NodeInitializer {
	// Lazily create the initializer only when Init is called.
	if c.initializer == nil {
		c.initializer = &createChatCompletionInitializer{}
	}
	return c.initializer
}

// Get executes the OpenAI CreateChatCompletion API call using the injected client interface.
// It's called by the framework during the execution phase when this node's result is needed.
// It takes the standard Go context and the input `openai.ChatCompletionRequest`.
// Returns the `openai.ChatCompletionResponse` or an error (e.g., ErrDependencyNotSet if
// the client wasn't injected, or an error from the API call itself).
func (c *createChatCompletion) Get(ctx context.Context, in openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// Ensure initializer and client were set up correctly via Init and DependencyInject.
	if c.initializer == nil || c.initializer.client == nil {
		return openai.ChatCompletionResponse{}, ErrDependencyNotSet
	}
	// Call the method via the interface - this works for both real (*openai.Client) and mock clients.
	return c.initializer.client.CreateChatCompletion(ctx, in)
}

// --- Compile-time Interface Checks ---

// Ensures createChatCompletion implements NodeResolver.
var _ heart.NodeResolver[openai.ChatCompletionRequest, openai.ChatCompletionResponse] = (*createChatCompletion)(nil)

// Ensures createChatCompletionInitializer implements NodeInitializer.
var _ heart.NodeInitializer = (*createChatCompletionInitializer)(nil)

// Ensures createChatCompletionInitializer implements DependencyInjectable with the client interface type.
var _ heart.DependencyInjectable[clientiface.ClientInterface] = (*createChatCompletionInitializer)(nil)
