package middleware

import (
	"encoding/json"
	"errors"
	"fmt"
	"heart"

	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

var errDuplicatedResponseFormat = errors.New("response format already provided")

func WithStructuredOutput[StructuredOutputStruct any](next heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]) heart.NodeDefinition[openai.ChatCompletionRequest, StructuredOutputStruct] {
	return heart.DefineThinMiddleware(middleware[StructuredOutputStruct], next)
}

func middleware[SOut any](ctx heart.Context, in openai.ChatCompletionRequest, next heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]) (SOut, error) {
	var sOut SOut

	// TODO: Try this nil trick later to avoid memory allocation
	// schema, err := jsonschema.GenerateSchemaForType((*SOut)(nil))

	if in.ResponseFormat != nil {
		return sOut, fmt.Errorf("error in openai structured output middleware: %w", errDuplicatedResponseFormat)
	}

	// Add schema to the input
	schema, err := jsonschema.GenerateSchemaForType(sOut)
	if err != nil {
		return sOut, fmt.Errorf("error in openai structured output middleware: error generating schema: %w", err)
	}

	in.ResponseFormat = &openai.ChatCompletionResponseFormat{
		Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
		JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
			Name:   "output",
			Schema: schema,
			Strict: true,
		},
	}

	out, err := next.Input(in).Get(ctx)
	if err != nil {
		return sOut, fmt.Errorf("error in openai structured output middleware: error from ChatCompletionRequest node: %w", err)
	}

	err = json.Unmarshal([]byte(out.Output.Choices[0].Message.Content), &sOut)
	return sOut, err
}
