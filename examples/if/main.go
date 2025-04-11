// ./examples/if_llm_condition/main.go
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
func conditionalWorkflowLLMCondition(ctx heart.Context, topic string) heart.Output[string] { // Final return type is still string

	// --- 1. LLM Node for Sentiment Analysis ---
	// (Same as before)
	sentimentRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "Analyze the sentiment of the following topic. Respond with only the word 'POSITIVE' if the sentiment is generally positive, otherwise respond with only the word 'NEGATIVE'."},
			{Role: goopenai.ChatMessageRoleUser, Content: topic},
		},
		MaxTokens: 5, Temperature: 0.1,
	}
	sentimentNodeDef := openai.CreateChatCompletion(ctx, "sentiment_check_llm")
	sentimentNode := sentimentNodeDef.Bind(heart.Into(sentimentRequest))

	// --- 2. Condition Node (Parsing LLM Output) ---
	// (Same as before)
	conditionOutput := heart.NewNode(
		ctx, "parse_sentiment",
		func(parseCtx heart.Context) heart.Output[bool] {
			fmt.Printf("Waiting for sentiment LLM analysis of topic: \"%s\"...\n", topic)
			sentimentResponse, err := sentimentNode.Out()
			if err != nil {
				return heart.IntoError[bool](fmt.Errorf("sentiment LLM call failed: %w", err))
			}
			llmTextResult, err := extractContent(sentimentResponse) // Use existing helper
			if err != nil {
				// If content extraction failed even from the sentiment check, handle it
				// Check if content is simply empty vs other errors
				if llmTextResult == "" && err.Error() == "no choices returned from LLM" || strings.Contains(err.Error(), "empty content received") {
					fmt.Printf("Sentiment LLM returned no text content for '%s'. Defaulting sentiment to NEGATIVE.\n", topic)
					return heart.Into(false) // Default to false if LLM gives no text
				}
				// For other extraction errors (content filter, length etc.)
				return heart.IntoError[bool](fmt.Errorf("failed to extract sentiment LLM content: %w", err))
			}
			trimmedResult := strings.TrimSpace(strings.ToUpper(llmTextResult))
			fmt.Printf("Sentiment LLM Result for '%s': '%s'\n", topic, trimmedResult)
			isPositive := (trimmedResult == "POSITIVE")
			return heart.Into(isPositive)
		},
	)

	// --- 3. heart.If Construct ---
	// If now selects which *Response Output* to forward.
	// The type parameter is changed to goopenai.ChatCompletionResponse.
	selectedResponseOutput := heart.If(
		ctx,                // Use the root context to define the If wrapper
		"sentiment_branch", // Node ID for the If wrapper
		conditionOutput,    // The Output[bool] from the parsing node

		// --- ifTrue Function ---
		// Now returns Output[goopenai.ChatCompletionResponse]
		func(trueCtx heart.Context) heart.Output[goopenai.ChatCompletionResponse] {
			fmt.Println("LLM perceived POSITIVE sentiment. Executing TRUE branch...")
			positiveRequest := goopenai.ChatCompletionRequest{
				Model: goopenai.GPT4oMini,
				Messages: []goopenai.ChatCompletionMessage{
					{Role: goopenai.ChatMessageRoleSystem, Content: "You are an optimistic assistant."},
					{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a short, uplifting sentence about: %s", topic)},
				}, MaxTokens: 100,
			}
			positiveNodeDef := openai.CreateChatCompletion(trueCtx, "optimistic_statement")
			positiveNode := positiveNodeDef.Bind(heart.Into(positiveRequest))
			// Return the Node handle directly, as Node implements Output[Response]
			return positiveNode // CHANGED RETURNED VALUE
		},

		// --- _else Function ---
		// Now returns Output[goopenai.ChatCompletionResponse]
		func(falseCtx heart.Context) heart.Output[goopenai.ChatCompletionResponse] {
			fmt.Println("LLM perceived NEGATIVE/NEUTRAL sentiment. Executing FALSE branch...")
			cautionaryRequest := goopenai.ChatCompletionRequest{
				Model: goopenai.GPT4oMini,
				Messages: []goopenai.ChatCompletionMessage{
					{Role: goopenai.ChatMessageRoleSystem, Content: "You are a balanced and slightly cautious assistant."},
					{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Generate a short sentence acknowledging potential challenges or complexities regarding: %s", topic)},
				}, MaxTokens: 100,
			}
			cautionaryNodeDef := openai.CreateChatCompletion(falseCtx, "cautionary_statement")
			cautionaryNode := cautionaryNodeDef.Bind(heart.Into(cautionaryRequest))
			// Return the Node handle directly
			return cautionaryNode // CHANGED RETURNED VALUE
		},
	)

	// --- 4. Final Extraction Node ---
	// This node is defined *after* the If. It takes the Output from the If
	// (which is the Output of the chosen LLM call) and extracts the string.
	finalStatementOutput := heart.NewNode(
		ctx,                       // Defined in the root context
		"extract_final_statement", // Node ID (e.g., path /extract_final_statement)
		func(extractCtx heart.Context) heart.Output[string] {
			fmt.Println("Waiting for selected LLM response...")
			// Block and wait for the output from the If construct (selected response)
			responseFromIf, err := selectedResponseOutput.Out() // Gets the ChatCompletionResponse
			if err != nil {
				// If the selected branch itself failed
				return heart.IntoError[string](fmt.Errorf("error receiving response from selected sentiment branch: %w", err))
			}

			fmt.Println("Extracting final content...")
			// Extract the string content using the same helper function
			content, err := extractContent(responseFromIf)
			if err != nil {
				// If extraction fails even from the selected response
				return heart.IntoError[string](fmt.Errorf("failed to extract final content: %w", err))
			}

			// Return the final extracted string
			return heart.Into(content)
		},
	)

	// Return the Output[string] from the final extraction node
	return finalStatementOutput
}

// extractContent (same as before) safely retrieves message content.
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	if len(resp.Choices) == 0 {
		errMsg := "no choices returned from LLM"
		return "", errors.New(errMsg)
	}
	message := resp.Choices[0].Message
	content := message.Content
	finishReason := resp.Choices[0].FinishReason
	if finishReason == goopenai.FinishReasonStop || finishReason == goopenai.FinishReasonToolCalls {
		if content == "" && finishReason == goopenai.FinishReasonStop {
			// Return empty string and nil error if content is legitimately empty
			return "", nil
		}
		return content, nil
	}
	// Handle unexpected finish reasons, return potentially partial content and an error
	errMsg := fmt.Sprintf("LLM generation finished unexpectedly (Reason: %s)", finishReason)
	if finishReason == goopenai.FinishReasonContentFilter {
		errMsg = "LLM content generation stopped due to content filter"
	} else if finishReason == goopenai.FinishReasonLength {
		errMsg = "LLM content generation stopped due to max_tokens limit reached"
	}
	return content, errors.New(errMsg) // Return content even with error
}

func main() {
	fmt.Println("Starting heart.If Example with LLM Condition (Refactored Extraction)...")

	// --- Standard Setup ---
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" { /* ... error handling ... */
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}

	client := goopenai.NewClient(apiKey)
	if err := heart.Dependencies(openai.Inject(client)); err != nil { /* ... error handling ... */
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}
	fileStore, err := store.NewFileStore("workflows")
	if err != nil { /* ... error handling ... */
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
