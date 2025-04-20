// ./examples/fanin/example.go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"heart"              // Provides core workflow definitions and execution.
	"heart/nodes/openai" // Provides pre-built nodes for OpenAI interaction.
	"heart/store"        // Provides storage options for workflow state.

	goopenai "github.com/sashabaranov/go-openai" // OpenAI Go client library.
)

// perspectives defines the structure for holding the results from the three
// different LLM perspectives generated in the workflow.
type perspectives struct {
	Optimistic  string `json:"optimistic_perspective"`
	Pessimistic string `json:"pessimistic_perspective"`
	Realistic   string `json:"realistic_perspective"`
}

// threePerspectivesWorkflowHandler defines the logic for a workflow that
// queries an LLM three times with different system prompts (optimistic, pessimistic,
// and realistic) based on an initial question, and then synthesizes the results.
//
// It demonstrates:
//   - Defining multiple independent branches of execution.
//   - Using heart.NewNode to define a dynamic node that waits for dependencies.
//   - Using heart.FanIn within NewNode to concurrently wait for multiple handles.
//   - Chaining LLM calls based on the results of previous calls.
func threePerspectivesWorkflowHandler(ctx heart.Context, question string) heart.ExecutionHandle[perspectives] {
	// --- Branch 1: Optimistic Perspective ---
	// Prepare the request for the optimistic LLM.
	optimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a technology evangelist, always looking at the most positive potential outcomes. Focus on the opportunities and upside."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 1024, Temperature: 0.8,
	}
	// Define the node blueprint for the optimistic call.
	// NodeIDs like "optimist-view" are relative to the workflow's BasePath.
	optimistNodeDef := openai.CreateChatCompletion("optimist-view")
	// Start the node execution lazily, providing the input request via heart.Into.
	// This returns a handle representing the future result.
	optimistNode := optimistNodeDef.Start(heart.Into(optimistRequest))

	// --- Branch 2: Pessimistic Perspective ---
	// Prepare the request for the pessimistic LLM.
	pessimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a doom-and-gloom analyst, always focusing on the potential risks and downsides. Be critical and skeptical."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 1024, Temperature: 0.4,
	}
	// Define the node blueprint for the pessimistic call.
	pessimistNodeDef := openai.CreateChatCompletion("pessimist-view")
	// Start the node execution lazily.
	pessimistNode := pessimistNodeDef.Start(heart.Into(pessimistRequest))

	// --- Synthesis Step using NewNode ---
	// Define a dynamic node that depends on the results of the optimistic and pessimistic branches.
	// NewNode takes the current context (ctx), a unique ID ("generate-realistic") within this context,
	// and a function that defines the node's logic.
	synthesisNode := heart.NewNode(
		ctx,                  // The context passed to the workflow handler.
		"generate-realistic", // NodeID for this dynamic node.
		// This function receives a NewNodeContext, allowing it to define and execute nodes
		// within its own scoped execution environment.
		func(synthesisCtx heart.NewNodeContext) heart.ExecutionHandle[perspectives] {
			// Use FanIn to wait for the results of the dependency handles (optimistNode, pessimistNode).
			// FanIn takes the NewNodeContext and the handle(s) to wait for.
			// It returns a Future, which allows retrieving the result once the dependency completes.
			optimistFuture := heart.FanIn(synthesisCtx, optimistNode)
			pessimistFuture := heart.FanIn(synthesisCtx, pessimistNode)

			// Get the results from the futures. This blocks until both dependencies are complete.
			// FanIn ensures dependencies execute concurrently where possible.
			optResp, errOpt := optimistFuture.Get()
			pessResp, errPess := pessimistFuture.Get()

			// Check for errors from fetching dependency results.
			fetchErrs := errors.Join(errOpt, errPess)
			if fetchErrs != nil {
				// If fetching failed, return an error handle immediately.
				// The error will include the path of the failing dependency.
				return heart.IntoError[perspectives](fetchErrs)
			}

			// Extract the text content from the LLM responses.
			optimistText, errExtOpt := extractContent(optResp)
			pessimistText, errExtPess := extractContent(pessResp)
			extractErrs := errors.Join(errExtOpt, errExtPess)
			if extractErrs != nil {
				// If content extraction failed, return an error handle.
				return heart.IntoError[perspectives](extractErrs)
			}

			// Prepare the prompt for the realistic synthesis LLM call, using the results.
			realisticPrompt := fmt.Sprintf(
				"Based on the following two perspectives, provide a balanced and realistic summary:\n\n"+
					"Optimistic View:\n%s\n\n"+
					"Pessimistic View:\n%s\n\n"+
					"Realistic Summary:",
				strings.TrimSpace(optimistText), strings.TrimSpace(pessimistText),
			)
			realisticRequest := goopenai.ChatCompletionRequest{
				Model: goopenai.GPT4oMini,
				Messages: []goopenai.ChatCompletionMessage{
					{Role: goopenai.ChatMessageRoleSystem, Content: "You are a balanced, realistic synthesizer. Combine the provided optimistic and pessimistic views into a neutral, practical perspective."},
					{Role: goopenai.ChatMessageRoleUser, Content: realisticPrompt},
				},
				MaxTokens: 2048, Temperature: 0.6,
			}

			// Define and start the node for the realistic synthesis LLM call.
			// This node is defined *within* the NewNode context (synthesisCtx).
			// Its path will be relative to the synthesisNode's path (e.g., "/threePerspectives:#0/generate-realistic:#0/realistic-synthesis:#0").
			realisticNodeDef := openai.CreateChatCompletion("realistic-synthesis")
			realisticNode := realisticNodeDef.Start(heart.Into(realisticRequest))

			// Use FanIn again to wait for the result of the realistic synthesis call.
			realisticFuture := heart.FanIn(synthesisCtx, realisticNode)

			// Get the final realistic response.
			realResp, errReal := realisticFuture.Get()
			if errReal != nil {
				// If the synthesis call failed, wrap the error and return an error handle.
				err := fmt.Errorf("realistic synthesis failed within node '%s': %w", synthesisCtx.BasePath, errReal)
				return heart.IntoError[perspectives](err)
			}

			// Extract the content from the final response.
			realText, errExtReal := extractContent(realResp)
			if errExtReal != nil {
				err := fmt.Errorf("error extracting realistic content within node '%s': %w", synthesisCtx.BasePath, errExtReal)
				return heart.IntoError[perspectives](err)
			}

			// Construct the final result structure.
			finalResult := perspectives{
				Pessimistic: strings.TrimSpace(pessimistText),
				Optimistic:  strings.TrimSpace(optimistText),
				Realistic:   strings.TrimSpace(realText),
			}

			// Return a handle containing the final successful result.
			return heart.Into(finalResult)
		},
	)

	// Return the handle to the synthesisNode. The entire workflow will execute
	// lazily when this handle is resolved (e.g., by heart.Execute).
	return synthesisNode
}

// main sets up dependencies, defines the workflow, executes it, and prints the results.
func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}

	// --- Dependency Injection ---
	// Create the OpenAI client instance.
	client := goopenai.NewClient(apiKey)
	// Inject the client instance so it's available to openai.CreateChatCompletion nodes.
	// heart.Dependencies should be called once during application setup.
	if err := heart.Dependencies(openai.Inject(client)); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}
	// Ensure dependencies are reset if this function is called multiple times in tests.
	defer heart.ResetDependencies() // Usually done in test cleanup.

	// --- Store Setup ---
	// Configure a file store to persist workflow state under './workflows_fanin'.
	fileStore, err := store.NewFileStore("workflows_fanin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file store: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Using FileStore at './workflows_fanin'")
	// defer os.RemoveAll("./workflows_fanin") // Optional cleanup

	// --- Workflow Definition ---
	// Define the workflow blueprint using the handler function.
	// "threePerspectives" is the top-level NodeID for this workflow definition.
	threePerspectiveWorkflowDef := heart.WorkflowFromFunc("threePerspectives", threePerspectivesWorkflowHandler)

	// --- Workflow Execution ---
	fmt.Println("Defining 3-Perspective workflow...")
	// Create a Go context with a timeout for the entire execution.
	workflowCtx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// Start the workflow LAZILY by calling Start on the definition.
	// This returns the root handle of the workflow instance. No execution happens yet.
	fmt.Println("Starting workflow (lazy)...")
	workflowHandle := threePerspectiveWorkflowDef.Start(heart.Into(inputQuestion))

	// Execute the workflow graph starting from the root handle.
	// This triggers the resolution process. heart.Execute blocks until the final
	// result (perspectives struct) is available or an error occurs.
	// We pass the Go context and workflow options (like the store) to Execute.
	fmt.Println("Executing workflow and waiting for result...")
	perspectivesResult, err := heart.Execute(workflowCtx, workflowHandle, heart.WithStore(fileStore))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		// Check if the error was due to the waiting context being cancelled or timing out.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "Waiting for workflow result cancelled or timed out: %v\n", workflowCtx.Err())
		}
		os.Exit(1)
	}

	// --- Output ---
	// Print the successfully obtained results.
	fmt.Println("\nWorkflow completed successfully!")
	fmt.Println("🚀 --- Optimistic Perspective ---")
	fmt.Println(perspectivesResult.Optimistic)
	fmt.Println("🧐 --- Pessimistic Perspective ---")
	fmt.Println(perspectivesResult.Pessimistic)
	fmt.Println("✅ --- Realistic Perspective ---")
	fmt.Println(perspectivesResult.Realistic)
}

// extractContent safely extracts the text content from the first choice of an
// OpenAI chat completion response. Returns an error if the response has no choices
// or the first choice has no message content.
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	if len(resp.Choices) == 0 {
		// Determine finish reason if available for better error context.
		finishReason := "unknown (no choices)"
		// It's unlikely to have Choices but no first choice message, but check defensively.
		if len(resp.Choices) > 0 {
			finishReason = string(resp.Choices[0].FinishReason)
		}
		errMsg := fmt.Sprintf("no choices returned from LLM (finish reason: %s)", finishReason)
		return "", errors.New(errMsg)
	}
	message := resp.Choices[0].Message
	content := message.Content
	if content == "" {
		// Check if content is empty, which might indicate an issue or function call.
		finishReason := string(resp.Choices[0].FinishReason)
		return "", fmt.Errorf("LLM response message content is empty (finish reason: %s)", finishReason)
	}
	return content, nil
}
