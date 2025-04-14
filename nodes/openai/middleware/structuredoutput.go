// ./nodes/openai/middleware/structuredoutput.go
package middleware

import (
	"encoding/json" // For unmarshalling the LLM response
	"errors"
	"fmt"
	"heart" // Use heart's core types and functions

	openai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema" // For schema generation
)

// Errors related to structured output processing.
var (
	errDuplicatedResponseFormat = errors.New("response format already provided in the request")
	errNoContentFromLLM         = errors.New("no content received from LLM response")
	errSchemaGenerationFailed   = errors.New("failed to generate JSON schema")
	errParsingFailed            = errors.New("failed to parse LLM response")
)

// structuredOutputNodeTypeID is the type identifier for the structured output workflow node.
const structuredOutputNodeTypeID heart.NodeTypeID = "openai_structured_output"

// structuredOutputHandler generates the core logic function for the structured output workflow.
// It captures the 'next' node definition (the actual LLM call) to be used during execution.
func structuredOutputHandler[SOut any](
	nextNodeDef heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
) heart.WorkflowHandlerFunc[openai.ChatCompletionRequest, SOut] {
	// This is the function that will be executed when the workflow runs.
	return func(ctx heart.Context, originalReq openai.ChatCompletionRequest) heart.ExecutionHandle[SOut] {

		// 1. Prepare a zero value of the target struct SOut for schema generation
		// and check for pre-existing ResponseFormat (which would be an error).
		var sOut SOut
		if originalReq.ResponseFormat != nil {
			err := fmt.Errorf("structured output ('%s'): %w", ctx.BasePath, errDuplicatedResponseFormat)
			return heart.IntoError[SOut](err)
		}

		// 2. Generate the JSON Schema for the target SOut type.
		schema, err := jsonschema.GenerateSchemaForType(sOut)
		if err != nil {
			err = fmt.Errorf("structured output ('%s'): %w for type %T: %v", ctx.BasePath, errSchemaGenerationFailed, sOut, err)
			return heart.IntoError[SOut](err)
		}

		// 3. Create the modified ChatCompletionRequest.
		modifiedReq := originalReq // Copy base request
		modifiedReq.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:   "output", // This name likely needs to match instruction in prompt
				Schema: schema,
				Strict: true,
			},
		}
		// It's crucial to instruct the model in the prompt to return JSON matching the desired structure.
		// e.g., add a system message: "You MUST respond ONLY with a JSON object matching the following schema: <schema description or reference>"

		// 4. Start the execution of the *next* node (the actual LLM call) using the modified request.
		// Use the workflow's context (ctx) for correct pathing and registry scope.
		llmNodeHandle := nextNodeDef.Start(heart.Into(modifiedReq))

		// 5. Define a final node using NewNode to parse the LLM response.
		// This node depends on the llmNodeHandle completing successfully.
		parserNodeID := heart.NodeID("parse_structured_response") // Local ID for the parsing node
		parserNode := heart.NewNode(
			ctx,          // Use the workflow's context
			parserNodeID, // Assign ID
			func(parseCtx heart.NewNodeContext) heart.ExecutionHandle[SOut] { // Returns handle to final SOut type
				// Resolve the result from the underlying LLM node using FanIn + Get.
				llmResponseFuture := heart.FanIn(parseCtx, llmNodeHandle)
				llmResponse, err := llmResponseFuture.Get() // Blocks until the LLM call completes
				if err != nil {
					// Error already happened in the LLM node, just propagate it.
					return heart.IntoError[SOut](err)
				}

				// Process the LLM response: check for content and unmarshal it into SOut.
				if len(llmResponse.Choices) == 0 || llmResponse.Choices[0].Message.Content == "" {
					finishReason := "unknown"
					if len(llmResponse.Choices) > 0 {
						finishReason = string(llmResponse.Choices[0].FinishReason)
					}
					err = fmt.Errorf("structured output ('%s' -> '%s'): %w (finish reason: %s)",
						ctx.BasePath, parserNodeID, errNoContentFromLLM, finishReason)
					return heart.IntoError[SOut](err)
				}

				llmContent := llmResponse.Choices[0].Message.Content
				// Attempt to unmarshal the LLM's text response into the target struct SOut.
				err = json.Unmarshal([]byte(llmContent), &sOut)
				if err != nil {
					// Provide detailed error including the type and the content that failed to parse.
					err = fmt.Errorf("structured output ('%s' -> '%s'): %w: failed to unmarshal LLM content into %T: %v. Raw Content: %s",
						ctx.BasePath, parserNodeID, errParsingFailed, sOut, err, llmContent)
					return heart.IntoError[SOut](err)
				}

				// Return a handle containing the successfully parsed SOut struct.
				return heart.Into(sOut)
			},
		)

		// 6. Return the handle to the parserNode. Resolving this handle triggers the whole chain.
		return parserNode
	}
}

// WithStructuredOutput defines a NodeDefinition that enforces structured JSON output.
// It wraps another NodeDefinition (typically an OpenAI call) and modifies the request
// to ask for JSON output, then parses the result into the specified SOut type.
//
// Parameters:
//   - nodeID: A unique identifier for this specific instance of the structured output workflow.
//   - nextNodeDef: The NodeDefinition of the LLM call node to wrap (e.g., openai.CreateChatCompletion).
//
// Returns:
//
//	A NodeDefinition that takes an openai.ChatCompletionRequest as input and produces
//	an SOut struct as output.
func WithStructuredOutput[SOut any](
	nodeID heart.NodeID, // User-provided ID for this workflow instance.
	nextNodeDef heart.NodeDefinition[openai.ChatCompletionRequest, openai.ChatCompletionResponse],
) heart.NodeDefinition[openai.ChatCompletionRequest, SOut] { // Input: Request, Output: SOut
	if nodeID == "" {
		panic("WithStructuredOutput requires a non-empty nodeID")
	}
	if nextNodeDef == nil {
		panic("WithStructuredOutput requires a non-nil nextNodeDef")
	}

	// Generate the handler function, capturing the nextNodeDef.
	handler := structuredOutputHandler[SOut](nextNodeDef)

	// Create a workflow resolver using the generated handler function.
	workflowResolver := heart.NewWorkflowResolver(nodeID, handler)

	// Define and return the NodeDefinition for this structured output workflow.
	// Use the structuredOutputNodeTypeID as the underlying type identifier.
	return heart.DefineNode(
		nodeID,           // Use the user-provided nodeID for this specific instance
		workflowResolver, // The resolver containing the workflow logic
	)
}
