package main

import (
	"context"
	"fmt"
	"log"
	"os"

	gaf "go-agent-framework"

	oainode "go-agent-framework/nodes/openai"

	"github.com/sashabaranov/go-openai"
)

func main() {
	ctx := context.Background()

	// 1. Setup OpenAI Client
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Println("OPENAI_API_KEY not set. Skipping live API call for simple_chain_bind example.")
		fmt.Println("To run this example fully, please set your OPENAI_API_KEY.")
		os.Exit(0)
	}
	client := openai.NewClient(apiKey)

	// Inject the OpenAI client dependency.
	// This should be done once, before defining nodes that need the client.
	if err := gaf.Dependencies(oainode.Inject(client)); err != nil {
		log.Fatalf("Error setting up dependencies: %v", err)
	}

	// 2. Define NodeDefinitions for Chat Completion
	chatNodeDef := oainode.CreateChatCompletion("chat_llm")

	// 3. First LLM Call: Generate a creative story idea
	initialRequest := openai.ChatCompletionRequest{
		Model: openai.GPT3Dot5Turbo,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Tell me a short, one-sentence creative story idea.",
			},
		},
		MaxTokens: 50,
	}
	// The input to chatNodeDef.Start is the ChatCompletionRequest itself if the node is defined as
	// NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse].
	// We wrap the request with gaf.Into() to create an ExecutionHandle[openai.ChatCompletionRequest].
	firstCallHandle := chatNodeDef.Start(gaf.Into(initialRequest))

	// 4. Define the mapping function for Bind
	mapStoryIdeaToExpansionRequest := func(response1 openai.ChatCompletionResponse) (openai.ChatCompletionRequest, error) {
		if len(response1.Choices) == 0 || response1.Choices[0].Message.Content == "" {
			return openai.ChatCompletionRequest{}, fmt.Errorf("first LLM call returned no content")
		}
		storyIdea := response1.Choices[0].Message.Content

		fmt.Printf("LLM Call 1 (Story Idea): %s\n\n", storyIdea)

		expansionRequest := openai.ChatCompletionRequest{
			Model: openai.GPT3Dot5Turbo,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: fmt.Sprintf("Expand this one-sentence story idea into a short paragraph: \"%s\"", storyIdea),
				},
			},
			MaxTokens: 150,
		}
		return expansionRequest, nil
	}

	// 5. Use Bind to chain the second LLM call
	secondCallHandle := gaf.Bind(chatNodeDef, firstCallHandle, mapStoryIdeaToExpansionRequest)

	fmt.Println("Executing chained LLM calls...")
	finalResponse, err := gaf.Execute(ctx, secondCallHandle)
	if err != nil {
		log.Fatalf("Error executing GAF graph: %v", err)
	}

	// 7. Print the final result
	if len(finalResponse.Choices) > 0 {
		fmt.Printf("\nLLM Call 2 (Expanded Story):\n%s\n", finalResponse.Choices[0].Message.Content)
	} else {
		fmt.Println("\nSecond LLM call returned no content.")
	}

	fmt.Println("\nSimple chain with Bind example finished.")
}
