// ./examples/if/example.go
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

// conditionalWorkflowLLMCondition demonstrates conditional logic using heart.NewNode.
// It selects an LLM response based on sentiment and then extracts the string content.
func conditionalWorkflowLLMCondition(ctx heart.Context, topic string) heart.ExecutionHandle[string] {

	// 1. LLM Node for Sentiment Analysis
	sentimentRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "Analyze sentiment. Respond ONLY with 'POSITIVE' or 'NEGATIVE'."},
			{Role: goopenai.ChatMessageRoleUser, Content: topic},
		},
		MaxTokens: 5, Temperature: 0.1,
	}
	sentimentNodeDef := openai.CreateChatCompletion(ctx, "sentiment_check_llm")
	sentimentNode := sentimentNodeDef.Start(heart.Into(sentimentRequest))

	// 2. Condition Node (Parses LLM Output to Boolean)
	conditionNode := heart.NewNode(
		ctx, "parse_sentiment",
		func(parseCtx heart.NewNodeContext) heart.ExecutionHandle[bool] {
			sentimentFuture := heart.FanIn(parseCtx, sentimentNode)
			sentimentResponse, err := sentimentFuture.Get() // Waits for sentiment LLM
			if err != nil {
				return heart.IntoError[bool](fmt.Errorf("sentiment LLM call failed: %w", err))
			}

			llmTextResult, err := extractContent(sentimentResponse)
			if err != nil {
				if llmTextResult == "" {
					fmt.Printf("WARN: Sentiment LLM returned no text for '%s'. Defaulting to NEGATIVE.\n", topic)
					return heart.Into(false) // Default to false
				}
				return heart.IntoError[bool](fmt.Errorf("failed to extract sentiment LLM content: %w", err))
			}

			isPositive := (strings.TrimSpace(strings.ToUpper(llmTextResult)) == "POSITIVE")
			fmt.Printf("Sentiment for '%s': %t\n", topic, isPositive)
			return heart.Into(isPositive)
		},
	)

	// 3. Branching Logic using NewNode (Replaces heart.If)
	// This node waits for the condition, then defines and starts the appropriate next LLM call.
	selectedResponseNode := heart.NewNode(
		ctx, "sentiment_branch_logic", // New ID for the node performing the branching
		func(branchCtx heart.NewNodeContext) heart.ExecutionHandle[goopenai.ChatCompletionResponse] {
			// Wait for the boolean condition result
			conditionFuture := heart.FanIn(branchCtx, conditionNode)
			isPositive, err := conditionFuture.Get()
			if err != nil {
				return heart.IntoError[goopenai.ChatCompletionResponse](fmt.Errorf("failed to get condition result for branching: %w", err))
			}

			// Use standard Go if/else to choose the branch
			if isPositive {
				fmt.Println("Conditional Branch: POSITIVE path taken (via NewNode).")
				positiveRequest := goopenai.ChatCompletionRequest{
					Model: goopenai.GPT4oMini,
					Messages: []goopenai.ChatCompletionMessage{
						{Role: goopenai.ChatMessageRoleSystem, Content: "You are an optimistic assistant."},
						{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a short, uplifting sentence about: %s", topic)},
					}, MaxTokens: 100,
				}
				// Define and start the node within the branch context
				positiveNodeDef := openai.CreateChatCompletion(branchCtx.Context, "optimistic_statement")
				return positiveNodeDef.Start(heart.Into(positiveRequest)) // Return the handle for the true branch
			} else {
				fmt.Println("Conditional Branch: NEGATIVE/NEUTRAL path taken (via NewNode).")
				cautionaryRequest := goopenai.ChatCompletionRequest{
					Model: goopenai.GPT4oMini,
					Messages: []goopenai.ChatCompletionMessage{
						{Role: goopenai.ChatMessageRoleSystem, Content: "You are a balanced, slightly cautious assistant."},
						{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a short sentence acknowledging challenges regarding: %s", topic)},
					}, MaxTokens: 100,
				}
				// Define and start the node within the branch context
				cautionaryNodeDef := openai.CreateChatCompletion(branchCtx.Context, "cautionary_statement")
				return cautionaryNodeDef.Start(heart.Into(cautionaryRequest)) // Return the handle for the false branch
			}
		},
	)

	// 4. Final Extraction Node
	// Now waits for the result of the selected branch from the `sentiment_branch_logic` node.
	finalStatementNode := heart.NewNode(
		ctx,
		"extract_final_statement",
		func(extractCtx heart.NewNodeContext) heart.ExecutionHandle[string] {
			// FanIn on the node that performed the branching logic
			selectedResponseFuture := heart.FanIn(extractCtx, selectedResponseNode)
			responseFromBranch, err := selectedResponseFuture.Get() // Waits for selected branch LLM call
			if err != nil {
				return heart.IntoError[string](fmt.Errorf("error receiving response from selected branch node: %w", err))
			}

			content, err := extractContent(responseFromBranch)
			if err != nil {
				return heart.IntoError[string](fmt.Errorf("failed to extract final content: %w", err))
			}
			return heart.Into(content)
		},
	)

	return finalStatementNode // Return the handle to the final string result
}

var errNoChoices = errors.New("no choices returned from LLM")

// extractContent safely retrieves the message content from an OpenAI response.
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	if len(resp.Choices) == 0 {
		errMsg := errNoChoices.Error()
		if resp.Usage.TotalTokens > 0 {
			errMsg = fmt.Sprintf("%s (Usage: %+v)", errMsg, resp.Usage)
			return "", fmt.Errorf(errMsg)
		}
		return "", errNoChoices
	}
	message := resp.Choices[0].Message
	content := message.Content
	finishReason := resp.Choices[0].FinishReason

	if finishReason == goopenai.FinishReasonStop || finishReason == goopenai.FinishReasonToolCalls {
		return content, nil
	}

	errMsg := fmt.Sprintf("LLM generation finished unexpectedly (Reason: %s)", finishReason)
	if finishReason == goopenai.FinishReasonContentFilter {
		errMsg = "LLM content generation stopped due to content filter"
	} else if finishReason == goopenai.FinishReasonLength {
		errMsg = "LLM content generation stopped due to max_tokens limit reached"
	}
	return content, errors.New(errMsg)
}

func main() {
	fmt.Println("Starting If Example (using NewNode)...") // Updated title

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

	fileStore, err := store.NewFileStore("workflows_if_newnode_example") // Use a different dir name
	if err != nil {
		fmt.Printf("Error creating file store: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Using FileStore at './workflows_if_newnode_example'")
	defer os.RemoveAll("./workflows_if_newnode_example") // Clean up store after run

	conditionalWorkflowDef := heart.DefineWorkflow(conditionalWorkflowLLMCondition, heart.WithStore(fileStore))

	workflowCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// --- Test Case 1: Likely Positive ---
	inputTopic1 := "Successful launch of a new community garden project"
	fmt.Printf("\n--- Running Workflow (Test 1): Topic: \"%s\" ---\n", inputTopic1)
	resultHandle1 := conditionalWorkflowDef.New(workflowCtx, inputTopic1)
	finalStatement1, err1 := resultHandle1.Out(workflowCtx)
	if err1 != nil {
		fmt.Fprintf(os.Stderr, "Workflow 1 execution failed: %v\n", err1)
	} else {
		fmt.Println("\nWorkflow 1 completed successfully!")
		fmt.Printf("Final Statement: %s\n", finalStatement1)
	}

	// --- Test Case 2: Likely Negative ---
	inputTopic2 := "Rising concerns about misinformation online"
	fmt.Printf("\n--- Running Workflow (Test 2): Topic: \"%s\" ---\n", inputTopic2)
	resultHandle2 := conditionalWorkflowDef.New(workflowCtx, inputTopic2)
	finalStatement2, err2 := resultHandle2.Out(workflowCtx)
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "Workflow 2 execution failed: %v\n", err2)
	} else {
		fmt.Println("\nWorkflow 2 completed successfully!")
		fmt.Printf("Final Statement: %s\n", finalStatement2)
	}

	fmt.Println("\nExample finished.")
}
