package main

import (
	"context"
	"encoding/json"
	"io"
	"ivy"
	"ivy/nodetypes/mapper"
	"ivy/nodetypes/streamnode"
	"strings"

	"github.com/openai/openai-go"
	option "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

func main() {
	ctx := context.Background()
	client := openai.NewClient(
		option.WithBaseURL("http://localhost:8000/v1/chat"),
	)

	reqMapper := mapper.NodeType(func(req io.ReadCloser) (openai.CompletionNewParams, error) {
		var mappedReq openai.CompletionNewParams
		if err := json.NewDecoder(req).Decode(&mappedReq); err != nil {
			return mappedReq, err
		}
		return mappedReq, nil
	})

	ivy.Workflow("client-internet-troubleshooting", reqMapper, func(ctx ivy.WorkflowContext, req openai.CompletionNewParams) (ivy.WorkflowOutput, error) {
		veganNode := ivy.StaticNode(ctx, streamnode.NodeType(), openai.CompletionNewParams{
			Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
			Model:       openai.F(openai.CompletionNewParamsModel("model/")),
			MaxTokens:   openai.F(int64(512)),
			Temperature: openai.F(1.000000),
		})

		// Capitalize the activist response.
		capslock := ivy.Node(
			ctx,
			mapper.NodeType(func(req string) (string, error) {
				return strings.ToUpper(req), nil
			}),
			func(nc ivy.NodeContext) (string, error) {
				bestActivist, err := veganNode.Get(nc)
				if err != nil {
					return "", err
				}
				return bestActivist.Output.Result, nil
			},
		)

		nodeDynamic := ivy.Node(ctx, streamnode.NodeType(), func(ctx ivy.NodeContext) (openai.CompletionNewParams, error) {
			result, err := veganNode.Get(ctx)

			return openai.CompletionNewParams{
				Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
				Model:       openai.F(openai.CompletionNewParamsModel("model/")),
				MaxTokens:   openai.F(int64(512)),
				Temperature: openai.F(1.000000),
			}, nil
		})

		return nil, nil
	})

	veganNode := ivy.StaticNode(ctx, streamnode.NodeType(), openai.CompletionNewParams{
		Prompt:      openai.F[openai.CompletionNewParamsPromptUnion](shared.UnionString("You are the best vegan activist that has ever existed.")),
		Model:       openai.F(openai.CompletionNewParamsModel("model/")),
		MaxTokens:   openai.F(int64(512)),
		Temperature: openai.F(1.000000),
	})
}
