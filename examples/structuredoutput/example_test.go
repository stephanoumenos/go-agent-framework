// ./examples/structuredoutput/example_test.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai"
	"heart/store"
	"sync"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock OpenAI Client ---
type mockStructuredOutputOpenAIClient struct {
	t                       *testing.T // To fail test on unexpected input
	expectedSystemPrompt    string
	expectedUserPromptTopic string
	mockRecipe              Recipe
	err                     error
	returnInvalidJSON       bool // Flag to control response content
	callsMtx                sync.Mutex
	calls                   []goopenai.ChatCompletionRequest
}

// CreateChatCompletion implements the clientiface.ClientInterface.
func (m *mockStructuredOutputOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.callsMtx.Lock()
	m.calls = append(m.calls, req)
	m.callsMtx.Unlock()

	// Check context cancellation
	if err := ctx.Err(); err != nil {
		fmt.Printf("[Mock OpenAI StructOut Test] Context cancelled or deadline exceeded\n")
		return goopenai.ChatCompletionResponse{}, err
	}

	// Basic validation of the request structure modified by the middleware
	// Use require inside the mock, attached to the test 't'
	require.NotNil(m.t, req.ResponseFormat, "Mock received nil ResponseFormat")
	require.Equal(m.t, goopenai.ChatCompletionResponseFormatTypeJSONSchema, req.ResponseFormat.Type, "Mock received wrong ResponseFormat Type")
	require.NotNil(m.t, req.ResponseFormat.JSONSchema, "Mock received nil JSONSchema")
	require.Equal(m.t, "output", req.ResponseFormat.JSONSchema.Name, "Mock received wrong JSONSchema Name")
	require.NotNil(m.t, req.ResponseFormat.JSONSchema.Schema, "Mock received nil Schema definition")

	// Validate prompts (optional but good practice)
	// Ensure enough messages before accessing them
	if len(req.Messages) >= 2 {
		assert.Contains(m.t, req.Messages[0].Content, m.expectedSystemPrompt, "Mock received unexpected system prompt")
		assert.Contains(m.t, req.Messages[1].Content, m.expectedUserPromptTopic, "Mock received unexpected user prompt content")
	} else {
		// Use assert.Fail to report the error without stopping the test in the mock
		assert.Fail(m.t, "Mock expected at least 2 messages")
	}

	// Return predefined error if set
	if m.err != nil {
		fmt.Printf("[Mock OpenAI StructOut Test] Returning predefined error: %v\n", m.err)
		return goopenai.ChatCompletionResponse{}, m.err
	}

	// Check flag to return invalid JSON for parsing error test
	if m.returnInvalidJSON {
		fmt.Printf("[Mock OpenAI StructOut Test] Returning invalid JSON response\n")
		return goopenai.ChatCompletionResponse{
			Choices: []goopenai.ChatCompletionChoice{
				{
					Message: goopenai.ChatCompletionMessage{
						Role:    goopenai.ChatMessageRoleAssistant,
						Content: `{"recipe_name": "Invalid JSON, "ingredients": ["missing quote]}`, // Invalid JSON
					},
					FinishReason: goopenai.FinishReasonStop,
				},
			},
		}, nil
	}

	// Marshal the mock recipe into JSON string (default behavior)
	recipeJSON, err := json.Marshal(m.mockRecipe)
	// Use require inside the mock as marshalling failure is critical for the mock's correctness
	require.NoError(m.t, err, "Failed to marshal mock recipe in mock client")

	// Return predefined response
	fmt.Printf("[Mock OpenAI StructOut Test] Returning mock recipe response\n")
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{
			{
				Message: goopenai.ChatCompletionMessage{
					Role:    goopenai.ChatMessageRoleAssistant,
					Content: string(recipeJSON), // Return the JSON string
				},
				FinishReason: goopenai.FinishReasonStop,
			},
		},
	}, nil
}

// --- Test Function ---

func TestStructuredOutputWorkflowIntegration(t *testing.T) {
	// Add cleanup hook to reset DI state after test run
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 1. Setup Mock Client with expected recipe
	mockRecipe := Recipe{
		RecipeName:  "Mock Vegan Chocolate Chip Cookies",
		Ingredients: []string{"Flour", "Sugar", "Vegan Butter", "Vegan Chocolate Chips", "Vanilla Extract", "Baking Soda"},
		Steps:       []string{"Preheat oven", "Mix ingredients", "Form cookies", "Bake"},
		PrepTime:    "20 minutes",
	}
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t, // Pass testing.T for assertions inside mock
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Quick vegan chocolate chip cookies",
		mockRecipe:              mockRecipe,
		err:                     nil,
		returnInvalidJSON:       false,
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	// 2. Setup Dependencies & Store
	// DI must happen *after* t.Cleanup is registered for the test instance
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies") // Use require for setup errors
	memStore := store.NewMemoryStore()

	// 3. Define Workflow using DefineNode
	workflowID := heart.NodeID("recipeGeneratorWorkflowTest")
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// 4. Prepare Input
	inputTopic := "Quick vegan chocolate chip cookies"

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start the workflow LAZILY
	resultHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))

	// Execute the workflow and wait for the result
	recipeResult, err := heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 6. Assertions
	// Check context error first
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("Test timed out waiting for workflow result")
	}
	require.NoError(t, err, "Workflow execution failed") // Use require for the main execution error check

	// Lock before accessing shared state for assertion
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()

	// Check that the mock was called once
	require.Len(t, mockClient.calls, 1, "Expected one call to OpenAI mock")

	// Check the result struct matches the mock
	assert.Equal(t, mockRecipe.RecipeName, recipeResult.RecipeName)
	assert.Equal(t, mockRecipe.PrepTime, recipeResult.PrepTime)
	assert.ElementsMatch(t, mockRecipe.Ingredients, recipeResult.Ingredients) // Use ElementsMatch for slices unordered comparison
	assert.Equal(t, mockRecipe.Steps, recipeResult.Steps)                     // Use Equal for slice ordered comparison
}

func TestStructuredOutputWorkflowIntegration_LLMError(t *testing.T) {
	// Add cleanup hook to reset DI state after test run
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 1. Setup Mock Client to return an error
	expectedError := errors.New("simulated API error")
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Error case",
		err:                     expectedError, // Return error
		returnInvalidJSON:       false,
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	// 2. Setup Dependencies & Store
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. Define Workflow
	workflowID := heart.NodeID("recipeGeneratorWorkflowErrorTest")
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// 4. Prepare Input
	inputTopic := "Error case"

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resultHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))
	_, err = heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 6. Assertions
	require.Error(t, err, "Workflow execution should have failed")
	// Check that the error originates from the LLM call (wrapped by the parsing node)
	require.ErrorContains(t, err, "simulated API error")
	// Check that the error message includes the path of the failing parsing node within the middleware structure
	require.ErrorContains(t, err, "openai_structured_output:#0/parse_structured_response")

	// Lock before accessing shared state for assertion
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()
	require.Len(t, mockClient.calls, 1, "Expected one call to OpenAI mock even on error")

}

func TestStructuredOutputWorkflowIntegration_ParsingError(t *testing.T) {
	// Add cleanup hook to reset DI state after test run
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 1. Setup Mock Client to return invalid JSON
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Bad JSON",
		returnInvalidJSON:       true, // SET FLAG TO TRUE
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	// 2. Setup Dependencies & Store
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. Define Workflow
	workflowID := heart.NodeID("recipeGeneratorWorkflowParseErrorTest")
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// 4. Prepare Input
	inputTopic := "Bad JSON"

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resultHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))
	_, err = heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 6. Assertions
	require.Error(t, err, "Workflow execution should have failed")
	// Check that the error is related to parsing/unmarshalling
	require.ErrorContains(t, err, "failed to parse LLM response") // Check for the specific error from middleware
	require.ErrorContains(t, err, "failed to unmarshal LLM content into main.Recipe")
	// Check that the error message includes the path of the failing parsing node
	require.ErrorContains(t, err, "openai_structured_output:#0/parse_structured_response")

	// Lock before accessing shared state for assertion
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()
	require.Len(t, mockClient.calls, 1, "Expected one call to OpenAI mock even on parsing error")
}
