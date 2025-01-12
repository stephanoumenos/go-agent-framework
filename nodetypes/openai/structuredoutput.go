package openai

import (
	"context"
	"encoding/json"
	"ivy"

	"github.com/invopop/jsonschema"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const nodeTypeID ivy.NodeTypeID = "openai:structured-output"

func NodeType[Out any]() ivy.NodeType[openai.ChatCompletionNewParams, Out] {
	return ivy.DefineNodeType(nodeTypeID, func(in openai.ChatCompletionNewParams) ivy.Definer[openai.ChatCompletionNewParams, Out] {
		return &structuredOutputDefinition[Out]{in}
	})
}

type structuredOutputDefinition[Out any] struct {
	in openai.ChatCompletionNewParams
}

func (s *structuredOutputDefinition[Out]) Define() ivy.NodeResolver[Out] {
	return &structuredOutput[Out]{in: s.in}
}

type structuredOutput[Out any] struct {
	in openai.ChatCompletionNewParams
}

func (s *structuredOutput[Out]) Get(ivy.NodeContext) (o Out, err error) {
	// Add schema to the input
	schemaParam := openai.ResponseFormatJSONSchemaJSONSchemaParam{
		Name:        openai.F("output"),                            // TODO: Allow user to set it
		Description: openai.F("The output for the user's request"), // TODO: Allow user to set it
		Schema:      openai.F(generateSchema[Out]()),
		Strict:      openai.Bool(true),
	}
	s.in.ResponseFormat = openai.F[openai.ChatCompletionNewParamsResponseFormatUnion](
		openai.ResponseFormatJSONSchemaParam{
			Type:       openai.F(openai.ResponseFormatJSONSchemaTypeJSONSchema),
			JSONSchema: openai.F(schemaParam),
		},
	)
	client := openai.NewClient(
		option.WithBaseURL("http://localhost:8000/v1/chat"),
	)
	chat, err := client.Chat.Completions.New(context.Background(), s.in)
	if err != nil {
		return o, err
	}

	err = json.Unmarshal([]byte(chat.Choices[0].Message.Content), &o)
	return
}

// Source: https://github.com/openai/openai-go/blob/main/examples/structured-outputs/main.go
func generateSchema[T any]() interface{} {
	// Structured Outputs uses a subset of JSON schema
	// These flags are necessary to comply with the subset
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	var v T
	schema := reflector.Reflect(v)
	return schema
}
