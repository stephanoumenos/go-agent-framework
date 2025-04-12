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
	"strings"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
)

type perspectives struct {
	Optimistic  string `json:"optimistic_perspective"`
	Pessimistic string `json:"pessimistic_perspective"`
	Realistic   string `json:"realistic_perspective"`
}

// threePerspectivesWorkflow demonstrates fan-in using heart.NewNode.
// The function provided to NewNode now returns a Node[perspectives] blueprint.
// The overall workflow function also returns Node[perspectives].
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
	// This NewNode now wraps the fan-in logic AND the subsequent synthesis call.
	// Its function returns the blueprint handle for the *final* node in its scope.
	synthesisNode := heart.NewNode[perspectives]( // Returns Node[perspectives]
		ctx,                  // Use the original workflow context to define this node
		"generate-realistic", // Node ID for the synthesis step itself (Path: /generate-realistic)
		// This function now takes NewNodeContext and returns a Node blueprint handle
		func(synthesisCtx heart.NewNodeContext) heart.Node[perspectives] {
			fmt.Println("Synthesis node started, waiting for parallel branches via FanIn...")

			// Use FanIn to wait for Optimist branch
			fmt.Println("Waiting for Optimist branch...")
			optResp, errOpt := heart.FanIn(synthesisCtx, optimistNode)
			if errOpt != nil {
				fmt.Printf("Optimist branch failed: %v\n", errOpt)
			} else {
				fmt.Println("Optimist branch completed.")
			}

			// Use FanIn to wait for Pessimist branch
			fmt.Println("Waiting for Pessimist branch...")
			pessResp, errPess := heart.FanIn[goopenai.ChatCompletionResponse](synthesisCtx, pessimistNode)
			if errPess != nil {
				fmt.Printf("Pessimist branch failed: %v\n", errPess)
			} else {
				fmt.Println("Pessimist branch completed.")
			}

			// Combine errors from FanIn. If either failed, return an error blueprint.
			fetchErrs := errors.Join(errOpt, errPess)
			if fetchErrs != nil {
				err := fmt.Errorf("error fetching parallel results via FanIn: %w", fetchErrs)
				// Return a Node blueprint representing the error
				return heart.IntoError[perspectives](err)
			}

			// --- Both branches succeeded via FanIn, proceed with extraction ---

			// Extract content
			optimistText, errExtOpt := extractContent(optResp)
			pessimistText, errExtPess := extractContent(pessResp)
			extractErrs := errors.Join(errExtOpt, errExtPess)
			if extractErrs != nil {
				err := fmt.Errorf("error extracting content from successful LLM calls: %w", extractErrs)
				// Return a Node blueprint representing the error
				return heart.IntoError[perspectives](err)
			}

			fmt.Println("Parallel branches finished successfully, defining realistic synthesis...")

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
			// Use the standard Context embedded within NewNodeContext for DefineNode/Bind
			realisticNodeDef := openai.CreateChatCompletion(synthesisCtx.Context, "realistic-synthesis")
			// Bind starts execution blueprint definition
			realisticNode := realisticNodeDef.Bind(heart.Into(realisticRequest)) // Returns Node[ChatCompletionResponse]

			// --- Define Final Assembly Node ---
			// This nested NewNode waits for the realisticNode and assembles the final struct.
			assemblyNode := heart.NewNode[perspectives](
				synthesisCtx.Context,    // Define in the same parent context
				"assemble-perspectives", // Node ID for the final assembly
				func(assemblyCtx heart.NewNodeContext) heart.Node[perspectives] {
					fmt.Println("Waiting for Realistic synthesis via FanIn...")
					// Use FanIn to wait for the realistic synthesis call to complete
					realResp, errReal := heart.FanIn(assemblyCtx, realisticNode)
					if errReal != nil {
						fmt.Printf("Realistic synthesis failed: %v\n", errReal)
						err := fmt.Errorf("realistic synthesis failed: %w", errReal)
						// Return an error blueprint
						return heart.IntoError[perspectives](err)
					}
					fmt.Println("Realistic synthesis completed.")

					realText, errExtReal := extractContent(realResp)
					if errExtReal != nil {
						err := fmt.Errorf("error extracting realistic content: %w", errExtReal)
						// Return an error blueprint
						return heart.IntoError[perspectives](err)
					}

					// Construct the final result
					finalResult := perspectives{
						Pessimistic: strings.TrimSpace(pessimistText),
						Optimistic:  strings.TrimSpace(optimistText),
						Realistic:   strings.TrimSpace(realText),
					}

					// Return a Node blueprint containing the final successful result
					return heart.Into(finalResult) // Into returns Node[perspectives]
				},
			) // End of assembly NewNode

			// The outer NewNode function returns the blueprint handle of the assemblyNode.
			// The NewNode wrapper will resolve this blueprint when its result is needed.
			return assemblyNode // Return Node[perspectives]
		},
	) // End of outer NewNode definition

	// Return the blueprint handle from the outer NewNode.
	// Its resolution will eventually provide the perspectives.
	return synthesisNode // Return Node[perspectives]
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
	// DefineWorkflow now takes the handler returning Node[perspectives]
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(fileStore))

	// --- Workflow Execution ---
	fmt.Println("Running 3-Perspective workflow using heart.NewNode...")
	workflowCtx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// Execute a new instance of the workflow. New returns WorkflowOutput immediately.
	// The type parameter remains perspectives as New wraps the final node's output.
	resultHandle := threePerspectiveWorkflowDef.New(workflowCtx, inputQuestion) // Returns Output[perspectives]
	fmt.Println("Workflow instance started...")

	// Block until the final result (from the assembly node inside NewNode) is ready.
	perspectivesResult, err := resultHandle.Out() // Call Out() on the Output handle
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "Workflow execution failed: Timeout exceeded.")
		} else if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Workflow execution failed: Context canceled.")
		} else {
			fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
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
