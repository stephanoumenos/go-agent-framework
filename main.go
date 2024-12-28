package main

import (
	"context"
	"fmt"

	"github.com/openai/openai-go"
	option "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

func main() {
	client := openai.NewClient(
		option.WithBaseURL("http://localhost:8000/v1/chat"),
	)

	/*
		models, err := client.Models.List(context.Background())
		if err != nil {
			fmt.Printf("Error listing models: %v\n", err)
			return
		}
		model := models.Data[0].ID
	*/

	stream := client.Completions.NewStreaming(context.TODO(), openai.CompletionNewParams{
		Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
		Model:       openai.F(openai.CompletionNewParamsModel("model/")),
		MaxTokens:   openai.F(int64(512)),
		Temperature: openai.F(1.000000),
	})

	for stream.Next() {
		evt := stream.Current()
		if len(evt.Choices) > 0 {
			fmt.Println(evt.Choices[0].Text)
		}
	}
}
