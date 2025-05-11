// ./nodes/openai/middleware/structuredoutput.go
package middleware

import (
	"encoding/json"
	"errors"
	"fmt"

	gaf "github.com/stephanoumenos/go-agent-framework"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// Errors related to structured output processing.
var (
	// errStructuredOutputNilNextNodeDef indicates WithStructuredOutput was called with a nil nextNodeDef.
	errStructuredOutputNilNextNodeDef = errors.New("WithStructuredOutput requires a non-nil nextNodeDef")
	// errDuplicatedResponseFormat indicates the input ChatCompletionRequest already had ResponseFormat set.
	errDuplicatedResponseFormat = errors.New("response format already provided in the request")
	// errNoContentFromLLM indicates the LLM response (after potential modification) had no content.
	errNoContentFromLLM = errors.New("no content received from LLM response")
	// errSchemaGenerationFailed indicates failure during JSON schema generation for the target type.
	errSchemaGenerationFailed = errors.New("failed to generate JSON schema")
	// errParsingFailed indicates failure when unmarshalling the LLM's response content into the target struct.
	errParsingFailed = errors.New("failed to parse LLM response")
)

// structuredOutputNodeID is the NodeID used for the node definition created by WithStructuredOutput.
const structuredOutputNodeID gaf.NodeID = "openai_structured_output"

// structuredOutputHandler generates the core WorkflowHandlerFunc for the structured output workflow.
// It captures the 'next' node definition (the actual LLM call) to be used during execution.
// The resulting handler modifies the request to enforce JSON Schema output based on SOut,
// executes the nextNodeDef, and then parses the LLM response into SOut.
func structuredOutputHandler[SOut any](
	nextNodeDef gaf.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
) gaf.WorkflowHandlerFunc[openai.ChatCompletionRequest, SOut] {
	// This is the function that will be executed when the workflow runs.
	return func(ctx gaf.Context, originalReq openai.ChatCompletionRequest) gaf.ExecutionHandle[SOut] {
		if nextNodeDef == nil {
			return gaf.IntoError[SOut](errStructuredOutputNilNextNodeDef)
		}

		// 1. Prepare a zero value of the target struct SOut for schema generation
		// and check for pre-existing ResponseFormat (which would be an error).
		var sOut SOut
		if originalReq.ResponseFormat != nil {
			err := fmt.Errorf("structured output ('%s'): %w", ctx.BasePath, errDuplicatedResponseFormat)
			return gaf.IntoError[SOut](err)
		}

		// 2. Generate the JSON Schema for the target SOut type.
		schema, err := jsonschema.GenerateSchemaForType(sOut)
		if err != nil {
			err = fmt.Errorf("structured output ('%s'): %w for type %T: %v", ctx.BasePath, errSchemaGenerationFailed, sOut, err)
			return gaf.IntoError[SOut](err)
		}

		// 3. Create the modified ChatCompletionRequest.
		modifiedReq := originalReq // Copy base request
		modifiedReq.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:   "output", // This name MUST be referenced in the system prompt.
				Schema: schema,
				// Strict mode might be configurable in the future.
				// Strict: true, // TODO: Consider making strict mode configurable.
			},
		}
		// IMPORTANT: The prompt (usually system message) MUST instruct the model
		// to return JSON matching the schema, referencing the schema name ("output").

		// 4. Start the execution of the *next* node (the actual LLM call) using the modified request.
		llmNodeHandle := nextNodeDef.Start(gaf.Into(modifiedReq))

		// 5. Define a final node using NewNode to parse the LLM response.
		// This node depends on the llmNodeHandle completing successfully.
		parserNodeID := gaf.NodeID("parse_structured_response") // Local ID for the parsing node within the workflow.
		parserNode := gaf.NewNode(
			ctx,          // Use the workflow's context
			parserNodeID, // Assign ID
			func(parseCtx gaf.NewNodeContext) gaf.ExecutionHandle[SOut] { // Returns handle to final SOut type
				// Resolve the result from the underlying LLM node using FanIn + Get.
				llmResponseFuture := gaf.FanIn(parseCtx, llmNodeHandle)
				llmResponse, err := llmResponseFuture.Get() // Blocks until the LLM call completes
				if err != nil {
					// Error already happened in the LLM node, just propagate it, potentially adding context.
					// Error will already contain the failing node's path.
					return gaf.IntoError[SOut](err)
				}

				// Process the LLM response: check for content and unmarshal it into SOut.
				if len(llmResponse.Choices) == 0 || llmResponse.Choices[0].Message.Content == "" {
					finishReason := "unknown"
					if len(llmResponse.Choices) > 0 {
						finishReason = string(llmResponse.Choices[0].FinishReason)
					}
					err = fmt.Errorf("structured output ('%s' -> '%s'): %w (finish reason: %s)",
						ctx.BasePath, parserNodeID, errNoContentFromLLM, finishReason)
					return gaf.IntoError[SOut](err)
				}

				llmContent := llmResponse.Choices[0].Message.Content
				// Attempt to unmarshal the LLM's text response into the target struct SOut.
				err = json.Unmarshal([]byte(llmContent), &sOut)
				if err != nil {
					// Provide detailed error including the type and the content that failed to parse.
					err = fmt.Errorf("structured output ('%s' -> '%s'): %w: failed to unmarshal LLM content into %T: %v. Raw Content: %s",
						ctx.BasePath, parserNodeID, errParsingFailed, sOut, err, llmContent)
					return gaf.IntoError[SOut](err)
				}

				// Return a handle containing the successfully parsed SOut struct.
				return gaf.Into(sOut)
			},
		)

		// 6. Return the handle to the parserNode. Resolving this handle triggers the whole chain.
		return parserNode
	}
}

// WithStructuredOutput defines a NodeDefinition that enforces structured JSON output
// based on a provided Go struct type `SOut`.
//
// It wraps another NodeDefinition (`nextNodeDef`, typically an OpenAI chat completion node)
// and performs the following steps:
//  1. Generates a JSON schema based on the provided `SOut` type.
//  2. Modifies the incoming `openai.ChatCompletionRequest` to include the generated
//     schema in the `ResponseFormat` field, instructing the model to output JSON.
//     *Important*: The system prompt in the request *must* explicitly instruct the
//     model to generate JSON conforming to the schema named "output".
//  3. Executes the wrapped `nextNodeDef` with the modified request.
//  4. Parses the resulting `openai.ChatCompletionResponse`'s content string into
//     an instance of the `SOut` struct.
//
// The resulting NodeDefinition takes an `openai.ChatCompletionRequest` as input
// and produces an `SOut` struct as output.
//
// Parameters:
//   - nextNodeDef: The NodeDefinition of the LLM call node to wrap (e.g., openai.CreateChatCompletion).
//     It must take `openai.ChatCompletionRequest` as input and produce `openai.ChatCompletionResponse`.
//
// Returns:
//   - A NodeDefinition that takes an `openai.ChatCompletionRequest` as input and produces
//     an `SOut` struct as output, encapsulating the structured output logic.
func WithStructuredOutput[SOut any](nextNodeDef gaf.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse]) gaf.NodeDefinition[openai.ChatCompletionRequest, SOut] {
	// Generate the handler function, capturing the nextNodeDef.
	handler := structuredOutputHandler[SOut](nextNodeDef)

	// Define and return the NodeDefinition for this structured output workflow.
	// Use the structuredOutputNodeID as the underlying type identifier.
	return gaf.WorkflowFromFunc(structuredOutputNodeID, handler)
}
