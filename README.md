# go-agent-framework

[![Go Reference](https://pkg.go.dev/badge/github.com/stephanoumenos/go-agent-framework.svg)](https://pkg.go.dev/github.com/stephanoumenos/go-agent-framework) [![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

`go-agent-framework` is a **statically-typed framework** to build composable AI agents in Go.

Define your workflows using standard Go code, including its powerful concurrency primitives. The execution graph will be implicitly resolved.

Let `go-agent-framework` manage concurrency, dependency resolution, persistence, and error handling with a clean interface.

_Note: The framework implicitly handles concurrency for independent workflow branches. Advanced concurrency patterns like fan-in/fan-out within a workflow step can be achieved using `gaf.NewNode` and `gaf.FanIn`, detailed in the Concepts and Advanced Usage sections._

```go
package main

import (
	"context"
	"fmt"
	"os"

	gaf "[github.com/stephanoumenos/go-agent-framework](https://github.com/stephanoumenos/go-agent-framework)"
	openai "[github.com/stephanoumenos/go-agent-framework/nodes/openai](https://github.com/stephanoumenos/go-agent-framework/nodes/openai)"
	openaimw "[github.com/stephanoumenos/go-agent-framework/nodes/openai/middleware](https://github.com/stephanoumenos/go-agent-framework/nodes/openai/middleware)"
	store "[github.com/stephanoumenos/go-agent-framework/store](https://github.com/stephanoumenos/go-agent-framework/store)"

	goopenai "[github.com/sashabaranov/go-openai](https://github.com/sashabaranov/go-openai)"
)

type Recipe struct {
	RecipeName  string   `json:"recipe_name"`
	Ingredients []string `json:"ingredients"`
	Steps       []string `json:"steps"`
	PrepTime    string   `json:"prep_time"`
}

// recipeWorkflowHandler defines the logic for generating a recipe.
func recipeWorkflowHandler(ctx gaf.Context, topic string) gaf.ExecutionHandle[Recipe] {
	// Define the base LLM node.
	llmNodeDef := openai.CreateChatCompletion("generate_recipe_json")

	// Use middleware to enforce structured JSON output matching the Recipe struct.
	// The middleware wraps the base node definition.
	structuredOutputNodeDef := openaimw.WithStructuredOutput[Recipe](llmNodeDef)

	// Prepare the request for the *structured output* node.
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			// CRITICAL: System prompt instructing the model to use the 'output' schema.
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a helpful recipe assistant. You MUST output a JSON object that strictly adheres to the JSON schema provided under the name 'output'. Do not include any text outside of the JSON object."},
			{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a recipe for: %s", topic)},
		},
	}

	// Start the structured output node, providing the request via gaf.Into().
	// This returns a handle that will resolve to the parsed Recipe struct.
	return structuredOutputNodeDef.Start(gaf.Into(request))
}

// Define the reusable workflow blueprint.
var recipeWorkflow = gaf.WorkflowFromFunc(gaf.NodeID("recipeGenerator"), recipeWorkflowHandler)

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}
	client := goopenai.NewClient(apiKey)

	// Inject the OpenAI client dependency for nodes that require it.
	err := gaf.Dependencies(openai.Inject(client))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}
	defer gaf.ResetDependencies() // Good practice, especially for tests

	// Setup persistence (optional, defaults to in-memory).
	fileStore, err := store.NewFileStore("./workflow_runs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file store: %v\n", err)
		os.Exit(1)
	}

	// Define the input for this specific workflow run.
	workflowInput := "quick vegan chocolate chip cookies"

	// Start the workflow definition (creates the initial execution handle, still lazy).
	workflowHandle := recipeWorkflow.Start(gaf.Into(workflowInput))

	// Execute triggers the lazy computation graph and waits for the final result.
	ctx := context.Background() // Or context.WithTimeout, etc.
	recipeResult, err := gaf.Execute(ctx, workflowHandle, gaf.WithStore(fileStore))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		os.Exit(1)
	}

	// Use your statically-typed recipe!
	fmt.Printf("\nGenerated Recipe: %s (%s)\n", recipeResult.RecipeName, recipeResult.PrepTime)
	fmt.Println("Ingredients:")
	for _, ing := range recipeResult.Ingredients {
		fmt.Printf("- %s\n", ing)
	}
	fmt.Println("Steps:")
	for i, step := range recipeResult.Steps {
		fmt.Printf("%d. %s\n", i+1, step)
	}
}
```

## Features

- **🚀 Type Safe:** Leverage Go generics (`NodeDefinition[In, Out]`, `ExecutionHandle[Out]`) for end-to-end type safety at compile time.
- **🧩 Composable:** Define atomic units of work (`NodeDefinition`) using `gaf.DefineNode` and a `NodeResolver`. Compose them into complex workflows using standard Go functions (`gaf.WorkflowFromFunc`, `gaf.NewNode`). No YAML or external DSL needed.
- **⚡ Implicit Graphs:** Workflows are defined as graphs of `ExecutionHandle`s. Execution is triggered lazily. Independent branches of the graph run concurrently automatically once their inputs are ready.
- **💉 Dependency Injection:** Inject dependencies (API clients, DB connections, etc.) into your nodes using `gaf.Dependencies`. Supports generic `DependencyInjectable[Dep]` interface.
- **🤖 Structured Output Middleware:** Includes `WithStructuredOutput` middleware for OpenAI nodes to enforce JSON output conforming to a specified Go struct, automatically handling schema generation and response parsing based on the provided type.
- **🛠️ MCP Tool Integration:** Seamlessly integrate with the [MCP (Multi-Capability Protocol)](https://github.com/mark3labs/mcp-go) ecosystem.
  - Turn _any_ `gaf.NodeDefinition` (atomic node or complex workflow) into an MCP tool using the `mcp.IntoTool` adapter function from the `github.com/stephanoumenos/go-agent-framework/mcp` package.
  - Use the `WithMCP` middleware (from `nodes/openai/middleware`) for OpenAI nodes to automatically handle tool listing and calls via an injected MCP client.
- **💾 Persistence:** Automatically save workflow state (node status, errors) and input/output content using an extensible `store.Store` interface (`FileStore`, `MemoryStore` provided). Managed via `gaf.WithStore`.
- **🧪 Testable:** Dependency injection makes mocking external services (LLMs, MCP clients, databases) straightforward using interfaces (e.g., `clientiface.ClientInterface` from `nodes/openai/clientiface`, `mcpclient.MCPClient`) and mock implementations. The `gaf.ResetDependencies()` function aids test isolation.
- **🌐 Extensible:** Define custom `NodeDefinition`s by implementing the `NodeResolver` interface to integrate any Go library, API, or internal service into your workflows.

## Why go-agent-framework?

Building robust AI agents often involves orchestrating multiple LLM calls, using external tools/APIs, interacting with data sources, parsing outputs, and managing complex control flow. `go-agent-framework` addresses these challenges within the Go ecosystem by:

- **Prioritizing Go Idioms:** Leverages Go's strengths – strong typing via generics, interfaces, built-in concurrency – rather than relying on external configuration languages or dynamically typed systems.
- **Compile-Time Confidence:** Generic `NodeDefinition`s and `ExecutionHandle`s ensure that the inputs and outputs of your workflow steps match up, drastically reducing runtime errors common in agent orchestration.
- **Developer Experience:** Define, test, and refactor workflows using familiar Go code and tooling. The structure encourages modularity and clarity.
- **Performance:** Designed for efficient execution by leveraging Go's concurrency model and lazy evaluation, minimizing unnecessary overhead.

## Installation

```bash
go get [github.com/stephanoumenos/go-agent-framework](https://github.com/stephanoumenos/go-agent-framework)
```

_(Note: You'll typically import the root package `github.com/stephanoumenos/go-agent-framework` aliased as `gaf`, and specific sub-packages like `.../nodes/openai`, `.../store`, etc. as needed in your code.)_

## 🚀 Quick Start

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	gaf "[github.com/stephanoumenos/go-agent-framework](https://github.com/stephanoumenos/go-agent-framework)"
	store "[github.com/stephanoumenos/go-agent-framework/store](https://github.com/stephanoumenos/go-agent-framework/store)"
)

// 1. Define Node Logic (NodeResolver + NodeInitializer)

// uppercaseResolver implements the core logic
type uppercaseResolver struct{}

// uppercaseInitializer provides metadata and handles dependencies (if any)
type uppercaseInitializer struct{} // Implements gaf.NodeInitializer

// ID provides a unique identifier for this node type, used for dependency matching.
func (i *uppercaseInitializer) ID() gaf.NodeTypeID { return "example:uppercase" }

// Init returns the initializer instance.
func (r *uppercaseResolver) Init() gaf.NodeInitializer { return &uppercaseInitializer{} }

// Get contains the actual work the node performs.
func (r *uppercaseResolver) Get(_ context.Context, in string) (string, error) {
	return strings.ToUpper(in), nil
}

// Define the reusable node blueprint using gaf.DefineNode
var UppercaseNode = gaf.DefineNode(gaf.NodeID("uppercase"), &uppercaseResolver{})

// 2. Define a Workflow using the Node
func simpleWorkflowHandler(ctx gaf.Context, input string) gaf.ExecutionHandle[string] {
	// Create an input handle (immediately resolved) using gaf.Into
	inputHandle := gaf.Into(input)

	// Start the uppercase node, passing the input handle (lazy execution)
	uppercaseHandle := UppercaseNode.Start(inputHandle)

	// The handle returned here defines the workflow's final output
	return uppercaseHandle
}

// Define the reusable workflow blueprint using gaf.WorkflowFromFunc
var SimpleWorkflow = gaf.WorkflowFromFunc(gaf.NodeID("simpleWorkflow"), simpleWorkflowHandler)

func main() {
	// 3. Setup Store (optional, defaults to MemoryStore)
	memStore := store.NewMemoryStore()

	// 4. Execute the Workflow
	ctx := context.Background()
	fileStore, _ := store.NewFileStore("workflow_data") // Example store
	input := "some input string"

	// Execute the workflow directly
	output, err := gaf.ExecuteWorkflow(ctx, SimpleWorkflow, input, gaf.WithStore(fileStore))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow failed: %v\n", err)
		return
	}

	fmt.Printf("Input: %s\n", input)
	fmt.Printf("Result: %s\n", output) // Output: HELLO WORLD
}
```

## Concepts

- **`NodeDefinition[In, Out]`**: A reusable, immutable blueprint for a unit of work (atomic node or composite workflow). Created via `gaf.DefineNode` or `gaf.WorkflowFromFunc`. Contains a base `NodeID`.
- **`NodeResolver[In, Out]`**: Implements the core logic (`Get`) and initialization (`Init`) for an _atomic_ node definition.
- **`NodeInitializer`**: Returned by `Init()`. Provides a `NodeTypeID` used for matching dependencies during injection. Can implement `DependencyInjectable[Dep]`.
- **`ExecutionHandle[Out]`**: A lightweight handle representing a future result of type `Out` for a specific node/workflow _instance_ in the graph. Obtained by calling `Start()` on a `NodeDefinition` _within_ a workflow handler or `NewNode` function. Acts as input for downstream nodes. It embodies the lazy execution model. Each handle instance gets a unique `NodePath` (e.g., `/workflowA:#0/nodeB:#1`) during execution.
- **`gaf.Execute`**: The top-level function to trigger the execution of a graph defined by a root `ExecutionHandle`. Manages the run context (`Context`), execution registry, persistence, and waits for the final result.
- **`gaf.ExecuteWorkflow`**: The top-level function to trigger the execution of a `WorkflowDefinition`. It takes the workflow definition itself and the direct input value. Manages the run context (`Context`), execution registry, persistence, and waits for the final result.
- **`gaf.Context`**: Passed to `WorkflowHandlerFunc` and `NewNode` functions. Provides access to the workflow's execution scope (UUID, BasePath, registry), the cancellable Go `context.Context`, and the `store.Store`.
- **`gaf.NewNode`**: Allows defining anonymous, inline workflow steps (subgraphs) within a `WorkflowHandlerFunc`. Enables complex dynamic branching and joining logic. Receives `NewNodeContext`.
- **`gaf.FanIn`**: Used within `gaf.NewNode` functions to concurrently await results from one or more dependency `ExecutionHandle`s, returning a `Future[T]`. Useful for implementing fan-in/join patterns explicitly.
- **`Future[T]`**: Returned by `FanIn`. Represents the eventual result of a dependency within a `NewNode`. Use `future.Get()` to retrieve the value or error, blocking until ready.
- **Dependency Injection**: Nodes declare dependencies by having their `NodeInitializer` implement `DependencyInjectable[Dep]`. Dependencies (e.g., clients) are registered globally via `gaf.Dependencies(constructor ...)` using `DependencyInjector`s (often created via helpers like `openai.Inject` or `gaf.NodesDependencyInject`). Injection happens automatically during `NodeDefinition.Start()`.
- **`store.Store`**: Interface (`store.Store`) for persisting workflow execution state. Pass implementations (`MemoryStore`, `FileStore`) via `gaf.WithStore(...)` during `Execute`.

## Advanced Usage

### Structured Output with OpenAI

```go
package main // Assume necessary imports are present

import (
	"context"
	"fmt"
	// ... other std lib imports

	gaf "[github.com/stephanoumenos/go-agent-framework](https://github.com/stephanoumenos/go-agent-framework)"
	openai "[github.com/stephanoumenos/go-agent-framework/nodes/openai](https://github.com/stephanoumenos/go-agent-framework/nodes/openai)"
	openaimw "[github.com/stephanoumenos/go-agent-framework/nodes/openai/middleware](https://github.com/stephanoumenos/go-agent-framework/nodes/openai/middleware)"

	goopenai "[github.com/sashabaranov/go-openai](https://github.com/sashabaranov/go-openai)"
)

// Define your Go struct
type MyData struct {
	Name   string `json:"name"`
	Values []int  `json:"values"`
}

// Inside your workflow handler:
func myWorkflowHandler(ctx gaf.Context, prompt string) gaf.ExecutionHandle[MyData] {
	llmNodeDef := openai.CreateChatCompletion("call_openai_for_data")

	// Wrap the LLM node def with the middleware, specifying the target Go type
	structuredNodeDef := openaimw.WithStructuredOutput[MyData](llmNodeDef)

	// Prepare request (CRITICAL: Prompt must instruct model to use JSON Schema 'output')
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Or other capable model
		Messages: []goopenai.ChatCompletionMessage{
			// The middleware hardcodes the schema name to "output"
			{Role: goopenai.ChatMessageRoleSystem, Content: "You MUST respond ONLY with a JSON object matching the 'output' schema."},
			{Role: goopenai.ChatMessageRoleUser, Content: prompt},
		},
		// ResponseFormat is set automatically by the middleware to enforce JSON mode
	}

	// Start returns ExecutionHandle[MyData]
	handle := structuredNodeDef.Start(gaf.Into(request))
	return handle
}

// Define the workflow
var myWorkflow = gaf.WorkflowFromFunc(gaf.NodeID("myWorkflow"), myWorkflowHandler)

// func main() {
//   // Setup dependencies (OpenAI client) using gaf.Dependencies(openai.Inject(client))
//   // Define input: myInput := "Generate data for 'example' with values [1, 2, 3]"
//   // Start workflow: myWorkflowHandle := myWorkflow.Start(gaf.Into(myInput))
//   // Execute `myWorkflow`... result will be MyData struct or error.
//   // dataResult, err := gaf.Execute(ctx, myWorkflowHandle, ...)
// }
```

### MCP Tool Integration

Turn any `gaf` node (even complex workflows) into an MCP tool and use the `WithMCP` middleware for OpenAI nodes to handle tool calls.

```go
package main // Assume necessary imports are present

import (
	"context"
	"fmt"
	// ... other std lib imports

	gaf "[github.com/stephanoumenos/go-agent-framework](https://github.com/stephanoumenos/go-agent-framework)"
	mcp_adapter "[github.com/stephanoumenos/go-agent-framework/mcp](https://github.com/stephanoumenos/go-agent-framework/mcp)"                 // Adapter package
	openai "[github.com/stephanoumenos/go-agent-framework/nodes/openai](https://github.com/stephanoumenos/go-agent-framework/nodes/openai)"           // OpenAI nodes
	openaimw "[github.com/stephanoumenos/go-agent-framework/nodes/openai/middleware](https://github.com/stephanoumenos/go-agent-framework/nodes/openai/middleware)" // OpenAI middleware

	mcpclient "[github.com/mark3labs/mcp-go/client](https://github.com/mark3labs/mcp-go/client)"
	mcpschema "[github.com/mark3labs/mcp-go/mcp](https://github.com/mark3labs/mcp-go/mcp)"
	mcpserver "[github.com/mark3labs/mcp-go/server](https://github.com/mark3labs/mcp-go/server)"
	goopenai "[github.com/sashabaranov/go-openai](https://github.com/sashabaranov/go-openai)"
)

// Assume AddNode is a gaf.NodeDefinition[AddInput, AddOutput]
type AddInput struct { A, B int }
type AddOutput struct { Result int }
// var addNodeDef = gaf.DefineNode(...) // Define your node

// 1. Define MCP Schema for the tool
var addToolSchema = mcpschema.Tool{
	Name:        "add_numbers",
	Description: "Adds two integer numbers.",
	InputSchema: mcpschema.ToolInputSchema{
		Type: mcpschema.InputSchemaTypeObject,
		Properties: map[string]mcpschema.Property{
			"A": {Type: mcpschema.PropertyTypeInteger, Description: "First number"},
			"B": {Type: mcpschema.PropertyTypeInteger, Description: "Second number"},
		},
		Required: []string{"A", "B"},
	},
}

// 2. Define Mapper Functions (MCP Request -> Node Input, Node Output -> MCP Result)
func mapMCReqToAddInput(ctx context.Context, req mcpschema.CallToolRequest) (AddInput, error) {
	var input AddInput
	// Example: Extract arguments (replace with robust JSON parsing/validation)
	if a, ok := req.Params.Arguments["A"].(float64); ok { // JSON numbers often decode as float64
		input.A = int(a)
	} else { /* handle error */
		return input, fmt.Errorf("invalid or missing argument 'A'")
	}
	if b, ok := req.Params.Arguments["B"].(float64); ok {
		input.B = int(b)
	} else { /* handle error */
		return input, fmt.Errorf("invalid or missing argument 'B'")
	}
	return input, nil
}

func mapAddOutputToMCPResult(ctx context.Context, out AddOutput) (*mcpschema.CallToolResult, error) {
	return &mcpschema.CallToolResult{
		Content: []mcpschema.Content{
			{Type: mcpschema.ContentTypeText, Text: fmt.Sprintf("The sum is %d", out.Result)},
		},
	}, nil
}

// 3. Adapt the gaf NodeDefinition into an MCPTool
/*
// Assume addNodeDef is defined elsewhere:
// var addNodeDef = gaf.DefineNode(...)
adaptedTool := mcp_adapter.IntoTool(
	addNodeDef,              // Your gaf NodeDefinition
	addToolSchema,           // MCP schema
	mapMCReqToAddInput,      // Mapper: MCP Request -> Node Input
	mapAddOutputToMCPResult, // Mapper: Node Output -> MCP Result
)
*/

// 4. Add the adapted tool to an MCP Server
// (Setup mcpServer instance from [github.com/mark3labs/mcp-go/server](https://github.com/mark3labs/mcp-go/server))
// mcpServer := mcpserver.NewMCPServer(...)
// mcp_adapter.AddTools(mcpServer, adaptedTool) // Use adapter's helper

// 5. Inject MCP Client into go-agent-framework Workflows
// (Setup openaiClient and mcpClient connected to the server)
// openaiClient := goopenai.NewClient(...)
// mcpClient, _ := mcpclient.NewSSEMCPClient(/* server URL */)
// ctx := context.Background()
// mcpClient.Start(ctx) // Start client processing (in background)

// Inject *both* OpenAI and MCP clients for the middleware
/*
err := gaf.Dependencies(
	openai.Inject(openaiClient),       // For standard OpenAI calls
	openai.InjectMCPClient(mcpClient), // For the WithMCP middleware
)
if err != nil { // handle err... }
defer gaf.ResetDependencies()
*/

// 6. Using the WithMCP Middleware
// Wraps a standard OpenAI ChatCompletion node and handles the tool call loop.
func toolUsingWorkflowHandler(ctx gaf.Context, prompt string) gaf.ExecutionHandle[goopenai.ChatCompletionResponse] {
	// Define the node for the underlying LLM call
	llmNodeDef := openai.CreateChatCompletion("llm_call_with_tools")

	// Wrap the base LLM node definition with the MCP middleware
	// Requires MCPClient injected via openai.InjectMCPClient
	mcpEnabledNodeDef := openaimw.WithMCP(llmNodeDef)

	// Prepare the initial request
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Or model supporting tool calls
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "Use tools for calculations when needed."},
			{Role: goopenai.ChatMessageRoleUser, Content: prompt}, // e.g., "What is 5 + 7?"
		},
		// NOTE: Do not set Tools or ToolChoice manually here.
		// The middleware handles adding discovered tools from the MCP client
		// and setting ToolChoice="auto" initially.
	}

	// Start the MCP-wrapped node. The handle resolves to the final LLM response
	// after any necessary tool interactions.
	finalHandle := mcpEnabledNodeDef.Start(gaf.Into(request))
	return finalHandle
}

// Define the workflow
var toolUsingWorkflow = gaf.WorkflowFromFunc(gaf.NodeID("toolUsingWorkflow"), toolUsingWorkflowHandler)

// 7. Execute the workflow containing the WithMCP node...
// Ensure dependencies (OpenAI client, MCP Client) are injected first.
// workflowInput := "What is 123 + 456?"
// finalResultHandle := toolUsingWorkflow.Start(gaf.Into(workflowInput))
// result, err := gaf.Execute(ctx, finalResultHandle, ...)
// The final 'result' will contain the LLM's textual response after using the add_numbers tool.
```

### Persistence

Workflows automatically persist their execution state (node status, errors) and can optionally persist input/output content when a `store.Store` is provided via `gaf.WithStore`.

```go
package main // Assume necessary imports are present

import (
	"context"
	"fmt"
	// ... other imports
	gaf "[github.com/stephanoumenos/go-agent-framework](https://github.com/stephanoumenos/go-agent-framework)"
	store "[github.com/stephanoumenos/go-agent-framework/store](https://github.com/stephanoumenos/go-agent-framework/store)"
)

// Assume MyWorkflow is a defined gaf.WorkflowDefinition (like SimpleWorkflow above)
// var MyWorkflow = gaf.WorkflowFromFunc(...)

func main() {
	// Use FileStore for persistence across runs
	fileStore, err := store.NewFileStore("./my_workflow_runs")
	if err != nil {
		fmt.Printf("Error creating file store: %v\n", err)
		return
	}

	// Execute with the store
	ctx := context.Background()
	// Assume MyWorkflow takes a string input
	inputHandle := gaf.Into("some input data")
	handle := MyWorkflow.Start(inputHandle) // Get the root handle for your workflow run

	fmt.Println("Executing workflow with FileStore...")
	result, err := gaf.Execute(ctx, handle, gaf.WithStore(fileStore))
	if err != nil {
		fmt.Printf("First execution failed: %v\n", err)
		// If the process was interrupted, the state is saved.
	} else {
		fmt.Printf("First execution result: %v\n", result)
	}

	// If the process is interrupted and restarted, executing with the same store
	// allows the execution to potentially resume. Completed nodes load results
	// from the store, avoiding re-computation.

	fmt.Println("\nAttempting to resume workflow execution...")
	// Re-create the handle for the *same* logical workflow instance
	// Using the same input results in the same initial node handles
	resumeInputHandle := gaf.Into("some input data")
	resumeHandle := MyWorkflow.Start(resumeInputHandle)

	// Optionally provide the previous run ID if known (retrieved from logs/store listing)
	// previousRunID := handle.ExecutionID() // Get from the first run handle IF it completed Execute setup
	// If the first run failed *during* Execute setup, the ID might not be easily available
	// If ID is unknown, Execute will check the store for matching completed nodes based on path

	resumeResult, resumeErr := gaf.Execute(ctx, resumeHandle,
		gaf.WithStore(fileStore),
		// gaf.WithExistingUUID(previousRunID), // Optional: ensures resuming the exact run
	)
	if resumeErr != nil {
		fmt.Printf("Resumed execution failed: %v\n", resumeErr)
	} else {
		fmt.Printf("Resumed execution result: %v (potentially loaded from store)\n", resumeResult)
	}
}
```

See the `./examples` directory for more detailed usage patterns, including `FanIn` and testing setups.

## Philosophy

- **Static over Dynamic:** Prioritize compile-time safety and checking using Go generics.
- **Composability:** Build complex systems from simple, reusable, type-safe units (NodeDefinitions).
- **Idiomatic Go:** Leverage Go's built-in features (interfaces, concurrency, error handling) and common practices.
- **Explicitness:** Make dependencies (via DI) and workflow structure (via Go code) clear and traceable.
- **Testability:** Design components (nodes, workflows, dependencies) to be easily unit-tested and integrated.

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss changes or report bugs, or submit a pull request. We plan to add a `CONTRIBUTING.md` guide with more detailed information on the process and coding standards soon.
