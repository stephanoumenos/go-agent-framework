// ./examples/fanin/example_test.go
package main

import (
	"context"
	"errors" // Import errors package for error handling.
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"heart"              // Provides core workflow definitions and execution.
	"heart/nodes/openai" // Provides OpenAI nodes used by the workflow.
	"heart/store"        // Provides storage options (MemoryStore used here).

	goopenai "github.com/sashabaranov/go-openai" // OpenAI Go client structures.
	"github.com/stretchr/testify/assert"         // For assertions.
	"github.com/stretchr/testify/require"        // For setup checks and fatal assertions.
)

// --- Mock OpenAI Client ---

// mockOpenAIClient implements the openai.ClientInterface (specifically CreateChatCompletion)
// for testing purposes. It allows routing responses and errors based on keywords in the
// system prompt of the incoming request.
type mockOpenAIClient struct {
	// responses maps a keyword (e.g., "optimist") to a canned response.
	responses map[string]goopenai.ChatCompletionResponse
	// errors maps a keyword to a canned error.
	errors map[string]error
	// callsMtx protects concurrent access to the calls slice.
	callsMtx sync.Mutex
	// CreateChatCompletionCalls records the requests received by the mock.
	CreateChatCompletionCalls []goopenai.ChatCompletionRequest
}

// CreateChatCompletion is the mock implementation of the corresponding method
// in the OpenAI client interface. It records the call, determines a routing key
// based on the system prompt, and returns a predefined response or error based on the key.
// It also checks for context cancellation.
func (m *mockOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	// Record the request safely.
	m.callsMtx.Lock()
	m.CreateChatCompletionCalls = append(m.CreateChatCompletionCalls, req)
	m.callsMtx.Unlock()

	// Determine routing key based on system prompt content.
	key := "default" // Default key if no specific system prompt matches.
	for _, msg := range req.Messages {
		if msg.Role == goopenai.ChatMessageRoleSystem {
			content := msg.Content
			// Match based on keywords used in the workflow's system prompts.
			if strings.Contains(content, "technology evangelist") {
				key = "optimist"
				break
			} else if strings.Contains(content, "doom-and-gloom analyst") {
				key = "pessimist"
				break
			} else if strings.Contains(content, "balanced, realistic synthesizer") {
				key = "realistic"
				break
			}
		}
	}

	// --- Context Cancellation Check ---
	// Check if the test context has been cancelled or timed out before proceeding.
	// This simulates the behavior of the real client respecting context cancellation.
	if err := ctx.Err(); err != nil {
		fmt.Printf("[Mock OpenAI Fanin Test] Context cancelled or deadline exceeded for key '%s'\n", key)
		return goopenai.ChatCompletionResponse{}, err // Return context error.
	}

	// --- Response/Error Routing ---
	// Return predefined error if one exists for the determined key.
	if err, exists := m.errors[key]; exists && err != nil {
		fmt.Printf("[Mock OpenAI Fanin Test] Returning predefined error for key '%s': %v\n", key, err)
		return goopenai.ChatCompletionResponse{}, err
	}
	// Return predefined response if one exists for the determined key.
	if resp, exists := m.responses[key]; exists {
		fmt.Printf("[Mock OpenAI Fanin Test] Returning predefined response for key '%s'\n", key)
		return resp, nil
	}

	// --- Fallback Response ---
	// If no specific response or error is defined for the key, return a default response.
	fmt.Printf("[Mock OpenAI Fanin Test] No predefined response/error for key '%s'. Returning default.\n", key)
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{
			{
				Message:      goopenai.ChatCompletionMessage{Content: "Mocked default response for " + key},
				FinishReason: goopenai.FinishReasonStop,
			},
		},
	}, nil
}

// --- Test Function ---

// TestThreePerspectivesWorkflowIntegration tests the threePerspectivesWorkflowHandler
// using the mockOpenAIClient. It verifies:
//   - Correct dependency injection setup using the mock client.
//   - The workflow executes successfully.
//   - The mock client is called the expected number of times (3).
//   - The final result structure contains the expected content from the mock responses.
//   - (Optional) The prompts sent to the mock client match expectations.
func TestThreePerspectivesWorkflowIntegration(t *testing.T) {
	t.Parallel() // Mark test as safe to run in parallel.

	// --- Test Setup ---
	// 1. Add cleanup hook to reset global dependency injection state after the test.
	// This is crucial for maintaining test isolation, especially when running in parallel.
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 2. Setup Mock Client with canned responses for each perspective.
	mockClient := &mockOpenAIClient{
		responses: map[string]goopenai.ChatCompletionResponse{
			"optimist":  {Choices: []goopenai.ChatCompletionChoice{{Message: goopenai.ChatCompletionMessage{Content: "Mocked positive outlook"}, FinishReason: goopenai.FinishReasonStop}}},
			"pessimist": {Choices: []goopenai.ChatCompletionChoice{{Message: goopenai.ChatCompletionMessage{Content: "Mocked negative risks"}, FinishReason: goopenai.FinishReasonStop}}},
			"realistic": {Choices: []goopenai.ChatCompletionChoice{{Message: goopenai.ChatCompletionMessage{Content: "Mocked balanced summary"}, FinishReason: goopenai.FinishReasonStop}}},
		},
		errors: make(map[string]error), // Initialize errors map (no errors in this test case).
	}

	// 3. Setup Dependencies & Store
	// Inject the mock client using the standard openai.Inject function.
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies") // Use require for setup errors.
	// Use an in-memory store for integration tests.
	memStore := store.NewMemoryStore()

	// 4. Define the workflow using the production handler.
	// Use a unique ID for the definition within the test.
	threePerspectiveWorkflowDef := heart.WorkflowFromFunc("threePerspectivesTest", threePerspectivesWorkflowHandler)

	// 5. Prepare Input for the workflow.
	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// 6. Execute Workflow
	// Set a reasonable timeout for the test execution context.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start the workflow lazily.
	resultHandle := threePerspectiveWorkflowDef.Start(heart.Into(inputQuestion))

	// Execute the workflow and wait for the result.
	// Pass the context and store options to heart.Execute.
	perspectivesResult, err := heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// --- Assertions ---
	// 7. Check for execution errors, prioritizing context errors.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("Test timed out waiting for workflow result")
	}
	require.NoError(t, err, "Workflow execution failed") // Use require for the main execution error check.

	// Lock before accessing shared mock state for assertions.
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()

	// 8. Check that the mock client was called the expected number of times.
	require.Len(t, mockClient.CreateChatCompletionCalls, 3, "Expected three calls to OpenAI mock")

	// 9. Check the content of the final result structure against the mock responses.
	assert.Equal(t, "Mocked positive outlook", perspectivesResult.Optimistic, "Optimistic response mismatch")
	assert.Equal(t, "Mocked negative risks", perspectivesResult.Pessimistic, "Pessimistic response mismatch")
	assert.Equal(t, "Mocked balanced summary", perspectivesResult.Realistic, "Realistic response mismatch")

	// 10. Optional: Verify the prompts received by the mock client.
	// Example: Check system and user prompts of the first call (optimist).
	require.GreaterOrEqual(t, len(mockClient.CreateChatCompletionCalls[0].Messages), 2, "Optimist call missing messages")
	assert.Contains(t, mockClient.CreateChatCompletionCalls[0].Messages[0].Content, "technology evangelist", "Optimist system prompt mismatch")
	assert.Equal(t, inputQuestion, mockClient.CreateChatCompletionCalls[0].Messages[1].Content, "Optimist user prompt mismatch")

	// Example: Check system and user prompts of the second call (pessimist).
	require.GreaterOrEqual(t, len(mockClient.CreateChatCompletionCalls[1].Messages), 2, "Pessimist call missing messages")
	assert.Contains(t, mockClient.CreateChatCompletionCalls[1].Messages[0].Content, "doom-and-gloom analyst", "Pessimist system prompt mismatch")
	assert.Equal(t, inputQuestion, mockClient.CreateChatCompletionCalls[1].Messages[1].Content, "Pessimist user prompt mismatch")

	// Example: Check system and user prompts of the third call (realistic).
	require.GreaterOrEqual(t, len(mockClient.CreateChatCompletionCalls[2].Messages), 2, "Realistic call missing messages")
	assert.Contains(t, mockClient.CreateChatCompletionCalls[2].Messages[0].Content, "balanced, realistic synthesizer", "Realistic system prompt mismatch")
	// The user prompt for the third call is dynamically generated based on the first two responses.
	expectedRealisticUserPrompt := fmt.Sprintf(
		"Based on the following two perspectives, provide a balanced and realistic summary:\n\n"+
			"Optimistic View:\n%s\n\n"+
			"Pessimistic View:\n%s\n\n"+
			"Realistic Summary:",
		"Mocked positive outlook", "Mocked negative risks",
	)
	assert.Equal(t, expectedRealisticUserPrompt, mockClient.CreateChatCompletionCalls[2].Messages[1].Content, "Realistic user prompt mismatch")
}

// TODO: Add tests for error scenarios (e.g., one of the LLM calls fails).
