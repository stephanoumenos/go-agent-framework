# heart ❤️
[![Go Reference](https://pkg.go.dev/badge/github.com/your-user/heart.svg)](https://pkg.go.dev/github.com/stephanoumenos/heart) [![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

`heart` is a **statically-typed Go library** for building complex, composable, and resilient AI agent workflows using the power of Go generics.

Define your workflow steps as type-safe nodes, compose them into sophisticated graphs, and let `heart` manage concurrency, dependency resolution, persistence, and error handling with a clean, monadic-inspired interface.

```go
type Recipe struct {
	RecipeName  string   `json:"recipe_name"`
	Ingredients []string `json:"ingredients"`
	Steps       []string `json:"steps"`
	PrepTime    string   `json:"prep_time"`
}

var recipeWorkflow = heart.WorkflowFromFunc("recipeGenerator",
	func(ctx heart.Context, topic string) heart.ExecutionHandle[Recipe] {
		llmNodeDef := openai.CreateChatCompletion("generate_recipe_json")

		structuredOutputNodeDef := openaimw.WithStructuredOutput[Recipe](llmNodeDef)

		request := goopenai.ChatCompletionRequest{
			Model: goopenai.GPT4oMini,
			Messages: []goopenai.ChatCompletionMessage{
				{Role: goopenai.ChatMessageRoleSystem, Content: "You MUST output a JSON object adhering to the 'output' schema."},
				{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a recipe for: %s", topic)},
			},
			MaxTokens: 1024,
		}

		return structuredOutputNodeDef.Start(heart.Into(request))
	},
)

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	client := goopenai.NewClient(apiKey)
	// Inject the client for openai.CreateChatCompletion nodes
	if err := heart.Dependencies(openai.Inject(client)); err != nil { /* handle error */}

	fileStore, _ := store.NewFileStore("./workflow_runs")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Start the workflow definition (still lazy)
	workflowHandle := recipeWorkflow.Start(heart.Into("quick vegan chocolate chip cookies"))

	// Execute triggers the lazy computation graph and waits for the final result
	recipeResult, err := heart.Execute(ctx, workflowHandle, heart.WithStore(fileStore))
	if err != nil { /* handle error */}

	// Use your statically-typed recipe!
	fmt.Printf("Generated Recipe: %s (%s)\n", recipeResult.RecipeName, recipeResult.PrepTime)
	fmt.Println("Ingredients:", recipeResult.Ingredients)
}
````

## Features

  * **🚀 Statically Typed Workflows:** Leverage Go generics (`NodeDefinition[In, Out]`, `ExecutionHandle[Out]`) for end-to-end type safety. Catch input/output mismatches at compile time.
  * **🧩 Composable & Declarative:** Define atomic units of work (`NodeDefinition`) using `heart.DefineNode` and a `NodeResolver`. Compose them into complex workflows using standard Go functions (`heart.WorkflowFromFunc`, `heart.NewNode`). No YAML or external DSL needed.
  * **⚡ Lazy Execution & Implicit Concurrency:** Workflows are defined as graphs of `ExecutionHandle`s. Execution is triggered lazily by `heart.Execute` or `Future.Get`. Independent branches of the graph run concurrently automatically.
  * **🔗 Monadic-Inspired Interface:** The `ExecutionHandle[T]` acts like a future/promise, representing a computation that will eventually yield a value of type `T` or an error. `heart.FanIn` allows clean dependency joining within `heart.NewNode`.
  * **💉 Built-in Dependency Injection:** Inject dependencies (API clients, DB connections, etc.) into your nodes (`NodeResolver`s) via `NodeInitializer`s and `heart.Dependencies`. Supports generic `DependencyInjectable[Dep]` interface.
  * **🤖 Structured Output Middleware:** Includes `WithStructuredOutput` middleware for OpenAI nodes to enforce JSON output conforming to a specified Go struct, automatically handling schema generation and response parsing.
  * **🛠️ MCP Tool Integration:** Seamlessly integrate with the [MCP (Multi-Capability Protocol)](https://github.com/mark3labs/mcp-go) ecosystem.
      * Turn *any* `heart.NodeDefinition` (atomic node or complex workflow) into an MCP tool using `heart/mcp.IntoTool` adapter and mapper functions.
      * Use the `WithMCP` middleware for OpenAI nodes to automatically handle tool listing, LLM tool calls, execution via an injected MCP client, and response formatting.
  * **💾 Persistence:** Automatically save and potentially resume workflow state (node status, errors) and input/output content using an extensible `store.Store` interface (`FileStore`, `MemoryStore` provided). Managed via `heart.WithStore`.
  * **🧪 Testable:** Dependency injection makes mocking external services (LLMs, MCP clients, databases) straightforward using interfaces (e.g., `clientiface.ClientInterface`, `mcpclient.MCPClient`) and mock implementations. The `heart.ResetDependencies()` function aids test isolation.
  * **🌐 Extensible:** Define custom `NodeDefinition`s by implementing the `NodeResolver` interface to integrate any Go library, API, or internal service into your workflows.

## Why Heart?

Building robust AI agents often involves orchestrating multiple LLM calls, using external tools/APIs, interacting with data sources, parsing outputs, and managing complex control flow. `heart` addresses these challenges within the Go ecosystem by:

  * **Prioritizing Go Idioms:** Leverages Go's strengths – strong typing via generics, interfaces, built-in concurrency – rather than relying on external configuration languages or dynamically typed systems.
  * **Compile-Time Confidence:** Generic `NodeDefinition`s and `ExecutionHandle`s ensure that the inputs and outputs of your workflow steps match up, drastically reducing runtime errors common in agent orchestration.
  * **Developer Experience:** Define, test, and refactor workflows using familiar Go code and tooling. The structure encourages modularity and clarity.
  * **Performance:** Designed for efficient execution by leveraging Go's concurrency model and lazy evaluation, minimizing unnecessary overhead.

## Installation

```bash
go get [github.com/your-user/heart](https://github.com/your-user/heart) # Replace with your actual path
```

## 🚀 Quick Start

```go
package main

import (
	"context"
	"fmt"
	"heart"
	"heart/store"
	"strings"
	"time"
)

// 1. Define Node Logic (NodeResolver + NodeInitializer)
type uppercaseResolver struct{}
type uppercaseInitializer struct{} // Implements heart.NodeInitializer

func (i *uppercaseInitializer) ID() heart.NodeTypeID { return "example:uppercase" } // Unique ID for DI
func (r *uppercaseResolver) Init() heart.NodeInitializer { return &uppercaseInitializer{} } // Returns initializer
func (r *uppercaseResolver) Get(_ context.Context, in string) (string, error) { // The core logic
	return strings.ToUpper(in), nil
}

// Define the reusable node blueprint
var UppercaseNode = heart.DefineNode("uppercase", &uppercaseResolver{})

// 2. Define a Workflow using the Node
func simpleWorkflow(ctx heart.Context, input string) heart.ExecutionHandle[string] {
	// Create an input handle (immediately resolved)
	inputHandle := heart.Into(input)
	// Start the uppercase node, passing the input handle (lazy)
	uppercaseHandle := UppercaseNode.Start(inputHandle)
	// The handle returned here defines the workflow's output
	return uppercaseHandle
}

// Define the reusable workflow blueprint
var SimpleWorkflow = heart.WorkflowFromFunc("simpleWorkflow", simpleWorkflow)

func main() {
	// 3. Setup Store (optional, defaults to MemoryStore)
	memStore := store.NewMemoryStore()

	// 4. Execute the Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start the workflow definition (still lazy)
	workflowInput := "hello world"
	// Get the handle for the final result of this specific workflow run
	finalHandle := SimpleWorkflow.Start(heart.Into(workflowInput))

	// Execute triggers the computation graph and waits for the result
	result, err := heart.Execute(ctx, finalHandle, heart.WithStore(memStore))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow failed: %v\n", err)
		return
	}

	fmt.Printf("Input: %s\n", workflowInput)
	fmt.Printf("Result: %s\n", result) // Output: HELLO WORLD
}

```

## Concepts

  * **`NodeDefinition[In, Out]`**: A reusable, immutable blueprint for a unit of work (atomic node or composite workflow). Created via `heart.DefineNode` or `heart.WorkflowFromFunc`. Contains a base `NodeID`.
  * **`NodeResolver[In, Out]`**: Implements the core logic (`Get`) and initialization (`Init`) for an *atomic* node definition.
  * **`NodeInitializer`**: Returned by `Init()`. Provides a `NodeTypeID` used for matching dependencies during injection. Can implement `DependencyInjectable[Dep]`.
  * **`ExecutionHandle[Out]`**: A lightweight handle representing a future result of type `Out` for a specific node/workflow *instance* in the graph. Obtained by calling `Start()` on a `NodeDefinition`. Acts as input for downstream nodes. It embodies the lazy, monadic nature of `heart`. Each handle instance gets a unique `NodePath` (e.g., `/workflowA:#0/nodeB:#1`) during execution.
  * **`heart.Execute`**: The top-level function to trigger the execution of a graph defined by a root `ExecutionHandle`. Manages the run context (`Context`), execution registry, persistence, and waits for the final result.
  * **`heart.Context`**: Passed to `WorkflowHandlerFunc` and `NewNode` functions. Provides access to the workflow's execution scope (UUID, BasePath, registry), the cancellable Go `context.Context`, and the `store.Store`.
  * **`heart.NewNode`**: Allows defining anonymous, inline workflow steps (subgraphs) within a `WorkflowHandlerFunc`. Enables complex dynamic branching and joining logic. Receives `NewNodeContext`.
  * **`heart.FanIn`**: Used within `NewNode` functions to concurrently await results from one or more dependency `ExecutionHandle`s, returning a `Future[T]`.
  * **`Future[T]`**: Returned by `FanIn`. Represents the eventual result of a dependency within a `NewNode`. Use `future.Get()` to retrieve the value or error, blocking until ready.
  * **Dependency Injection**: Nodes declare dependencies by having their `NodeInitializer` implement `DependencyInjectable[Dep]`. Dependencies (e.g., clients) are registered globally via `heart.Dependencies(constructor ...)` using `DependencyInjector`s (often created via helpers like `openai.Inject` or `heart.NodesDependencyInject`). Injection happens automatically during `NodeDefinition.Start()`.
  * **`store.Store`**: Interface (`store.Store`) for persisting workflow execution state. Pass implementations (`MemoryStore`, `FileStore`) via `heart.WithStore(...)` during `Execute`.

## Advanced Usage

### Structured Output with OpenAI

```go
import (
	"heart/nodes/openai"
	openaimw "heart/nodes/openai/middleware"
	goopenai "[github.com/sashabaranov/go-openai](https://github.com/sashabaranov/go-openai)"
)

// Define your Go struct
type MyData struct {
	Name   string `json:"name"`
	Values []int  `json:"values"`
}

// Inside your workflow handler:
func myWorkflow(ctx heart.Context, prompt string) heart.ExecutionHandle[MyData] {
	llmNodeDef := openai.CreateChatCompletion("call_openai_for_data")

	// Wrap the LLM node def with the middleware, specifying the target Go type
	structuredNodeDef := openaimw.WithStructuredOutput[MyData](llmNodeDef)

	// Prepare request (CRITICAL: Prompt must instruct model to use JSON Schema 'output')
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Or other capable model
		Messages: []goopenai.ChatCompletionMessage{
			// The middleware hardcodes the schema name to "output"
			{Role: "system", Content: "You MUST respond ONLY with a JSON object matching the 'output' schema."},
			{Role: "user", Content: prompt},
		},
		// ResponseFormat is set automatically by the middleware
	}

	// Start returns ExecutionHandle[MyData]
	handle := structuredNodeDef.Start(heart.Into(request))
	return handle
}

// Execute `myWorkflow`... result will be MyData struct or error.
```

### MCP Tool Integration

Turn any `heart` node (even complex workflows) into an MCP tool and use the `WithMCP` middleware.

```go
import (
	"heart/mcp" // Adapter package
	"heart/nodes/openai"
	openaimw "heart/nodes/openai/middleware" // Middleware package
	mcpclient "[github.com/mark3labs/mcp-go/client](https://github.com/mark3labs/mcp-go/client)"
	mcpschema "[github.com/mark3labs/mcp-go/mcp](https://github.com/mark3labs/mcp-go/mcp)"
	mcpserver "[github.com/mark3labs/mcp-go/server](https://github.com/mark3labs/mcp-go/server)"
	goopenai "[github.com/sashabaranov/go-openai](https://github.com/sashabaranov/go-openai)"
)

// Assume AddNode is a heart.NodeDefinition[AddInput, AddOutput]

// 1. Define MCP Schema for the tool
var addToolSchema = mcpschema.Tool{
	Name:        "add_numbers",
	Description: "Adds two integer numbers.",
	InputSchema: mcpschema.ToolInputSchema{ /* ... define properties A, B ... */ },
}

// 2. Define Mapper Functions
func mapMCReqToAddInput(ctx context.Context, req mcpschema.CallToolRequest) (AddInput, error) { /* map req.Params.Arguments to AddInput */ }
func mapAddOutputToMCPResult(ctx context.Context, out AddOutput) (*mcpschema.CallToolResult, error) { /* map out.Result to mcp.TextContent */ }

// 3. Adapt the Heart NodeDefinition into an MCPTool
adaptedTool := mcp.IntoTool(
	addNodeDef,        // Your heart NodeDefinition
	addToolSchema,     // MCP schema
	mapMCReqToAddInput,  // Mapper: MCP Request -> Node Input
	mapAddOutputToMCPResult, // Mapper: Node Output -> MCP Result
)

// 4. Add the adapted tool to an MCP Server
// (Setup mcpServer instance from [github.com/mark3labs/mcp-go/server](https://github.com/mark3labs/mcp-go/server))
mcpServer := mcpserver.NewMCPServer(/* ... */)
mcp.AddTools(mcpServer, adaptedTool) // Use heart's helper

// 5. Inject MCP Client into Heart Workflows
// (Setup mcpClient connected to the server from [github.com/mark3labs/mcp-go/client](https://github.com/mark3labs/mcp-go/client))
mcpClient, _ := mcpclient.NewSSEMCPClient(/* server URL */)
// client.Start(ctx) // Start client processing

// Inject *both* OpenAI and MCP clients
err := heart.Dependencies(
	openai.Inject(openaiClient),      // For standard OpenAI calls
	openai.InjectMCPClient(mcpClient), // For the WithMCP middleware's internal nodes
)
// handle err...
defer heart.ResetDependencies()

// 6. Using the WithMCP Middleware << NEW SECTION >>
// The `WithMCP` middleware wraps a NodeDefinition that handles standard OpenAI
// chat completions (`NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]`).
// It requires an `mcpclient.MCPClient` to be injected (see step 5).
//
// When executed, it:
// - Uses the injected MCP client to list available tools.
// - Includes these tool definitions in the request to the LLM.
// - If the LLM responds requesting a tool call:
//   - It invokes the appropriate tool via the injected MCP client.
//   - Adds the tool result message to the chat history.
//   - Calls the LLM again with the updated history.
// - Repeats the LLM/tool call loop until the LLM responds with text or an error/limit occurs.
func toolUsingWorkflow(ctx heart.Context, prompt string) heart.ExecutionHandle[goopenai.ChatCompletionResponse] {
	// Define the node for the underlying LLM call
	llmNodeDef := openai.CreateChatCompletion("llm_call_with_tools")

	// Wrap the base LLM node definition with the MCP middleware
	mcpEnabledNodeDef := openaimw.WithMCP(llmNodeDef)

	// Prepare the initial request
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: "system", Content: "Use tools for calculations."},
			{Role: "user", Content: prompt}, // e.g., "What is 5 + 7?"
		},
		// NOTE: Do not set Tools or ToolChoice manually here if relying solely on MCP discovery.
		// The middleware handles adding discovered tools and setting ToolChoice="auto" initially.
	}

	// Start the MCP-wrapped node. The handle resolves to the final LLM response
	// after the tool interaction loop completes.
	finalHandle := mcpEnabledNodeDef.Start(heart.Into(request))
	return finalHandle
}

// 7. Execute the workflow containing the WithMCP node...
// result, err := heart.Execute(ctx, toolUsingWorkflowHandle)
// The final 'result' will contain the LLM's textual response after using the tool.
```

### Persistence

Workflows automatically persist their state (node status, errors) and can persist input/output content when a `store.Store` is provided via `heart.WithStore`.

```go
// Use FileStore for persistence across runs
fileStore, err := store.NewFileStore("./my_workflow_runs")
if err != nil { /* handle error */ }

// Execute with the store
ctx := context.Background()
handle := MyWorkflow.Start(...) // Get the root handle for your workflow run
result, err := heart.Execute(ctx, handle, heart.WithStore(fileStore))

// If the process is interrupted and restarted, executing with the same store
// and optionally the same UUID (via heart.WithExistingUUID(runID)) allows
// the execution to potentially resume. Completed nodes load results from the store,
// avoiding re-computation.
// resumeResult, resumeErr := heart.Execute(ctx, handle, heart.WithStore(fileStore), heart.WithExistingUUID(previousRunID))
```

See the [`./examples`](https://www.google.com/search?q=./examples) directory for more detailed usage patterns, including `FanIn` and testing setups.

## Philosophy

  * **Static over Dynamic:** Prioritize compile-time safety and checking using Go generics.
  * **Composability:** Build complex systems from simple, reusable, type-safe units (NodeDefinitions).
  * **Idiomatic Go:** Leverage Go's built-in features (interfaces, concurrency, error handling) and common practices.
  * **Explicitness:** Make dependencies (via DI) and workflow structure (via Go code) clear and traceable.
  * **Testability:** Design components (nodes, workflows, dependencies) to be easily unit-tested and integrated.

## Contributing

Contributions are welcome\! Please read the `CONTRIBUTING.md` guide (TODO: Create this file) for details on the process, coding standards, and reporting issues.
