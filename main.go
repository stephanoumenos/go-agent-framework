package main

import (
	"context"
	"fmt"

	openai "github.com/openai/openai-go"
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

	chatCompletion, err := client.Completions.New(context.TODO(), openai.CompletionNewParams{
		Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
		Model:       openai.F(openai.CompletionNewParamsModel("Qwen/Qwen2.5-7B-Instruct")),
		MaxTokens:   openai.F(int64(512)),
		Temperature: openai.F(1.000000),
	})
	if err != nil {
		fmt.Printf("Error creating completion: %v\n", err)
		return
	}

	fmt.Println(chatCompletion.Choices[0].Text)
}
