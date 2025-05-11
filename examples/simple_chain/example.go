package main

import (
	"context"
	"fmt"
	"log"
	"os"

	gaf "go-agent-framework"

	// oainode "go-agent-framework/nodes/openai"
	"go-agent-framework/nodes/openai" // Assuming package name is 'openai'

	// "github.com/sashabaranov/go-openai"
	goopenai "github.com/sashabaranov/go-openai"
)

// storyWorkflowHandler defines the logic for generating and expanding a story idea.
// It takes a gaf.Context and an initial prompt string, and returns a handle to the final expanded story.
func storyWorkflowHandler(gctx gaf.Context, initialPrompt string) gaf.ExecutionHandle[goopenai.ChatCompletionResponse] {
	// NodeDefinition can be created here or passed in if it's shared.
	chatNodeDef := openai.CreateChatCompletion("chat_llm") // Assuming package name is 'openai'

	// 1. First LLM Call: Generate a creative story idea
	initialRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT3Dot5Turbo,
		Messages: []goopenai.ChatCompletionMessage{
			{
				Role:    goopenai.ChatMessageRoleUser,
				Content: initialPrompt,
			},
		},
		MaxTokens: 50,
	}
	firstCallHandle := chatNodeDef.Start(gaf.Into(initialRequest))

	// 2. Define the mapping function for Bind
	mapStoryIdeaToExpansionRequest := func(response1 goopenai.ChatCompletionResponse) (goopenai.ChatCompletionRequest, error) {
		if len(response1.Choices) == 0 || response1.Choices[0].Message.Content == "" {
			return goopenai.ChatCompletionRequest{}, fmt.Errorf("first LLM call returned no content")
		}
		storyIdea := response1.Choices[0].Message.Content
		fmt.Printf("LLM Call 1 (Story Idea): %s\n\n", storyIdea)

		expansionRequest := goopenai.ChatCompletionRequest{
			Model: goopenai.GPT3Dot5Turbo,
			Messages: []goopenai.ChatCompletionMessage{
				{
					Role:    goopenai.ChatMessageRoleUser,
					Content: fmt.Sprintf("Expand this one-sentence story idea into a short paragraph: \"%s\"", storyIdea),
				},
			},
			MaxTokens: 150,
		}
		return expansionRequest, nil
	}

	// 3. Use Bind to chain the second LLM call
	secondCallHandle := gaf.Bind(chatNodeDef, firstCallHandle, mapStoryIdeaToExpansionRequest)
	return secondCallHandle
}

// Define the reusable workflow blueprint.
var storyChainWorkflow = gaf.WorkflowFromFunc(gaf.NodeID("storyChainGenerator"), storyWorkflowHandler)

func main() {
	ctx := context.Background()

	// 1. Setup OpenAI Client
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Println("OPENAI_API_KEY not set. Skipping live API call for simple_chain_bind example.")
		fmt.Println("To run this example fully, please set your OPENAI_API_KEY.")
		os.Exit(0)
	}
	// client := openai.NewClient(apiKey)
	client := goopenai.NewClient(apiKey)

	// Inject the OpenAI client dependency.
	// This should be done once, before defining nodes that need the client.
	// if err := gaf.Dependencies(oainode.Inject(client)); err != nil {
	if err := gaf.Dependencies(openai.Inject(client)); err != nil { // Assuming package name is 'openai'
		log.Fatalf("Error setting up dependencies: %v", err)
	}

	// Define the input for this specific workflow run.
	workflowInput := "Tell me a short, one-sentence creative story idea."

	fmt.Println("Executing story chain workflow...")
	// Execute triggers the lazy computation graph and waits for the final result.
	finalResponse, err := gaf.ExecuteWorkflow(ctx, storyChainWorkflow, workflowInput)
	if err != nil {
		log.Fatalf("Error executing GAF graph: %v", err)
	}

	// Print the final result
	if len(finalResponse.Choices) > 0 {
		fmt.Printf("\nLLM Call 2 (Expanded Story):\n%s\n", finalResponse.Choices[0].Message.Content)
	} else {
		fmt.Println("\nSecond LLM call returned no content.")
	}

	fmt.Println("\nSimple chain with Bind example (using workflow) finished.")
}
