// ./examples/fanin/example.go
package main

import (
	"context"
	"errors"
	"fmt"
	"heart" // Use ExecutionHandle, Context, Execute etc.
	"heart/nodes/openai"
	"heart/store"
	"os"
	"strings"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
)

type perspectives struct {
	Optimistic  string `json:"optimistic_perspective"`
	Pessimistic string `json:"pessimistic_perspective"`
	Realistic   string `json:"realistic_perspective"`
}

// threePerspectivesWorkflow handler function remains the same logically.
// It takes heart.Context and returns heart.ExecutionHandle.
func threePerspectivesWorkflowHandler(ctx heart.Context, question string) heart.ExecutionHandle[perspectives] {
	// --- Branch 1: Optimistic Perspective ---
	optimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a technology evangelist..."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 1024, Temperature: 0.8,
	}
	// Use DefineNode with the passed context (ctx) to get correct BasePath
	optimistNodeDef := openai.CreateChatCompletion("optimist-view") // Pass context
	optimistNode := optimistNodeDef.Start(heart.Into(optimistRequest))

	// --- Branch 2: Pessimistic Perspective ---
	pessimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a doom-and-gloom analyst..."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 1024, Temperature: 0.4,
	}
	pessimistNodeDef := openai.CreateChatCompletion("pessimist-view") // Pass context
	pessimistNode := pessimistNodeDef.Start(heart.Into(pessimistRequest))

	// --- Synthesis Step using NewNode ---
	synthesisNode := heart.NewNode( // Pass context
		ctx,
		"generate-realistic",
		func(synthesisCtx heart.NewNodeContext) heart.ExecutionHandle[perspectives] {

			optimistFuture := heart.FanIn(synthesisCtx, optimistNode)   // Pass NewNodeContext
			pessimistFuture := heart.FanIn(synthesisCtx, pessimistNode) // Pass NewNodeContext

			optResp, errOpt := optimistFuture.Get()
			pessResp, errPess := pessimistFuture.Get()

			fetchErrs := errors.Join(errOpt, errPess)
			if fetchErrs != nil {
				return heart.IntoError[perspectives](fetchErrs)
			}

			optimistText, errExtOpt := extractContent(optResp)
			pessimistText, errExtPess := extractContent(pessResp)
			extractErrs := errors.Join(errExtOpt, errExtPess)
			if extractErrs != nil {
				return heart.IntoError[perspectives](extractErrs)
			}

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
					{Role: goopenai.ChatMessageRoleSystem, Content: "You are a balanced, realistic synthesizer..."},
					{Role: goopenai.ChatMessageRoleUser, Content: realisticPrompt},
				},
				MaxTokens: 2048, Temperature: 0.6,
			}

			// Use the NewNodeContext's embedded Context
			realisticNodeDef := openai.CreateChatCompletion("realistic-synthesis")
			realisticNode := realisticNodeDef.Start(heart.Into(realisticRequest))

			realisticFuture := heart.FanIn(synthesisCtx, realisticNode) // Pass NewNodeContext

			realResp, errReal := realisticFuture.Get()
			if errReal != nil {
				err := fmt.Errorf("realistic synthesis failed: %w", errReal)
				return heart.IntoError[perspectives](err)
			}

			realText, errExtReal := extractContent(realResp)
			if errExtReal != nil {
				err := fmt.Errorf("error extracting realistic content: %w", errExtReal)
				return heart.IntoError[perspectives](err)
			}

			finalResult := perspectives{
				Pessimistic: strings.TrimSpace(pessimistText),
				Optimistic:  strings.TrimSpace(optimistText),
				Realistic:   strings.TrimSpace(realText),
			}

			return heart.Into(finalResult)
		},
	)

	return synthesisNode
}

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}
	client := goopenai.NewClient(apiKey)
	// --- Dependency Injection (Remains the same) ---
	if err := heart.Dependencies(openai.Inject(client)); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}

	// --- Store Setup ---
	fileStore, err := store.NewFileStore("workflows_fanin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file store: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Using FileStore at './workflows_fanin'")
	// defer os.RemoveAll("./workflows_fanin") // Optional cleanup

	// --- Workflow Definition using DefineNode ---
	// Create the workflow resolver first
	workflowResolver := heart.NewWorkflowResolver("threePerspectives", threePerspectivesWorkflowHandler)
	// Define the workflow like any other node
	threePerspectiveWorkflowDef := heart.DefineNode[string, perspectives](
		"threePerspectives", // Node ID for the workflow definition
		workflowResolver,
	)

	// --- Workflow Execution ---
	fmt.Println("Defining 3-Perspective workflow...")
	workflowCtx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// Start the workflow LAZILY, get an ExecutionHandle
	fmt.Println("Starting workflow (lazy)...")
	workflowHandle := threePerspectiveWorkflowDef.Start(heart.Into(inputQuestion))

	// Execute the workflow and wait for the result
	fmt.Println("Executing workflow and waiting for result...")
	// Pass workflow options (like store) to Execute
	perspectivesResult, err := heart.Execute(workflowCtx, workflowHandle, heart.WithStore(fileStore))

	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		// Check if the error was due to the waiting context being cancelled
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "Waiting for workflow result cancelled or timed out: %v\n", workflowCtx.Err())
		}
		os.Exit(1)
	}

	// --- Output ---
	fmt.Println("\nWorkflow completed successfully!")
	fmt.Println("🚀 --- Optimistic Perspective ---")
	fmt.Println(perspectivesResult.Optimistic)
	fmt.Println("🧐 --- Pessimistic Perspective ---")
	fmt.Println(perspectivesResult.Pessimistic)
	fmt.Println("✅ --- Realistic Perspective ---")
	fmt.Println(perspectivesResult.Realistic)
}

// extractContent remains the same
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	if len(resp.Choices) == 0 {
		errMsg := "no choices returned"
		// ... (rest of error handling) ...
		return "", errors.New(errMsg)
	}
	message := resp.Choices[0].Message
	content := message.Content
	return content, nil
}
