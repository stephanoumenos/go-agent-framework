package main

import (
	"context"
	"fmt"
	"go-cot/streamnode"

	_ "github.com/mattn/go-sqlite3"
	"github.com/openai/openai-go"
	option "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

func main() {
	req := openai.CompletionNewParams{
		Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
		Model:       openai.F(openai.CompletionNewParamsModel("model/")),
		MaxTokens:   openai.F(int64(512)),
		Temperature: openai.F(1.000000),
	}

	node := streamnode.NewStreamNode(req)

	client := openai.NewClient(
		option.WithBaseURL("http://localhost:8000/v1/chat"),
	)
	ctx := context.Background()

	node.Start(ctx, client)
	for node.Next() {
		fmt.Println(node.Token())
	}

	/*
		models, err := client.Models.List(context.Background())
		if err != nil {
			fmt.Printf("Error listing models: %v\n", err)
			return
		}
		model := models.Data[0].ID
	*/
}
