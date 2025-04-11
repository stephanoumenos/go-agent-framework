package main

import (
	"context"
	"errors"
	"fmt"
	"heart"              // Your library's root package
	"heart/nodes/openai" // OpenAI node integration
	"heart/store"        // Storage interface and implementations
	"os"
	"strings"
	"time"

	goopenai "github.com/sashabaranov/go-openai" // OpenAI client library
)

// titleAndParagraphWorkflow_Simple defines a two-step workflow:
// 1. Generate a potential title for a given topic.
// 2. Generate an introductory paragraph based on the title.
// It uses a direct .Out() call within the handler for data transformation.
// It returns the final OpenAI API response containing the paragraph.
func titleAndParagraphWorkflow_Simple(ctx heart.Context, topic string) heart.Output[goopenai.ChatCompletionResponse] {
	// This handler function defines the structure and dependencies of the workflow graph.

	// --- Node 1: Title Generator ---
	fmt.Printf("INFO: Defining Node 1 '%s'...\n", "/generate-title")
	titleGenReq := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a creative assistant. Generate a concise and engaging title for an article about the following topic. Respond with *only* the title text, nothing else."},
			{Role: goopenai.ChatMessageRoleUser, Content: topic},
		},
		MaxTokens: 30, Temperature: 0.7, N: 1,
	}
	titleGenNodeDef := openai.CreateChatCompletion(ctx, "generate-title") // Path: /generate-title
	// Bind starts execution of Node 1 immediately and returns its Output handle.
	titleGenNode := titleGenNodeDef.Bind(heart.Into(titleGenReq)) // Output[ChatCompletionResponse]
	fmt.Printf("INFO: Node 1 '%s' bound and running...\n", "/generate-title")

	// --- Direct Data Transformation in Handler ---
	// Block and wait for Node 1 to complete *within the handler function*.
	// NOTE: This makes the setup of Node 2 dependent on the completion of Node 1.
	fmt.Printf("INFO: Workflow handler waiting for Node 1 '%s' result...\n", "/generate-title")
	titleResp, err := titleGenNode.Out() // *** Blocking call ***
	if err != nil {
		// If Node 1 fails, we can't proceed. Return an Output that immediately resolves to this error.
		fmt.Printf("ERROR: Node 1 '%s' failed: %v. Aborting workflow setup.\n", "/generate-title", err)
		return heart.IntoError[goopenai.ChatCompletionResponse](fmt.Errorf("title generation node failed: %w", err))
	}
	fmt.Printf("INFO: Node 1 '%s' completed. Extracting title...\n", "/generate-title")

	// Extract the title text from Node 1's response.
	titleText, err := extractContent(titleResp)
	if err != nil {
		fmt.Printf("ERROR: Failed to extract title text: %v. Aborting workflow setup.\n", err)
		return heart.IntoError[goopenai.ChatCompletionResponse](fmt.Errorf("failed to extract title text: %w", err))
	}
	titleText = strings.Trim(titleText, `" `) // Clean up
	if titleText == "" {
		fmt.Printf("ERROR: Title generator returned empty content. Aborting workflow setup.\n")
		return heart.IntoError[goopenai.ChatCompletionResponse](errors.New("title generator returned empty content"))
	}
	fmt.Printf("INFO: Extracted title: \"%s\". Preparing Node 2...\n", titleText)

	// Create the request for Node 2 using the extracted title.
	paraReq := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a helpful writing assistant. Write a short, engaging introductory paragraph for an article with the following title. Respond with *only* the paragraph."},
			{Role: goopenai.ChatMessageRoleUser, Content: fmt.Sprintf("Title: %s", titleText)},
		},
		MaxTokens: 150, Temperature: 0.6, N: 1,
	}

	// --- Node 2: Paragraph Generator ---
	fmt.Printf("INFO: Defining Node 2 '%s'...\n", "/generate-paragraph")
	paragraphGenNodeDef := openai.CreateChatCompletion(ctx, "generate-paragraph") // Path: /generate-paragraph
	// Bind Node 2 with its prepared request. This starts Node 2's execution.
	paragraphGenNode := paragraphGenNodeDef.Bind(heart.Into(paraReq)) // Output[ChatCompletionResponse]
	fmt.Printf("INFO: Node 2 '%s' bound and running...\n", "/generate-paragraph")

	// Return the Output handle from the *final* node. The workflow's overall result
	// will be the result of this node.
	return paragraphGenNode
}

// extractContent safely retrieves the message content from an OpenAI response.
func extractContent(resp goopenai.ChatCompletionResponse) (string, error) {
	// (Implementation remains the same)
	if len(resp.Choices) == 0 {
		return "", errors.New("no choices returned from LLM")
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
	fmt.Println("Starting Simpler Chained Workflow Example (Title -> Paragraph)...")

	// --- 1. Setup: API Key and Dependencies ---
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" { /* ... error handling ... */
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}
	client := goopenai.NewClient(apiKey)
	err := heart.Dependencies(openai.Inject(client)) // Inject dependency ONCE
	if err != nil {                                  /* ... error handling ... */
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("INFO: Dependencies injected.")

	// --- 2. Workflow Definition ---
	memStore := store.NewMemoryStore()
	fmt.Println("INFO: Using MemoryStore.")
	// Define the workflow using the simpler handler function.
	titleParaWorkflow := heart.DefineWorkflow(titleAndParagraphWorkflow_Simple, heart.WithStore(memStore))
	fmt.Println("INFO: Workflow defined.")

	// --- 3. Workflow Execution ---
	inputTopic := "The impact of artificial intelligence on creative writing"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Printf("\nINFO: Executing workflow instance for topic: \"%s\"...\n", inputTopic)
	resultHandle := titleParaWorkflow.New(ctx, inputTopic) // Returns Output[goopenai.ChatCompletionResponse]

	// --- 4. Get Result ---
	fmt.Println("INFO: Waiting for final workflow result (Node 2)...")
	// Block until the final node (`paragraphGenNode`) completes.
	finalApiResponse, err := resultHandle.Out()

	if err != nil { /* ... error handling ... */
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "\nERROR: Workflow execution failed: Timeout exceeded.\n")
		} else if errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "\nERROR: Workflow execution failed: Context canceled.\n")
		} else {
			fmt.Fprintf(os.Stderr, "\nERROR: Workflow execution failed: %v\n", err)
		}
		os.Exit(1)
	}

	// Extract the final paragraph text.
	paragraphText, extractErr := extractContent(finalApiResponse)
	if extractErr != nil { /* ... error handling ... */
		fmt.Fprintf(os.Stderr, "\nERROR: Error extracting final paragraph from LLM response: %v\n", extractErr)
		fmt.Fprintf(os.Stderr, "DEBUG: Raw Final Response Choices: %+v\n", finalApiResponse.Choices)
		os.Exit(1)
	}

	// --- 5. Output ---
	fmt.Println("\n--- Workflow Completed Successfully! ---")
	fmt.Printf("Topic: %s\n", inputTopic)
	// We still don't easily have the intermediate title here without querying the store
	fmt.Println("Generated Paragraph:")
	fmt.Println(paragraphText)
	fmt.Println("---------------------------------------")

	// You can still inspect the store to see the two persisted nodes:
	// /generate-title and /generate-paragraph
	// workflowUUID := titleParaWorkflow.GetUUID(resultHandle) // Assuming way to get UUID
	// nodes, _ := memStore.Graphs().ListNodes(context.Background(), workflowUUID.String())
	// fmt.Println("DEBUG: Persisted nodes:", nodes)
}
