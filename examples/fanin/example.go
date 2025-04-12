// ./examples/fanin/example.go
package main

import (
	"context"
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai"
	"heart/store"
	"os"
	"strings" // Import sync for WaitGroup
	"time"

	goopenai "github.com/sashabaranov/go-openai"
)

type perspectives struct {
	Optimistic  string `json:"optimistic_perspective"`
	Pessimistic string `json:"pessimistic_perspective"`
	Realistic   string `json:"realistic_perspective"`
}

// threePerspectivesWorkflow demonstrates fan-in using heart.NewNode.
// FanIn now returns Future[T], requiring explicit .Get() calls to wait.
func threePerspectivesWorkflow(ctx heart.Context, question string) heart.Node[perspectives] {
	// --- Branch 1: Optimistic Perspective ---
	optimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a technology evangelist focusing only on the positive potential and breakthroughs. Be very enthusiastic."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 1024, Temperature: 0.8,
	}
	optimistNodeDef := openai.CreateChatCompletion(ctx, "optimist-view")
	optimistNode := optimistNodeDef.Bind(heart.Into(optimistRequest)) // Returns Node[ChatCompletionResponse]

	// --- Branch 2: Pessimistic Perspective ---
	pessimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a doom-and-gloom analyst focusing only on the negatives, risks, and potential failures. Be very critical."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 1024, Temperature: 0.4,
	}
	pessimistNodeDef := openai.CreateChatCompletion(ctx, "pessimist-view")
	pessimistNode := pessimistNodeDef.Bind(heart.Into(pessimistRequest)) // Returns Node[ChatCompletionResponse]

	// --- Synthesis Step using NewNode ---
	synthesisNode := heart.NewNode( // Returns Node[perspectives]
		ctx,                  // Use the original workflow context to define this node
		"generate-realistic", // Node ID for the synthesis step itself (Path: /generate-realistic)
		// This function defines the logic within the NewNode wrapper.
		func(synthesisCtx heart.NewNodeContext) heart.Node[perspectives] {

			// --- Initiate FanIn (Non-Blocking) ---
			// FanIn returns Future[T] immediately.
			optimistFuture := heart.FanIn(synthesisCtx, optimistNode)
			pessimistFuture := heart.FanIn(synthesisCtx, pessimistNode)

			// Let's wait sequentially for simplicity here:
			optResp, errOpt := optimistFuture.Get()    // *** BLOCKS HERE ***
			pessResp, errPess := pessimistFuture.Get() // *** BLOCKS HERE ***

			fetchErrs := errors.Join(errOpt, errPess)
			if fetchErrs != nil {
				return heart.IntoError[perspectives](fetchErrs) // Return error blueprint
			}

			// Both branches succeeded, proceed with extraction
			optimistText, errExtOpt := extractContent(optResp)
			pessimistText, errExtPess := extractContent(pessResp)
			extractErrs := errors.Join(errExtOpt, errExtPess)
			if extractErrs != nil {
				return heart.IntoError[perspectives](extractErrs) // Return error blueprint
			}

			// --- Define 3rd LLM Call (Realistic Synthesis) ---
			realisticPrompt := fmt.Sprintf(
				"Based on the following two perspectives, provide a balanced and realistic summary:\n\n"+
					"Optimistic View:\n%s\n\n"+
					"Pessimistic View:\n%s\n\n"+
					"Realistic Summary:",
				strings.TrimSpace(optimistText),
				strings.TrimSpace(pessimistText),
			)
			realisticRequest := goopenai.ChatCompletionRequest{
				Model: goopenai.GPT4oMini,
				Messages: []goopenai.ChatCompletionMessage{
					{Role: goopenai.ChatMessageRoleSystem, Content: "You are a balanced, realistic synthesizer of information."},
					{Role: goopenai.ChatMessageRoleUser, Content: realisticPrompt},
				},
				MaxTokens: 2048, Temperature: 0.6,
			}

			// Define node using the derived context `synthesisCtx.Context`
			realisticNodeDef := openai.CreateChatCompletion(synthesisCtx.Context, "realistic-synthesis")
			realisticNode := realisticNodeDef.Bind(heart.Into(realisticRequest)) // Returns Node[ChatCompletionResponse]

			// Initiate FanIn for the realistic synthesis result
			realisticFuture := heart.FanIn(synthesisCtx, realisticNode) // Returns Future[ChatCompletionResponse]

			// Wait for the realistic synthesis result
			realResp, errReal := realisticFuture.Get() // *** BLOCKS HERE ***
			if errReal != nil {
				err := fmt.Errorf("realistic synthesis failed: %w", errReal)
				return heart.IntoError[perspectives](err) // Return error blueprint
			}

			// Extract realistic content
			realText, errExtReal := extractContent(realResp)
			if errExtReal != nil {
				err := fmt.Errorf("error extracting realistic content: %w", errExtReal)
				return heart.IntoError[perspectives](err) // Return error blueprint
			}

			// Construct the final result using values already retrieved
			finalResult := perspectives{
				Pessimistic: strings.TrimSpace(pessimistText), // Already available from outer scope
				Optimistic:  strings.TrimSpace(optimistText),  // Already available from outer scope
				Realistic:   strings.TrimSpace(realText),
			}

			// Return a Node blueprint containing the final successful result
			return heart.Into(finalResult) // Into returns Node[perspectives]
		},
	)

	return synthesisNode
}

func main() {
	// --- Standard Setup ---
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}
	client := goopenai.NewClient(apiKey)
	if err := heart.Dependencies(openai.Inject(client)); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}

	fileStore, err := store.NewFileStore("workflows")
	if err != nil {
		fmt.Println("error creating file store: ", err)
		return
	}

	// --- Workflow Definition ---
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(fileStore))

	// --- Workflow Execution ---
	fmt.Println("Running 3-Perspective workflow using heart.NewNode and Futures...")
	workflowCtx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	inputQuestion := "Should companies invest heavily in custom AGI research?"

	workflowResult := threePerspectiveWorkflowDef.New(workflowCtx, inputQuestion)

	perspectivesResult, err := workflowResult.Out()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		os.Exit(1)
	}

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
		if resp.Usage.TotalTokens > 0 {
			errMsg = fmt.Sprintf("%s (Usage: %+v)", errMsg, resp.Usage)
		}
		return "", errors.New(errMsg)
	}
	message := resp.Choices[0].Message
	content := message.Content

	finishReason := resp.Choices[0].FinishReason
	if finishReason == goopenai.FinishReasonStop || finishReason == goopenai.FinishReasonToolCalls {
		if content == "" && finishReason == goopenai.FinishReasonStop {
			// Allow empty content if finish reason is stop
			return "", nil
		}
		return content, nil // Valid content or empty content with tool_calls
	}

	errMsg := fmt.Sprintf("generation finished unexpectedly (Reason: %s, Usage: %+v)", finishReason, resp.Usage)
	if finishReason == goopenai.FinishReasonContentFilter {
		errMsg = fmt.Sprintf("content generation stopped due to OpenAI content filter (Usage: %+v)", resp.Usage)
	} else if finishReason == goopenai.FinishReasonLength {
		errMsg = fmt.Sprintf("content generation stopped due to max_tokens limit (Usage: %+v)", resp.Usage)
	}
	// Return content even with error for length/filter cases, but signal the issue
	return content, errors.New(errMsg)
}
