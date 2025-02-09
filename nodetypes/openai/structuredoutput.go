package openai

import (
	"context"
	"encoding/json"
	"heart"
	"net/http"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

const nodeTypeID heart.NodeTypeID = "openai:structured-output"

func StructuredOutput[Out any](nodeID heart.NodeID) heart.NodeType[openai.ChatCompletionRequest, Out] {
	return heart.DefineNodeType(nodeID, nodeTypeID, func(in openai.ChatCompletionRequest) heart.Definer[openai.ChatCompletionRequest, Out] {
		return &structuredOutputDefinition[Out]{in}
	})
}

type structuredOutputDefinition[Out any] struct {
	in openai.ChatCompletionRequest
}

func (s *structuredOutputDefinition[Out]) Define() heart.NodeResolver[Out] {
	return &structuredOutput[Out]{in: s.in}
}

type structuredOutput[Out any] struct {
	in openai.ChatCompletionRequest
}

func (s *structuredOutput[Out]) Get(heart.NodeContext) (o Out, err error) {
	// Add schema to the input
	schema, err := jsonschema.GenerateSchemaForType(o)
	if err != nil {
		return o, err
	}
	s.in.ResponseFormat = &openai.ChatCompletionResponseFormat{
		Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
		JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
			Name:   "output",
			Schema: schema,
			Strict: true,
		},
	}
	client := openai.NewClientWithConfig(
		openai.ClientConfig{
			BaseURL:    "http://localhost:8000/v1",
			HTTPClient: &http.Client{},
		},
	)
	chat, err := client.CreateChatCompletion(context.TODO(), s.in)
	if err != nil {
		return o, err
	}

	err = json.Unmarshal([]byte(chat.Choices[0].Message.Content), &o)
	return
}
