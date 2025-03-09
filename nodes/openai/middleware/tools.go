package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"heart"

	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

var (
	errToolsAlreadyDefined   = errors.New("tools already defined. please define only once")
	errUnknownToolFromOpenAI = errors.New("openAI requested unknown tool")
)

type Tool interface {
	parseParams(map[string]string) (any, error)
	defineTool() ToolDefinition
	call(ctx context.Context, params any) (string, error)
}

type ToolParam struct {
	Description string
	Enum        []string
	IsRequired  bool
}

type ToolDefinition struct {
	Name        string
	Description string
	Params      map[string]ToolParam
}

type ToolDefiner[Params any] interface {
	heart.NodeResolver[Params, string]
	ParseParams(map[string]string) (Params, error)
	DefineTool() ToolDefinition
}

type tool[Params any] struct {
	definer ToolDefiner[Params]
}

func (t *tool[Params]) parseParams(paramsMap map[string]string) (any, error) {
	return t.definer.ParseParams(paramsMap)
}

func (t *tool[Params]) defineTool() ToolDefinition {
	t.definer.Init()
	// TODO: Add dependency injection here
	return t.definer.DefineTool()
}

func (t *tool[Params]) call(ctx context.Context, params any) (string, error) {
	typedParams, ok := params.(Params)
	if !ok {
		return "", fmt.Errorf("TODO")
	}
	return t.definer.Get(ctx, typedParams)
}

func DefineTool[Params any](definer ToolDefiner[Params]) Tool {
	return &tool[Params]{definer: definer}
}

func WithTools(next heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse], tools ...Tool) heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse] {
	return heart.DefineThinMiddleware(toolsMiddleware(tools), next)
}

func toolsMiddleware(tools []Tool) func(heart.NoderGetter, openai.ChatCompletionRequest, heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]) (openai.ChatCompletionResponse, error) {
	return func(get heart.NoderGetter, ccr openai.ChatCompletionRequest, nd heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]) (openai.ChatCompletionResponse, error) {
		var out openai.ChatCompletionResponse
		if len(ccr.Tools) > 0 {
			return out, fmt.Errorf("error in openai tools middleware: %w", errToolsAlreadyDefined)
		}
		openaiTools := make([]openai.Tool, 0, len(tools))
		toolRefs := make(map[string]Tool, len(tools)) // ToolID -> Tool
		for _, tool := range tools {
			tool := tool // Just in case for older Go versions
			def := tool.defineTool()
			toolRefs[def.Name] = tool
			paramProperties := make(map[string]jsonschema.Definition, len(def.Params))
			requiredParams := make([]string, 0)
			for paramID, paramDef := range def.Params {
				paramProperties[paramID] = jsonschema.Definition{
					Type:        jsonschema.String,
					Description: paramDef.Description,
					Enum:        paramDef.Enum,
				}
				if paramDef.IsRequired {
					requiredParams = append(requiredParams, paramID)
				}
			}
			openaiTools = append(openaiTools, openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        def.Name,
					Description: def.Description,
					Parameters: jsonschema.Definition{
						Type:       jsonschema.Object,
						Properties: paramProperties,
						Required:   requiredParams,
					},
				},
			})
		}
		ccr.Tools = openaiTools

		resp, err := nd.Bind(heart.Into(ccr)).Out(get)
		if err != nil {
			return out, err
		}
		toolCalls := resp.Choices[0].Message.ToolCalls
		toolsCalled := len(toolCalls)
		// TODO: Add a few more checks here
		for toolsCalled > 0 {
			for _, toolCall := range toolCalls {
				tool, ok := toolRefs[toolCall.Function.Name]
				if !ok {
					return out, fmt.Errorf("error in openai tools middleware: %w", errUnknownToolFromOpenAI)
				}
				// TODO: Parse JSON here.
				var functionArguments map[string]interface{}
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &functionArguments); err != nil {
					return out, err
				}

				paramsMap := make(map[string]string)
				for key, value := range functionArguments {
					switch v := value.(type) {
					case string:
						paramsMap[key] = v
					case float64:
						paramsMap[key] = fmt.Sprintf("%v", v)
					case bool:
						paramsMap[key] = fmt.Sprintf("%v", v)
					case nil:
						paramsMap[key] = ""
					default:
						return out, errors.New("nested object found: value for key '" + key + "' is not a simple type")
					}
				}

				params, err := tool.parseParams(paramsMap)
				if err != nil {
					return out, err
				}

				toolOut, err := tool.call(context.TODO(), params)
				if err != nil {
					return out, err
				}
				ccr.Messages = append(ccr.Messages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    toolOut,
					Name:       toolCall.Function.Name,
					ToolCallID: toolCall.ID,
				})
			}

			resp, err := nd.Bind(heart.Into(ccr)).Out(get)
			if err != nil {
				return out, err
			}
			toolCalls = resp.Choices[0].Message.ToolCalls
			toolsCalled = len(toolCalls)
		}

		return resp, nil
	}
}
