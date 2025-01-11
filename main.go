package main

import (
	"context"
	"encoding/json"
	"golem/golem"
	"golem/nodetypes/requestmapper"
	"golem/nodetypes/streamnode"
	"io"

	"github.com/openai/openai-go"
	option "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

func main() {
	ctx := context.Background()
	client := openai.NewClient(
		option.WithBaseURL("http://localhost:8000/v1/chat"),
	)

	reqMapper := requestmapper.NodeType(func(req io.ReadCloser) (openai.CompletionNewParams, error) {
		var mappedReq openai.CompletionNewParams
		if err := json.NewDecoder(req).Decode(&mappedReq); err != nil {
			return mappedReq, err
		}
		return mappedReq, nil
	})

	golem.HandleFunc("client-internet-troubleshooting", reqMapper, func(ctx golem.WorkflowContext, req openai.CompletionNewParams) error {
		veganNode := golem.NewNode(ctx, streamnode.NodeType(), openai.CompletionNewParams{
			Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
			Model:       openai.F(openai.CompletionNewParamsModel("model/")),
			MaxTokens:   openai.F(int64(512)),
			Temperature: openai.F(1.000000),
		})

		nodeDynamic := golem.NewAgenticNode(ctx, streamnode.NodeType(), func(ctx golem.NodeContext) (openai.CompletionNewParams, error) {
			result, err := veganNode.Get(ctx)

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
