package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"ivy"
	"ivy/nodetypes/mapper"
	nt "ivy/nodetypes/openai"

	openai "github.com/sashabaranov/go-openai"
)

type VeganQuestions struct {
	CarnistArgumentOne   string `json:"carnist_argument_one" jsonschema_description:"First carnist argument that is implicitly present in the person's phrase"`
	CarnistArgumentTwo   string `json:"carnist_argument_two" jsonschema_description:"Second carnist argument that is implicitly present in the person's phrase"`
	CarnistArgumentThree string `json:"carnist_argument_three" jsonschema_description:"Third carnist argument that is implicitly present in the person's phrase"`
}

var systemMsg = `
You are an AI trained to analyze anti-vegan messages and identify the core carnist arguments being made. When presented with a message, extract exactly three main carnist arguments that are either explicitly stated or implicitly present in the text.
For each argument, provide a clear, concise description that captures the essential reasoning or claim being made. Focus on the underlying logic or justification being used to defend non-vegan practices, rather than just paraphrasing the surface-level statements.
Your output must be structured in the JSON format specified by the provided schema.
`

var carnistUserMsg = `"Look, veganism is completely unnatural - our ancestors have been eating meat for millions of years and that's just how nature intended it. Plus, studies show that plants actually feel pain too, so you're not really saving anything by eating them instead of animals. And what about all the small family farms that would go bankrupt if everyone stopped eating meat? You're just trying to destroy people's livelihoods with your extreme ideology."`

func main() {
	reqMapper := mapper.NodeType(func(req io.ReadCloser) (mapped openai.ChatCompletionRequest, err error) {
		json.NewDecoder(req).Decode(&mapped)
		return
	})

	ivy.Workflow("carnist-debunker", reqMapper, func(ctx ivy.WorkflowContext, req openai.ChatCompletionRequest) (ivy.WorkflowOutput[VeganQuestions], error) {
		questions := nt.NodeType[VeganQuestions]().
			StaticInput(req)

		marshaled := ivy.Transform(questions, func(req VeganQuestions) (io.ReadCloser, error) {
			data, err := json.Marshal(req)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(data)), nil
		})

		return marshaled, nil
	})

	// Test
	req := openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem},
			{Content: carnistUserMsg, Role: openai.ChatMessageRoleUser},
		},
		Model:       "model/",
		MaxTokens:   2048,
		Temperature: 1.000000,
	}
	marshalled, err := json.Marshal(req)
	if err != nil {
		return
	}
	out, err := ivy.RunWorkflow("carnist-debunker", io.NopCloser(bytes.NewReader(marshalled)))
	if err != nil {
		fmt.Println("error runnign workflow: ", err)
		return
	}
	fmt.Println("Success!")
	data, _ := io.ReadAll(out)
	fmt.Println(string(data))
}
