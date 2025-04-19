// ./examples/structuredoutput/example_test.go
package main

import (
	"context"
	"encoding/json" // Used to marshal mock recipe and simulate invalid JSON.
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai" // Provides OpenAI node and DI injection.
	"heart/store"        // Provides storage options (MemoryStore used here).
	"sync"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai" // OpenAI Go client library.
	"github.com/stretchr/testify/assert"         // For non-fatal assertions.
	"github.com/stretchr/testify/require"        // For fatal assertions.
)

// --- Mock OpenAI Client for Structured Output ---

// mockStructuredOutputOpenAIClient implements the openai.ClientInterface specifically
// for testing the WithStructuredOutput middleware. It allows configuring the response
// to simulate successful JSON generation, errors from the API, or invalid JSON output.
type mockStructuredOutputOpenAIClient struct {
	t *testing.T // Reference to the current test for assertions within the mock.

	// Configuration for validating the request received by the mock.
	expectedSystemPrompt    string // Substring expected in the system prompt.
	expectedUserPromptTopic string // Substring expected in the user prompt.

	// Configuration for the mock's response.
	mockRecipe        Recipe // The recipe struct to marshal and return as JSON content.
	err               error  // If non-nil, this error is returned immediately.
	returnInvalidJSON bool   // If true, returns a malformed JSON string.

	// Internal state for tracking calls.
	callsMtx sync.Mutex                       // Protects access to the calls slice.
	calls    []goopenai.ChatCompletionRequest // Records requests received by the mock.
}

// CreateChatCompletion implements the clientiface.ClientInterface for the mock.
// It records the call, validates the request against expectations (especially the
// ResponseFormat added by the middleware), and returns a configured response or error.
func (m *mockStructuredOutputOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	// Record the call safely.
	m.callsMtx.Lock()
	m.calls = append(m.calls, req)
	m.callsMtx.Unlock()

	// Simulate checking for context cancellation, like a real client would.
	if err := ctx.Err(); err != nil {
		fmt.Printf("[Mock OpenAI StructOut Test] Context cancelled or deadline exceeded\n")
		return goopenai.ChatCompletionResponse{}, err
	}

	// --- Validate Request Structure ---
	// The WithStructuredOutput middleware MUST modify the request to include
	// the JSON Schema response format. Assert these modifications are present.
	// Use require inside the mock, attached to the test 't', as these are critical invariants.
	require.NotNil(m.t, req.ResponseFormat, "Mock received request with nil ResponseFormat")
	require.Equal(m.t, goopenai.ChatCompletionResponseFormatTypeJSONSchema, req.ResponseFormat.Type, "Mock received wrong ResponseFormat Type")
	require.NotNil(m.t, req.ResponseFormat.JSONSchema, "Mock received request with nil JSONSchema field in ResponseFormat")
	// The middleware hardcodes the schema name to "output".
	assert.Equal(m.t, "output", req.ResponseFormat.JSONSchema.Name, "Mock received wrong JSONSchema Name")
	// Check that a schema definition was actually included.
	require.NotNil(m.t, req.ResponseFormat.JSONSchema.Schema, "Mock received request with nil Schema definition")
	// Optional: Could add deeper validation of the generated schema structure if needed.
	// schemaMap, _ := req.ResponseFormat.JSONSchema.Schema.(map[string]interface{})
	// require.NotNil(t, schemaMap["properties"])

	// --- Validate Prompts (Optional but Recommended) ---
	// Check if the system and user prompts match expectations.
	if len(req.Messages) >= 2 {
		// Check if the system prompt contains instructions about using the JSON schema.
		assert.Contains(m.t, req.Messages[0].Content, m.expectedSystemPrompt, "Mock received unexpected system prompt content")
		// Check if the user prompt contains the expected topic.
		assert.Contains(m.t, req.Messages[1].Content, m.expectedUserPromptTopic, "Mock received unexpected user prompt content")
	} else {
		// Fail the test if the message structure is unexpected.
		assert.Fail(m.t, "Mock expected at least 2 messages (system, user) in the request")
	}

	// --- Return Configured Response or Error ---
	// 1. Return predefined error if configured.
	if m.err != nil {
		fmt.Printf("[Mock OpenAI StructOut Test] Returning predefined error: %v\n", m.err)
		return goopenai.ChatCompletionResponse{}, m.err
	}

	// 2. Return invalid JSON content if configured (for testing parsing errors).
	if m.returnInvalidJSON {
		fmt.Printf("[Mock OpenAI StructOut Test] Returning invalid JSON response\n")
		return goopenai.ChatCompletionResponse{
			Choices: []goopenai.ChatCompletionChoice{
				{
					Message: goopenai.ChatCompletionMessage{
						Role: goopenai.ChatMessageRoleAssistant,
						// Malformed JSON string: missing quote after recipe_name value.
						Content: `{"recipe_name": "Invalid JSON, "ingredients": ["missing quote]}`,
					},
					FinishReason: goopenai.FinishReasonStop,
				},
			},
		}, nil
	}

	// 3. Return valid JSON based on the mockRecipe (default success case).
	// Marshal the configured mock recipe into a JSON string.
	recipeJSON, marshalErr := json.Marshal(m.mockRecipe)
	// Marshalling the mock recipe should not fail in a test setup.
	require.NoError(m.t, marshalErr, "Failed to marshal mock recipe in mock client")

	fmt.Printf("[Mock OpenAI StructOut Test] Returning valid JSON response based on mockRecipe\n")
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{
			{
				Message: goopenai.ChatCompletionMessage{
					Role:    goopenai.ChatMessageRoleAssistant,
					Content: string(recipeJSON), // The valid JSON string content.
				},
				FinishReason: goopenai.FinishReasonStop,
			},
		},
	}, nil
}

// --- Test Function: Success Case ---

// TestStructuredOutputWorkflowIntegration tests the successful execution path of the
// structured output workflow using the mock client.
// It verifies that the workflow:
// - Correctly injects the mock client.
// - Calls the mock client once with the expected request modifications (ResponseFormat).
// - Successfully parses the valid JSON response from the mock into the Recipe struct.
// - Returns the expected Recipe data.
func TestStructuredOutputWorkflowIntegration(t *testing.T) {
	// Add cleanup hook to reset global dependency injection state after the test.
	// Crucial for test isolation.
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 1. --- Setup Mock Client ---
	// Define the expected recipe data the mock will return.
	mockRecipe := Recipe{
		RecipeName:  "Mock Vegan Chocolate Chip Cookies",
		Ingredients: []string{"Flour", "Sugar", "Vegan Butter", "Vegan Chocolate Chips", "Vanilla Extract", "Baking Soda"},
		Steps:       []string{"Preheat oven", "Mix ingredients", "Form cookies", "Bake"},
		PrepTime:    "20 minutes",
		Difficulty:  "Easy",
	}
	// Configure the mock client for the success case.
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,                                    // Pass *testing.T for assertions inside the mock.
		expectedSystemPrompt:    "You MUST output a JSON object",      // Expected substring in system prompt.
		expectedUserPromptTopic: "Quick vegan chocolate chip cookies", // Expected substring in user prompt.
		mockRecipe:              mockRecipe,                           // Recipe to return as JSON.
		err:                     nil,                                  // No error simulation.
		returnInvalidJSON:       false,                                // Return valid JSON.
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	// 2. --- Setup Dependencies & Store ---
	// Inject the configured mock client. This must happen *after* t.Cleanup is registered.
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies") // Use require for setup failures.
	memStore := store.NewMemoryStore()                            // Use an in-memory store for the test.

	// 3. --- Define Workflow ---
	// Define the workflow using the handler from the main example code.
	workflowID := heart.NodeID("recipeGeneratorWorkflowTest") // Unique ID for the test workflow.
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// 4. --- Prepare Input ---
	inputTopic := "Quick vegan chocolate chip cookies" // Input for the workflow handler.

	// 5. --- Execute Workflow ---
	// Set a reasonable timeout for the test execution.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start the workflow lazily.
	resultHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))
	// Execute the workflow and wait for the result.
	recipeResult, err := heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 6. --- Assertions ---
	// Check for context errors first (e.g., timeout).
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("Test timed out waiting for workflow result")
	}
	// Check for workflow execution errors. None expected in the success case.
	require.NoError(t, err, "Workflow execution failed unexpectedly")

	// Lock before accessing shared mock state for assertion.
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()

	// Verify the mock client was called exactly once.
	require.Len(t, mockClient.calls, 1, "Expected exactly one call to the OpenAI mock client")

	// Verify the returned Recipe struct matches the data configured in the mock.
	assert.Equal(t, mockRecipe.RecipeName, recipeResult.RecipeName, "RecipeName mismatch")
	assert.Equal(t, mockRecipe.PrepTime, recipeResult.PrepTime, "PrepTime mismatch")
	// Use ElementsMatch for slices where order doesn't matter.
	assert.ElementsMatch(t, mockRecipe.Ingredients, recipeResult.Ingredients, "Ingredients mismatch")
	// Use Equal for slices where order matters (e.g., steps).
	assert.Equal(t, mockRecipe.Steps, recipeResult.Steps, "Steps mismatch")
	assert.Equal(t, mockRecipe.Difficulty, recipeResult.Difficulty, "Difficulty mismatch")
}

// --- Test Function: LLM API Error Case ---

// TestStructuredOutputWorkflowIntegration_LLMError tests the scenario where the
// underlying OpenAI API call (simulated by the mock) returns an error.
// It verifies that the workflow correctly propagates this error.
func TestStructuredOutputWorkflowIntegration_LLMError(t *testing.T) {
	// Setup cleanup hook for DI state.
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 1. --- Setup Mock Client to Return Error ---
	expectedError := errors.New("simulated API error from mock") // The error the mock will return.
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Error case topic", // Topic for this specific test case.
		err:                     expectedError,      // Configure the mock to return this error.
		returnInvalidJSON:       false,
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	// 2. --- Setup Dependencies & Store ---
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. --- Define Workflow ---
	workflowID := heart.NodeID("recipeGeneratorWorkflowErrorTest")
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// 4. --- Prepare Input ---
	inputTopic := "Error case topic"

	// 5. --- Execute Workflow ---
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resultHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))
	// Expect an error from Execute.
	_, err = heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 6. --- Assertions ---
	// Verify that an error was indeed returned.
	require.Error(t, err, "Workflow execution should have failed due to simulated API error")
	// Check that the returned error contains the specific error message from the mock.
	// The error might be wrapped by the framework, so use ErrorContains or Is/As.
	require.ErrorContains(t, err, "simulated API error")
	// Check that the error message includes the path of the node where the error occurred.
	// The error originates in the underlying LLM call but is caught and reported by the
	// 'parse_structured_response' node within the middleware.
	require.ErrorContains(t, err, "openai_structured_output:#0/parse_structured_response")

	// Lock before accessing shared mock state.
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()
	// Verify the mock was still called once, even though it returned an error.
	require.Len(t, mockClient.calls, 1, "Expected one call to OpenAI mock even when returning an error")
}

// --- Test Function: JSON Parsing Error Case ---

// TestStructuredOutputWorkflowIntegration_ParsingError tests the scenario where the
// LLM (simulated by the mock) returns a response, but the content is not valid JSON.
// It verifies that the middleware detects the parsing error and returns an appropriate
// error message.
func TestStructuredOutputWorkflowIntegration_ParsingError(t *testing.T) {
	// Setup cleanup hook for DI state.
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 1. --- Setup Mock Client to Return Invalid JSON ---
	mockClient := &mockStructuredOutputOpenAIClient{
		t:                       t,
		expectedSystemPrompt:    "You MUST output a JSON object",
		expectedUserPromptTopic: "Bad JSON topic",
		err:                     nil,
		returnInvalidJSON:       true, // Configure mock to return malformed JSON.
		calls:                   make([]goopenai.ChatCompletionRequest, 0),
	}

	// 2. --- Setup Dependencies & Store ---
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. --- Define Workflow ---
	workflowID := heart.NodeID("recipeGeneratorWorkflowParseErrorTest")
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// 4. --- Prepare Input ---
	inputTopic := "Bad JSON topic"

	// 5. --- Execute Workflow ---
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resultHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))
	// Expect an error from Execute due to parsing failure.
	_, err = heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 6. --- Assertions ---
	// Verify that an error was returned.
	require.Error(t, err, "Workflow execution should have failed due to JSON parsing error")
	// Check for the specific parsing error messages defined in the middleware.
	require.ErrorContains(t, err, "failed to parse LLM response")
	require.ErrorContains(t, err, "failed to unmarshal LLM content into main.Recipe")
	// Verify the error message includes the path of the parsing node.
	require.ErrorContains(t, err, "openai_structured_output:#0/parse_structured_response")
	// Optionally check that the raw content is included in the error message for debugging.
	require.ErrorContains(t, err, `Raw Content: {"recipe_name": "Invalid JSON, "ingredients": ["missing quote]}`)

	// Lock before accessing shared mock state.
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()
	// Verify the mock was still called once.
	require.Len(t, mockClient.calls, 1, "Expected one call to OpenAI mock even on parsing error")
}
