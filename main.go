package main

import (
	"context"
	"golem/golem"
	"golem/golem/node"
	"golem/streamnode"

	"github.com/openai/openai-go"
	option "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

func main() {
	ctx := context.Background()
	client := openai.NewClient(
		option.WithBaseURL("http://localhost:8000/v1/chat"),
	)

	golem.HandleFunc("client-internet-troubleshooting", func(ctx golem.Context) error {
		veganNode := node.New(ctx, streamnode.Type(), openai.CompletionNewParams{
			Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
			Model:       openai.F(openai.CompletionNewParamsModel("model/")),
			MaxTokens:   openai.F(int64(512)),
			Temperature: openai.F(1.000000),
		})

		nodeDynamic := node.NewDynamic(ctx, streamnode.Type(), func(node.Context) (openai.CompletionNewParams, error) {
			return openai.CompletionNewParams{
				Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
				Model:       openai.F(openai.CompletionNewParamsModel("model/")),
				MaxTokens:   openai.F(int64(512)),
				Temperature: openai.F(1.000000),
			}, nil
		})

		return nil
	})
}
