// ./examples/fanin/main.go
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
// The function provided to NewNode now returns an heart.Outputer.
func threePerspectivesWorkflow(ctx heart.Context, question string) heart.Outputer[perspectives] {

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
	optimistNoder := optimistNodeDef.Bind(heart.Into(optimistRequest)) // Starts Execution

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
	pessimistNoder := pessimistNodeDef.Bind(heart.Into(pessimistRequest)) // Starts Execution

	// --- Synthesis Step using NewNode ---
	synthesisNoder := heart.NewNode(
		ctx,                  // Use the original context to define this synthesis node
		"generate-realistic", // Node ID for the synthesis step itself (Path: /generate-realistic)
		// This function now returns an Outputer[perspectives]
		func(synthesisCtx heart.Context) heart.Outputer[perspectives] {
			fmt.Println("Synthesis node started, waiting for parallel branches sequentially...")

			// Block and wait for Optimist branch
			fmt.Println("Waiting for Optimist branch...")
			optResp, errOpt := optimistNoder.Out()
			if errOpt != nil {
				fmt.Printf("Optimist branch failed: %v\n", errOpt)
			} else {
				fmt.Println("Optimist branch completed.")
			}

			// Block and wait for Pessimist branch
			fmt.Println("Waiting for Pessimist branch...")
			pessResp, errPess := pessimistNoder.Out()
			if errPess != nil {
				fmt.Printf("Pessimist branch failed: %v\n", errPess)
			} else {
				fmt.Println("Pessimist branch completed.")
			}

			// Combine errors from parallel branches. If either failed, return an error Outputer.
			fetchErrs := errors.Join(errOpt, errPess)
			if fetchErrs != nil {
				err := fmt.Errorf("error fetching parallel results: %w", fetchErrs)
				// Return an Outputer containing the error
				return heart.IntoError[perspectives](err)
			}

			// --- Both branches succeeded, proceed with synthesis ---

			// Extract content
			optimistText, errExtOpt := extractContent(optResp)
			pessimistText, errExtPess := extractContent(pessResp)
			extractErrs := errors.Join(errExtOpt, errExtPess)
			if extractErrs != nil {
				err := fmt.Errorf("error extracting content from successful LLM calls: %w", extractErrs)
				// Return an Outputer containing the error
				return heart.IntoError[perspectives](err)
			}

			fmt.Println("Parallel branches finished successfully, starting realistic synthesis...")

			// --- Define 3rd LLM Call (Realistic Synthesis) ---
			// Use the derived synthesisCtx to define this sub-node correctly.
			// Its path will be /generate-realistic/realistic-synthesis
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

			// Define node using the derived context `synthesisCtx`
			realisticNodeDef := openai.CreateChatCompletion(synthesisCtx, "realistic-synthesis")
			// Bind starts execution
			realisticNoder := realisticNodeDef.Bind(heart.Into(realisticRequest))

			// Wait for the realistic synthesis call to complete by calling Out()
			fmt.Println("Waiting for Realistic synthesis...")
			realResp, errReal := realisticNoder.Out()
			if errReal != nil {
				fmt.Printf("Realistic synthesis failed: %v\n", errReal)
				err := fmt.Errorf("realistic synthesis failed: %w", errReal)
				// Return an Outputer containing the error
				return heart.IntoError[perspectives](err)
			}
			fmt.Println("Realistic synthesis completed.")

			realText, errExtReal := extractContent(realResp)
			if errExtReal != nil {
				err := fmt.Errorf("error extracting realistic content: %w", errExtReal)
				// Return an Outputer containing the error
				return heart.IntoError[perspectives](err)
			}

			// Construct the final result
			finalResult := perspectives{
				Pessimistic: strings.TrimSpace(pessimistText),
				Optimistic:  strings.TrimSpace(optimistText),
				Realistic:   strings.TrimSpace(realText),
			}

			// Return an Outputer containing the final successful result
			return heart.Into(finalResult)

		}) // End of NewNode function

	// Return the Noder from NewNode. Its Out() method will eventually provide the perspectives.
	return synthesisNoder
}

// extractContent safely retrieves the message content from an OpenAI response.
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	if len(resp.Choices) == 0 {
		// Add Usage info for context if available
		errMsg := "no choices returned"
		if resp.Usage.TotalTokens > 0 {
			errMsg = fmt.Sprintf("%s (Usage: %+v)", errMsg, resp.Usage)
		}
		return "", errors.New(errMsg)
	}
	message := resp.Choices[0].Message
	content := message.Content

	// Handle finish reasons appropriately
	finishReason := resp.Choices[0].FinishReason
	if finishReason == goopenai.FinishReasonStop || finishReason == goopenai.FinishReasonToolCalls {
		// Content can be empty if finish reason is tool_calls
		// Content is expected if finish reason is stop
		if content == "" && finishReason == goopenai.FinishReasonStop {
			errMsg := fmt.Sprintf("empty content received despite finish_reason 'stop' (Usage: %+v)", resp.Usage)
			return "", errors.New(errMsg)
		}
		return content, nil // Valid content or empty content with tool_calls
	}

	// Handle other finish reasons (length, content_filter, null) which usually imply an issue or incomplete generation
	errMsg := fmt.Sprintf("generation finished unexpectedly (Reason: %s, Usage: %+v)", finishReason, resp.Usage)
	if finishReason == goopenai.FinishReasonContentFilter {
		errMsg = fmt.Sprintf("content generation stopped due to OpenAI content filter (Usage: %+v)", resp.Usage)
	} else if finishReason == goopenai.FinishReasonLength {
		errMsg = fmt.Sprintf("content generation stopped due to max_tokens limit (Usage: %+v)", resp.Usage)
	}
	// Return the potentially partial content along with the error message
	// For simplicity here, we treat these unexpected finishes as errors needing investigation.
	// You could choose to return the partial content + nil error depending on requirements.
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
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(fileStore))

	// --- Workflow Execution ---
	fmt.Println("Running 3-Perspective workflow using heart.NewNode (returning Outputer)...")
	// Increased timeout slightly as sequential waits might take longer
	workflowCtx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// Execute a new instance of the workflow. New returns immediately.
	resultHandle := threePerspectiveWorkflowDef.New(workflowCtx, inputQuestion)
	fmt.Println("Workflow instance started...")

	// Block until the final result (from the synthesis node created by NewNode) is ready.
	perspectivesResult, err := resultHandle.Get() // This calls .Out() on synthesisNoder
	if err != nil {
		// Check for context errors specifically
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "Workflow execution failed: Timeout exceeded.")
		} else if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Workflow execution failed: Context canceled.")
		} else {
			// Print the underlying error (could be from LLM, content extraction, etc.)
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
