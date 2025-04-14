// ./examples/fanin/example.go
package main

import (
	"context"
	"errors"
	"fmt"
	"heart" // Use ExecutionHandle, WorkflowResultHandle etc.
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

// threePerspectivesWorkflow defines the graph structure.
// It now returns an ExecutionHandle for the final node.
func threePerspectivesWorkflow(ctx heart.Context, question string) heart.ExecutionHandle[perspectives] {
	// --- Branch 1: Optimistic Perspective ---
	optimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a technology evangelist..."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 1024, Temperature: 0.8,
	}
	// DefineNode returns NodeDefinition
	optimistNodeDef := openai.CreateChatCompletion(ctx, "optimist-view")
	// Start is lazy, returns ExecutionHandle[ChatCompletionResponse]
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
	// DefineNode returns NodeDefinition
	pessimistNodeDef := openai.CreateChatCompletion(ctx, "pessimist-view")
	// Start is lazy, returns ExecutionHandle[ChatCompletionResponse]
	pessimistNode := pessimistNodeDef.Start(heart.Into(pessimistRequest))

	// --- Synthesis Step using NewNode ---
	// NewNode returns ExecutionHandle[perspectives]
	synthesisNode := heart.NewNode(
		ctx,
		"generate-realistic",
		// This function runs when synthesisNode is resolved.
		// It returns the handle for the node that produces the final perspectives.
		func(synthesisCtx heart.NewNodeContext) heart.ExecutionHandle[perspectives] {

			// --- Initiate FanIn (gets Futures) ---
			// FanIn takes ExecutionHandles, returns Future
			optimistFuture := heart.FanIn(synthesisCtx, optimistNode)
			pessimistFuture := heart.FanIn(synthesisCtx, pessimistNode)

			// *** BLOCKS HERE when Get() is called ***
			// Future.Get() triggers execution of optimist/pessimist nodes if not already started.
			optResp, errOpt := optimistFuture.Get()
			pessResp, errPess := pessimistFuture.Get()

			fetchErrs := errors.Join(errOpt, errPess)
			if fetchErrs != nil {
				// IntoError returns ExecutionHandle representing an error
				return heart.IntoError[perspectives](fetchErrs)
			}

			// --- Extract Content ---
			optimistText, errExtOpt := extractContent(optResp)
			pessimistText, errExtPess := extractContent(pessResp)
			extractErrs := errors.Join(errExtOpt, errExtPess)
			if extractErrs != nil {
				return heart.IntoError[perspectives](extractErrs)
			}

			// --- Define 3rd LLM Call (Realistic Synthesis) ---
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

			// Define node using the derived context `synthesisCtx.Context`
			realisticNodeDef := openai.CreateChatCompletion(synthesisCtx.Context, "realistic-synthesis")
			// Start returns ExecutionHandle[ChatCompletionResponse]
			realisticNode := realisticNodeDef.Start(heart.Into(realisticRequest))

			// Initiate FanIn for the realistic synthesis result
			realisticFuture := heart.FanIn(synthesisCtx, realisticNode)

			// *** BLOCKS HERE when Get() is called ***
			realResp, errReal := realisticFuture.Get()
			if errReal != nil {
				err := fmt.Errorf("realistic synthesis failed: %w", errReal)
				return heart.IntoError[perspectives](err)
			}

			// --- Extract realistic content ---
			realText, errExtReal := extractContent(realResp)
			if errExtReal != nil {
				err := fmt.Errorf("error extracting realistic content: %w", errExtReal)
				return heart.IntoError[perspectives](err)
			}

			// Construct the final result
			finalResult := perspectives{
				Pessimistic: strings.TrimSpace(pessimistText),
				Optimistic:  strings.TrimSpace(optimistText),
				Realistic:   strings.TrimSpace(realText),
			}

			// Return a Handle containing the final successful result
			// Into returns ExecutionHandle[perspectives]
			return heart.Into(finalResult)
		},
	)

	// Return the handle for the synthesis node.
	// The workflow's final result comes from resolving this handle.
	return synthesisNode
}

func main() {
	// --- Standard Setup ---
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" { /* ... error handling ... */
		os.Exit(1)
	}
	client := goopenai.NewClient(apiKey)
	if err := heart.Dependencies(openai.Inject(client)); err != nil { /* ... error handling ... */
		os.Exit(1)
	}
	fileStore, err := store.NewFileStore("workflows")
	if err != nil { /* ... error handling ... */
		return
	}

	// --- Workflow Definition ---
	// DefineWorkflow returns WorkflowDefinition[string, perspectives]
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(fileStore))

	// --- Workflow Execution ---
	fmt.Println("Running 3-Perspective workflow...")
	workflowCtx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// New starts the workflow EAGERLY and returns WorkflowResultHandle[perspectives]
	workflowResultHandle := threePerspectiveWorkflowDef.New(workflowCtx, inputQuestion)

	// Get Result: Use Out(ctx) on the WorkflowResultHandle.
	// This blocks until the workflow completes, respecting workflowCtx for the *wait*.
	perspectivesResult, err := workflowResultHandle.Out(workflowCtx) // Pass context
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

// extractContent safely retrieves the message content from an OpenAI response.
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
