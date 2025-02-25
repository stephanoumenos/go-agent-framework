package openai

import (
	"context"
	"encoding/json"
	"heart"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

const structuredOutputNodeTypeID heart.NodeTypeID = "openai:structured-output"

func StructuredOutput[Out any](nodeID heart.NodeID) heart.NodeBuilder[openai.ChatCompletionRequest, Out] {
	return heart.DefineNodeBuilder[openai.ChatCompletionRequest, Out](nodeID, &structuredOutput[Out]{})
}

type structuredOutputInitializer struct {
	client *openai.Client
}

func (s *structuredOutputInitializer) ID() heart.NodeTypeID {
	return structuredOutputNodeTypeID
}

func (s *structuredOutputInitializer) DependencyInject(client *openai.Client) {
	s.client = client
}

type structuredOutput[Out any] struct {
	*structuredOutputInitializer
}

func (s *structuredOutput[Out]) Init() heart.NodeInitializer {
	return s.structuredOutputInitializer
}

func (s *structuredOutput[Out]) Get(ctx heart.NodeContext, in openai.ChatCompletionRequest) (o Out, err error) {
	// Add schema to the input
	schema, err := jsonschema.GenerateSchemaForType(o)
	if err != nil {
		return o, err
	}
	in.ResponseFormat = &openai.ChatCompletionResponseFormat{
		Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
		JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
			Name:   "output",
			Schema: schema,
			Strict: true,
		},
	}
	chat, err := s.client.CreateChatCompletion(context.TODO(), in)
	if err != nil {
		return o, err
	}

	err = json.Unmarshal([]byte(chat.Choices[0].Message.Content), &o)
	return
}
