// ./examples/structuredoutput/example_test.go
package main

import (
	"context"
	"encoding/json" // Used to marshal mock recipe and simulate invalid JSON.
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	gaf "go-agent-framework"
	"go-agent-framework/nodes/openai" // Provides OpenAI node and DI injection.
	"go-agent-framework/store"        // Provides storage options (MemoryStore used here).

	goopenai "github.com/sashabaranov/go-openai" // OpenAI Go client library.
	"github.com/stretchr/testify/assert"         // For non-fatal assertions.
	"github.com/stretchr/testify/require"        // For fatal assertions.
)

// --- Mock OpenAI Client for Structured Output ---

// mockStructuredOutputOpenAIClient implements the openai.ClientInterface specifically
// for testing the WithStructuredOutput middleware. It allows configuring the response
// to simulate successful JSON generation, errors from the API, or invalid JSON output.
type mockStructuredOutputOpenAIClient struct {
	t *testing.T

	expectedSystemPrompt    string
	expectedUserPromptTopic string

	mockRecipe        Recipe
	err               error
	returnInvalidJSON bool

	callsMtx sync.Mutex
	calls    []goopenai.ChatCompletionRequest
}

// CreateChatCompletion implements the clientiface.ClientInterface for the mock.
// It records the call, validates the request against expectations (especially the
// ResponseFormat added by the middleware), and returns a configured response or error.
func (m *mockStructuredOutputOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.callsMtx.Lock()
	m.calls = append(m.calls, req)
	m.callsMtx.Unlock()

	if err := ctx.Err(); err != nil {
		fmt.Printf("[Mock OpenAI StructOut Test] Context cancelled or deadline exceeded\n")
		return goopenai.ChatCompletionResponse{}, err
	}

	require.NotNil(m.t, req.ResponseFormat, "Mock received request with nil ResponseFormat")
	require.Equal(m.t, goopenai.ChatCompletionResponseFormatTypeJSONSchema, req.ResponseFormat.Type, "Mock received wrong ResponseFormat Type")
	require.NotNil(m.t, req.ResponseFormat.JSONSchema, "Mock received request with nil JSONSchema field in ResponseFormat")
	assert.Equal(m.t, "output", req.ResponseFormat.JSONSchema.Name, "Mock received wrong JSONSchema Name")
	require.NotNil(m.t, req.ResponseFormat.JSONSchema.Schema, "Mock received request with nil Schema definition")

	if len(req.Messages) >= 2 {
		assert.Contains(m.t, req.Messages[0].Content, m.expectedSystemPrompt, "Mock received unexpected system prompt content")
		assert.Contains(m.t, req.Messages[1].Content, m.expectedUserPromptTopic, "Mock received unexpected user prompt content")
	} else {
		assert.Fail(m.t, "Mock expected at least 2 messages (system, user) in the request")
	}

	if m.err != nil {
		fmt.Printf("[Mock OpenAI StructOut Test] Returning predefined error: %v\n", m.err)
		return goopenai.ChatCompletionResponse{}, m.err
	}

	if m.returnInvalidJSON {
		fmt.Printf("[Mock OpenAI StructOut Test] Returning invalid JSON response\n")
		return goopenai.ChatCompletionResponse{
			Choices: []goopenai.ChatCompletionChoice{
				{
					Message: goopenai.ChatCompletionMessage{
						Role:    goopenai.ChatMessageRoleAssistant,
						Content: `{"recipe_name": "Invalid JSON, "ingredients": ["missing quote]}`,
					},
					FinishReason: goopenai.FinishReasonStop,
				},
			},
		}, nil
	}

	recipeJSON, marshalErr := json.Marshal(m.mockRecipe)
	require.NoError(m.t, marshalErr, "Failed to marshal mock recipe in mock client")

	fmt.Printf("[Mock OpenAI StructOut Test] Returning valid JSON response based on mockRecipe\n")
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{
			{
				Message: goopenai.ChatCompletionMessage{
					Role:    goopenai.ChatMessageRoleAssistant,
					Content: string(recipeJSON),
				},
				FinishReason: goopenai.FinishReasonStop,
			},
		},
	}, nil
}

// --- Test Function: Success Case ---

// TestStructuredOutputWorkflowIntegration tests the successful execution path of the
// structured output workflow using the mock client.
func TestStructuredOutputWorkflowIntegration(t *testing.T) {
	t.Cleanup(func() {
		gaf.ResetDependencies()
	})

	mockRecipe := Recipe{
		RecipeName:  "Mock Vegan Chocolate Chip Cookies",
		Ingredients: []string{"Flour", "Sugar", "Vegan Butter", "Vegan Chocolate Chips", "Vanilla Extract", "Baking Soda"},
		Steps:       []string{"Preheat oven", "Mix ingredients", "Form cookies", "Bake"},
		PrepTime:    "20 minutes",
		Difficulty:  "Easy",
	}
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Quick vegan chocolate chip cookies",
		mockRecipe:              mockRecipe,
		err:                     nil,
		returnInvalidJSON:       false,
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	err := gaf.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	workflowID := gaf.NodeID("recipeGeneratorWorkflowTest")
	recipeWorkflowDef := gaf.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	inputTopic := "Quick vegan chocolate chip cookies"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	recipeResult, err := gaf.ExecuteWorkflow(ctx, recipeWorkflowDef, inputTopic, gaf.WithStore(memStore))

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("Test timed out waiting for workflow result")
	}
	require.NoError(t, err, "Workflow execution failed unexpectedly")

	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()

	require.Len(t, mockClient.calls, 1, "Expected exactly one call to the OpenAI mock client")

	assert.Equal(t, mockRecipe.RecipeName, recipeResult.RecipeName, "RecipeName mismatch")
	assert.Equal(t, mockRecipe.PrepTime, recipeResult.PrepTime, "PrepTime mismatch")
	assert.ElementsMatch(t, mockRecipe.Ingredients, recipeResult.Ingredients, "Ingredients mismatch")
	assert.Equal(t, mockRecipe.Steps, recipeResult.Steps, "Steps mismatch")
	assert.Equal(t, mockRecipe.Difficulty, recipeResult.Difficulty, "Difficulty mismatch")
}

// --- Test Function: LLM API Error Case ---

// TestStructuredOutputWorkflowIntegration_LLMError tests the scenario where the
// underlying OpenAI API call (simulated by the mock) returns an error.
func TestStructuredOutputWorkflowIntegration_LLMError(t *testing.T) {
	t.Cleanup(func() {
		gaf.ResetDependencies()
	})

	expectedError := errors.New("simulated API error from mock")
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Error case topic",
		err:                     expectedError,
		returnInvalidJSON:       false,
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	err := gaf.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	workflowID := gaf.NodeID("recipeGeneratorWorkflowErrorTest")
	recipeWorkflowDef := gaf.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	inputTopic := "Error case topic"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = gaf.ExecuteWorkflow(ctx, recipeWorkflowDef, inputTopic, gaf.WithStore(memStore))

	require.Error(t, err, "Workflow execution should have failed due to simulated API error")
	require.ErrorContains(t, err, "simulated API error")
	require.ErrorContains(t, err, "openai_structured_output:#0/parse_structured_response")

	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()
	require.Len(t, mockClient.calls, 1, "Expected one call to OpenAI mock even when returning an error")
}

// --- Test Function: JSON Parsing Error Case ---

// TestStructuredOutputWorkflowIntegration_ParsingError tests the scenario where the
// LLM (simulated by the mock) returns a response, but the content is not valid JSON.
// It verifies that the middleware detects the parsing error and returns an appropriate
// error message.
func TestStructuredOutputWorkflowIntegration_ParsingError(t *testing.T) {
	t.Cleanup(func() {
		gaf.ResetDependencies()
	})

	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Bad JSON topic",
		err:                     nil,
		returnInvalidJSON:       true,
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	err := gaf.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	workflowID := gaf.NodeID("recipeGeneratorWorkflowParseErrorTest")
	recipeWorkflowDef := gaf.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	inputTopic := "Bad JSON topic"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = gaf.ExecuteWorkflow(ctx, recipeWorkflowDef, inputTopic, gaf.WithStore(memStore))

	require.Error(t, err, "Workflow execution should have failed due to JSON parsing error")
	require.ErrorContains(t, err, "failed to parse LLM response")
	require.ErrorContains(t, err, "failed to unmarshal LLM content into main.Recipe")
	require.ErrorContains(t, err, "openai_structured_output:#0/parse_structured_response")
	require.ErrorContains(t, err, `Raw Content: {"recipe_name": "Invalid JSON, "ingredients": ["missing quote]}`)

	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()
	require.Len(t, mockClient.calls, 1, "Expected one call to OpenAI mock even on parsing error")
}
