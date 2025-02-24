package openai

import (
	"context"
	"encoding/json"
	"heart"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

const structuredOutputNodeTypeID heart.NodeTypeID = "openai:structured-output"

func StructuredOutput[Out any](nodeID heart.NodeID) heart.NodeType[openai.ChatCompletionRequest, Out] {
	return heart.DefineNodeType(nodeID, structuredOutputNodeTypeID, func(in openai.ChatCompletionRequest) heart.Definer[openai.ChatCompletionRequest, Out] {
		return &structuredOutputDefinition[Out]{in, nil}
	})
}

type structuredOutputDefinition[Out any] struct {
	in     openai.ChatCompletionRequest
	client *openai.Client
}

func (s *structuredOutputDefinition[Out]) Define() heart.NodeResolver[Out] {
	return &structuredOutput[Out]{in: s.in, client: s.client}
}

func (s *structuredOutputDefinition[Out]) DependencyInject(client *openai.Client) {
	s.client = client
}

type structuredOutput[Out any] struct {
	in     openai.ChatCompletionRequest
	client *openai.Client
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
	chat, err := s.client.CreateChatCompletion(context.TODO(), s.in)
	if err != nil {
		return o, err
	}

	err = json.Unmarshal([]byte(chat.Choices[0].Message.Content), &o)
	return
}
