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

// conditionalWorkflowLLMCondition demonstrates using heart.If where the condition is determined by an LLM.
// It selects an LLM response based on sentiment and then extracts the string content.
// Returns heart.Output[string] representing the final extracted statement.
func conditionalWorkflowLLMCondition(ctx heart.Context, topic string) heart.Output[string] { // Final return type is string

	// --- 1. LLM Node for Sentiment Analysis ---
	sentimentRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "Analyze the sentiment of the following topic. Respond with only the word 'POSITIVE' if the sentiment is generally positive, otherwise respond with only the word 'NEGATIVE'."},
			{Role: goopenai.ChatMessageRoleUser, Content: topic},
		},
		MaxTokens: 5, Temperature: 0.1,
	}
	sentimentNodeDef := openai.CreateChatCompletion(ctx, "sentiment_check_llm")
	sentimentNode := sentimentNodeDef.Bind(heart.Into(sentimentRequest)) // Node[ChatCompletionResponse]

	// --- 2. Condition Node (Parsing LLM Output using NewNode + FanIn) ---
	// This node *waits* for the sentiment LLM and parses its output into a boolean.
	conditionOutput := heart.NewNode( // Returns Node[bool]
		ctx, "parse_sentiment",
		func(parseCtx heart.NewNodeContext) heart.Node[bool] { // Function returns Node[bool]
			fmt.Printf("Parse Sentiment Node: Initiating FanIn for sentiment LLM analysis of topic: \"%s\"...\n", topic)

			// Use FanIn to get the future result of the sentiment analysis.
			sentimentFuture := heart.FanIn(parseCtx, sentimentNode) // Returns Future[ChatCompletionResponse]

			// Wait for the sentiment analysis to complete.
			fmt.Println("Parse Sentiment Node: Waiting for sentiment LLM result via Future.Get()...")
			sentimentResponse, err := sentimentFuture.Get() // *** BLOCKS HERE ***
			if err != nil {
				fmt.Printf("Parse Sentiment Node: Sentiment LLM call failed: %v\n", err)
				// Return an error blueprint for the boolean result
				return heart.IntoError[bool](fmt.Errorf("sentiment LLM call failed: %w", err))
			}
			fmt.Println("Parse Sentiment Node: Sentiment LLM result received.")

			// Extract the text content from the response.
			llmTextResult, err := extractContent(sentimentResponse)
			if err != nil {
				// Handle extraction failure
				if llmTextResult == "" && (errors.Is(err, errNoChoices) || strings.Contains(err.Error(), "empty content")) { // Assuming errNoChoices exists or check string
					fmt.Printf("Sentiment LLM returned no text content for '%s'. Defaulting sentiment to NEGATIVE.\n", topic)
					return heart.Into(false) // Default to false if LLM gives no text
				}
				// For other extraction errors
				fmt.Printf("Parse Sentiment Node: Failed to extract sentiment content: %v\n", err)
				return heart.IntoError[bool](fmt.Errorf("failed to extract sentiment LLM content: %w", err))
			}

			// Parse the result into a boolean.
			trimmedResult := strings.TrimSpace(strings.ToUpper(llmTextResult))
			fmt.Printf("Sentiment LLM Result for '%s': '%s'\n", topic, trimmedResult)
			isPositive := (trimmedResult == "POSITIVE")

			// Return a blueprint containing the boolean result.
			return heart.Into(isPositive)
		},
	) // End of conditionOutput NewNode definition

	// --- 3. heart.If Construct ---
	// The heart.If implementation *internally* handles waiting for the conditionOutput node.
	// It uses FanIn internally and calls Get() on the condition's Future.
	selectedResponseOutput := heart.If( // Returns Node[ChatCompletionResponse]
		ctx,                // Use the root context to define the If wrapper
		"sentiment_branch", // Node ID for the If wrapper
		conditionOutput,    // Pass the Node[bool] blueprint handle
		// --- ifTrue Function (defines the blueprint for the true branch) ---
		func(trueCtx heart.Context) heart.Node[goopenai.ChatCompletionResponse] {
			fmt.Println("Conditional Branch: LLM perceived POSITIVE sentiment. Defining TRUE branch...")
			positiveRequest := goopenai.ChatCompletionRequest{
				Model: goopenai.GPT4oMini,
				Messages: []goopenai.ChatCompletionMessage{
					{Role: goopenai.ChatMessageRoleSystem, Content: "You are an optimistic assistant."},
					{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a short, uplifting sentence about: %s", topic)},
				}, MaxTokens: 100,
			}
			positiveNodeDef := openai.CreateChatCompletion(trueCtx, "optimistic_statement")
			positiveNode := positiveNodeDef.Bind(heart.Into(positiveRequest))
			return positiveNode // Return the Node blueprint handle
		},
		// --- _else Function (defines the blueprint for the false branch) ---
		func(falseCtx heart.Context) heart.Node[goopenai.ChatCompletionResponse] {
			fmt.Println("Conditional Branch: LLM perceived NEGATIVE/NEUTRAL sentiment. Defining FALSE branch...")
			cautionaryRequest := goopenai.ChatCompletionRequest{
				Model: goopenai.GPT4oMini,
				Messages: []goopenai.ChatCompletionMessage{
					{Role: goopenai.ChatMessageRoleSystem, Content: "You are a balanced and slightly cautious assistant."},
					{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a short sentence acknowledging potential challenges or complexities regarding: %s", topic)},
				}, MaxTokens: 100,
			}
			cautionaryNodeDef := openai.CreateChatCompletion(falseCtx, "cautionary_statement")
			cautionaryNode := cautionaryNodeDef.Bind(heart.Into(cautionaryRequest))
			return cautionaryNode // Return the Node blueprint handle
		},
	)

	// --- 4. Final Extraction Node ---
	// This node waits for the result of the `If` construct (which is the selected response blueprint).
	finalStatementOutput := heart.NewNode( // Returns Node[string]
		ctx,                       // Defined in the root context
		"extract_final_statement", // Node ID
		func(extractCtx heart.NewNodeContext) heart.Node[string] { // Returns Node[string]
			fmt.Println("Extract Final Node: Initiating FanIn for selected LLM response...")
			// Get the Future for the response selected by the If construct.
			selectedResponseFuture := heart.FanIn(extractCtx, selectedResponseOutput) // Future[ChatCompletionResponse]

			// Wait for the selected response to complete.
			fmt.Println("Extract Final Node: Waiting for selected LLM response via Future.Get()...")
			responseFromIf, err := selectedResponseFuture.Get() // *** BLOCKS HERE ***
			if err != nil {
				fmt.Printf("Extract Final Node: Error receiving response from selected branch: %v\n", err)
				return heart.IntoError[string](fmt.Errorf("error receiving response from selected sentiment branch: %w", err))
			}
			fmt.Println("Extract Final Node: Selected response received.")

			// Extract the string content.
			fmt.Println("Extracting final content...")
			content, err := extractContent(responseFromIf)
			if err != nil {
				fmt.Printf("Extract Final Node: Failed to extract final content: %v\n", err)
				return heart.IntoError[string](fmt.Errorf("failed to extract final content: %w", err))
			}

			// Return the final extracted string blueprint.
			return heart.Into(content)
		},
	)

	// Return the Output[string] from the final extraction node
	return finalStatementOutput
}

// Define a package-level error for clarity in extractContent
var errNoChoices = errors.New("no choices returned from LLM")

// extractContent (modified slightly to use errNoChoices)
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	if len(resp.Choices) == 0 {
		errMsg := errNoChoices.Error() // Use the defined error
		if resp.Usage.TotalTokens > 0 {
			errMsg = fmt.Sprintf("%s (Usage: %+v)", errMsg, resp.Usage)
			return "", fmt.Errorf(errMsg) // Return formatted error
		}
		return "", errNoChoices // Return base error
	}
	message := resp.Choices[0].Message
	content := message.Content
	finishReason := resp.Choices[0].FinishReason
	if finishReason == goopenai.FinishReasonStop || finishReason == goopenai.FinishReasonToolCalls {
		if content == "" && finishReason == goopenai.FinishReasonStop {
			return "", nil // Allow empty content if stopped normally
		}
		return content, nil
	}
	// Handle unexpected finish reasons
	errMsg := fmt.Sprintf("LLM generation finished unexpectedly (Reason: %s)", finishReason)
	if finishReason == goopenai.FinishReasonContentFilter {
		errMsg = "LLM content generation stopped due to content filter"
	} else if finishReason == goopenai.FinishReasonLength {
		errMsg = "LLM content generation stopped due to max_tokens limit reached"
	}
	return content, errors.New(errMsg) // Return content even with error
}

func main() {
	fmt.Println("Starting heart.If Example with LLM Condition (Refactored Extraction & Futures)...")

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
		fmt.Printf("Error creating file store: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Using FileStore at './workflows'")

	// --- Workflow Definition ---
	conditionalWorkflowDef := heart.DefineWorkflow(conditionalWorkflowLLMCondition, heart.WithStore(fileStore))

	// --- Workflow Execution ---
	workflowCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// ---- Test Case 1: Likely Positive Sentiment ----
	inputTopic1 := "Successful launch of a new community garden project"
	fmt.Printf("\n--- Running Workflow (Test 1): Topic: \"%s\" ---\n", inputTopic1)
	resultHandle1 := conditionalWorkflowDef.New(workflowCtx, inputTopic1)
	fmt.Println("Workflow instance 1 started...")
	finalStatement1, err1 := resultHandle1.Out()
	if err1 != nil {
		fmt.Fprintf(os.Stderr, "Workflow 1 execution failed: %v\n", err1)
	} else {
		fmt.Println("\nWorkflow 1 completed successfully!")
		fmt.Printf("Final Statement: %s\n", finalStatement1)
	}

	// ---- Test Case 2: Likely Negative/Complex Sentiment ----
	inputTopic2 := "Rising concerns about misinformation online"
	fmt.Printf("\n--- Running Workflow (Test 2): Topic: \"%s\" ---\n", inputTopic2)
	// Use a separate context if needed, or reuse if appropriate for the test
	resultHandle2 := conditionalWorkflowDef.New(workflowCtx, inputTopic2)
	fmt.Println("Workflow instance 2 started...")
	finalStatement2, err2 := resultHandle2.Out()
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "Workflow 2 execution failed: %v\n", err2)
	} else {
		fmt.Println("\nWorkflow 2 completed successfully!")
		fmt.Printf("Final Statement: %s\n", finalStatement2)
	}

	fmt.Println("\nExample finished.")
	fmt.Println("Check the './workflows' directory for persisted graph structure.")
}
