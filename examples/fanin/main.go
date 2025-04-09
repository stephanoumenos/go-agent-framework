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

// threePerspectivesWorkflow demonstrates:
//  1. Parallel generation of Optimist & Pessimist views via LLM calls.
//  2. Using heart.FanIn to wait for both views.
//  3. Defining a *third* LLM call *within* the FanIn callback to synthesize
//     a Realistic view based on the first two.
//  4. The FanIn callback returns the Outputer[perspectives] containing the three perspectives.
func threePerspectivesWorkflow(ctx heart.Context, question string) heart.Outputer[perspectives] {

	// --- Branch 1: Optimistic Perspective ---
	optimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a technology evangelist focusing only on the positive potential and breakthroughs. Be very enthusiastic."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 512, Temperature: 0.8,
	}
	optimistNode := openai.CreateChatCompletion(ctx, "optimist-view")
	optimistOutput := optimistNode.Bind(heart.Into(optimistRequest)) // Outputer[goopenai.ChatCompletionResponse]

	// --- Branch 2: Pessimistic Perspective ---
	pessimistRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a doom-and-gloom analyst focusing only on the negatives, risks, and potential failures. Be very critical."},
			{Role: goopenai.ChatMessageRoleUser, Content: question},
		},
		MaxTokens: 512, Temperature: 0.4,
	}
	pessimistNode := openai.CreateChatCompletion(ctx, "pessimist-view")
	pessimistOutput := pessimistNode.Bind(heart.Into(pessimistRequest)) // Outputer[goopenai.ChatCompletionResponse]

	// --- FanIn Synthesis Step ---
	return heart.FanIn(
		ctx,                  // Original heart.Context needed to define new nodes
		"generate-realistic", // Node ID for the FanIn step itself
		func(rctx heart.ResolverContext) heart.Outputer[perspectives] { // Callback returns Outputer[perspectives]
			// Fetch results from parallel branches
			optResp, errOpt := optimistOutput.Out(rctx)
			pessResp, errPess := pessimistOutput.Out(rctx)

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
				MaxTokens: 512, Temperature: 1.0,
			}
			realisticNode := openai.CreateChatCompletion(ctx, "realistic-synthesis")
			// realisticNodeOutput is Outputer[goopenai.ChatCompletionResponse]
			realResp, errReal := realisticNode.Bind(heart.Into(realisticRequest)).Out(rctx)
			if errReal != nil {
				return heart.IntoError[perspectives](errReal)
			}

			realText, errExtReal := extractContent(realResp)
			if errExtReal != nil {
				return heart.IntoError[perspectives](errExtReal)
			}

			return heart.Into(perspectives{
				Pessimistic: pessimistText,
				Optimistic:  optimistText,
				Realistic:   realText,
			})
		},
	)
}

// extractContent safely retrieves the message content from an OpenAI response.
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	if len(resp.Choices) == 0 {
		return "", errors.New("no choices returned")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		finishReason := resp.Choices[0].FinishReason
		errMsg := fmt.Sprintf("empty content received (Finish Reason: %s)", finishReason)
		if finishReason == goopenai.FinishReasonContentFilter {
			errMsg = "content generation stopped due to OpenAI content filter"
		}
		return "", errors.New(errMsg)
	}
	return content, nil
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
	// Define the workflow using the handler.
	threePerspectiveWorkflow := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(fileStore))

	// --- Workflow Execution ---
	fmt.Println("Running 3-Perspective workflow...")
	workflowCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// Execute a new instance of the workflow. New returns immediately.
	resultHandle := threePerspectiveWorkflow.New(workflowCtx, inputQuestion)

	// Block until the result is ready and retrieve it.
	perspectives, err := resultHandle.Get()
	if err != nil {
		// This error represents a failure *during the execution path leading to the final output*
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "Workflow execution failed: Timeout or context error during resolution.")
		} else {
			fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		}
		os.Exit(1)
	}

	// --- Output ---
	fmt.Println("\nWorkflow completed successfully!")
	fmt.Println("🚀 --- Optimistic Perspective ---")
	fmt.Println(perspectives.Optimistic)
	fmt.Println("🧐 --- Pessimistic Perspective ---")
	fmt.Println(perspectives.Pessimistic)
	fmt.Println("✅ --- Realistic Perspective ---")
	fmt.Println(perspectives.Realistic)
}
