# go-agent-framework

[![Go Reference](https://pkg.go.dev/badge/github.com/stephanoumenos/go-agent-framework.svg)](https://pkg.go.dev/github.com/stephanoumenos/go-agent-framework) [![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

`go-agent-framework` is a **statically-typed framework** to build composable AI agents in Go.

Define your workflows using standard Go code, including its powerful concurrency primitives. The execution graph will be implicitly resolved. Let `go-agent-framework` manage dependency resolution, persistence, and error handling with a clean interface. The framework implicitly handles concurrency for independent workflow branches; more advanced patterns like fan-in/fan-out can be achieved using `gaf.NewNode` and `gaf.FanIn` (see Concepts and Features below).

> [!WARNING]
> This project is currently experimental. APIs may change, and stability is not guaranteed. Use with caution.

## 🚀 Quick Start

This example demonstrates a two-step workflow using `gaf.Bind` to chain nodes where the second node's input is derived from the first node's output. The first node prepares a personalized message, and the second node processes this message. [Full example here](examples/simple_chain/example.go).

```go
import (
	// ...
	gaf "github.com/stephanoumenos/go-agent-framework"
	"github.com/stephanoumenos/go-agent-framework/nodes/openai"
	goopenai "github.com/sashabaranov/go-openai"
)

func storyWorkflowHandler(gctx gaf.Context, initialPrompt string) gaf.ExecutionHandle[goopenai.ChatCompletionResponse] {
	ideaNodeDef := openai.CreateChatCompletion("idea")

	// 1. First LLM Call: Generate a creative story idea
	initialRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT3Dot5Turbo,
		Messages: []goopenai.ChatCompletionMessage{
			{
				Role:    goopenai.ChatMessageRoleUser,
				Content: initialPrompt,
			},
		},
		MaxTokens: 50,
	}
	firstCallHandle := ideaNodeDef.Start(gaf.Into(initialRequest))

	// 2. Define the mapping function for Bind
	mapStoryIdeaToExpansionRequest := func(response1 goopenai.ChatCompletionResponse) (goopenai.ChatCompletionRequest, error) {
		if len(response1.Choices) == 0 || response1.Choices[0].Message.Content == "" {
			return goopenai.ChatCompletionRequest{}, fmt.Errorf("first LLM call returned no content")
		}
		storyIdea := response1.Choices[0].Message.Content
		fmt.Printf("LLM Call 1 (Story Idea): %s\n\n", storyIdea)

		expansionRequest := goopenai.ChatCompletionRequest{
			Model: goopenai.GPT3Dot5Turbo,
			Messages: []goopenai.ChatCompletionMessage{
				{
					Role:    goopenai.ChatMessageRoleUser,
					Content: fmt.Sprintf("Expand this one-sentence story idea into a short paragraph: \"%s\"", storyIdea),
				},
			},
			MaxTokens: 150,
		}
		return expansionRequest, nil
	}

	// 3. Use Bind to chain the second LLM call
	expandNodeDef := openai.CreateChatCompletion("story")
	secondCallHandle := gaf.Bind(expandNodeDef, firstCallHandle, mapStoryIdeaToExpansionRequest)
	return secondCallHandle
}

// Define the reusable workflow blueprint.
var storyChainWorkflow = gaf.WorkflowFromFunc(gaf.NodeID("storyChainGenerator"), storyWorkflowHandler)

func main() {
	client := goopenai.NewClient(apiKey)

	// Inject the OpenAI client dependency.
	if err := gaf.Dependencies(openai.Inject(client)); err != nil {
		log.Fatalf("Error setting up dependencies: %v", err)
	}

	workflowInput := "Tell me a short, one-sentence creative story idea."

	finalResponse, err := gaf.ExecuteWorkflow(ctx, storyChainWorkflow, workflowInput)

	fmt.Printf("\nLLM Call 2 (Expanded Story):\n%s\n", finalResponse.Choices[0].Message.Content)
}
```

## Installation

```bash
go get github.com/stephanoumenos/go-agent-framework
```

_(Note: You'll typically import the root package `github.com/stephanoumenos/go-agent-framework` aliased as `gaf`, and specific sub-packages like `.../nodes/openai`, `.../store`, etc. as needed.)_

## Features

### Structured Output with OpenAI

Enforce JSON output from OpenAI models that matches a specific Go struct. [This example](examples/structuredoutput/example.go) generates a recipe.

### Fan-In with `gaf.NewNode`

Use `gaf.NewNode` along with `gaf.FanIn` to concurrently await multiple dependencies and then combine their results. [Example](examples/fanin/example.go).

### MCP Tool Integration

#### A. Using Tools from an MCP Server with OpenAI

The `WithMCP` middleware allows an OpenAI node to discover and call tools exposed by an MCP server.

#### B. Exposing `gaf` Workflows as MCP Tools

Adapt any `gaf.WorkflowDefinition` to be callable as an MCP tool. [Example](examples/mcp/example.go).

### Persistence

Workflows automatically persist their execution state (node status, errors) and can optionally persist input/output content when a `store.Store` is provided via `gaf.WithStore`.

Currently the persistance serves more for debugging and observability, but replay and resume will be added in the future.

See the `./examples` directory for more detailed usage patterns.

## Philosophy

- **Static over Dynamic:** Prioritize compile-time safety and checking using Go generics.
- **Composability:** Build complex systems from simple, reusable, type-safe units.
- **Idiomatic Go:** Leverage Go's built-in features and common practices.
- **Explicitness:** Make dependencies and workflow structure clear and traceable.
- **Testability:** Design components to be easily unit-tested and integrated.
- **Code Sharing:** Promote the creation of reusable nodes and workflows that can be imported and shared across different agent implementations.

## Concepts

- **`NodeDefinition[In, Out]`**: A reusable, immutable blueprint for a unit of work. Created via `gaf.DefineNode` or `gaf.WorkflowFromFunc`.
- **`WorkflowDefinition[In, Out]`**: Same as `NodeDefinition[In, Out]`, but can be a graph and the entry point for an execution. Created via `gaf.WorkflowFromFunc`.
- **`NodeResolver[In, Out]`**: Implements the core logic (`Get`) and initialization (`Init`) for an _atomic_ node.
- **`NodeInitializer`**: Returned by `Init()`. Provides a `NodeTypeID` used for matching dependencies. Can implement `DependencyInjectable[Dep]`.
- **`ExecutionHandle[Out]`**: A lightweight handle representing a future result of type `Out`. Obtained by calling `Start()` on a `NodeDefinition`.
- **`gaf.ExecuteWorkflow`**: Triggers execution of a `WorkflowDefinition` with a direct input value.
- **`gaf.Context`**: Passed to `WorkflowHandlerFunc` and `NewNode` functions. Provides access to workflow scope, Go `context.Context`, and `store.Store`.
- **`gaf.NewNode`**: Allows defining anonymous, inline workflow steps (subgraphs) within a `WorkflowHandlerFunc`.
- **`gaf.FanIn`**: Used within `gaf.NewNode` to concurrently await results from multiple `ExecutionHandle`s.
- **`Future[T]`**: Returned by `FanIn`. Represents the eventual result of a dependency. Use `future.Get()` to retrieve the value.
- **Dependency Injection**: Nodes declare dependencies via `DependencyInjectable[Dep]`. Dependencies are registered globally via `gaf.Dependencies(...)`.
- **`store.Store`**: Interface for persisting workflow state. Implementations (`MemoryStore`, `FileStore`) passed via `gaf.WithStore(...)`.

## Running tests

### Unit tests

```bash
❯ go test ./examples/...
ok      go-agent-framework/examples/fanin       0.176s
ok      go-agent-framework/examples/mcp 0.397s
ok      go-agent-framework/examples/simple_chain        0.596s
ok      go-agent-framework/examples/structuredoutput    0.428s
```

### e2e tests

```bash
❯ OPENAI_AUTH_TOKEN="YOUR_TOKEN_HERE" go test -tags="e2e" ./examples/...
ok      go-agent-framework/examples/fanin       0.669s
ok      go-agent-framework/examples/mcp 0.415s
ok      go-agent-framework/examples/simple_chain        0.466s
ok      go-agent-framework/examples/structuredoutput    0.835s
```
