package main

import (
	"context"
	"go-cot/streamnode"
	"go-cot/supervisor"

	"github.com/openai/openai-go"
	option "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

func main() {
	ctx := context.Background()
	client := openai.NewClient(
		option.WithBaseURL("http://localhost:8000/v1/chat"),
	)

	req := openai.CompletionNewParams{
		Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
		Model:       openai.F(openai.CompletionNewParamsModel("model/")),
		MaxTokens:   openai.F(int64(512)),
		Temperature: openai.F(1.000000),
	}

	supervisor.Supervise(ctx, func(ctx context.Context) error {
		question1 := streamnode.NewStreamNode(ctx, client, req)
		resp, err := question1.Get(ctx)

		question2 := streamnode.NewStreamNode(ctx, client, req)
		resp2, err := question2.Get(ctx)
	})

	/*
		models, err := client.Models.List(context.Background())
		if err != nil {
			fmt.Printf("Error listing models: %v\n", err)
			return
		}
		model := models.Data[0].ID
	*/
}
