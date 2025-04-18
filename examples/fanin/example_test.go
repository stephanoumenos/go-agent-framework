// ./examples/fanin/example_test.go
package main

import (
	"context" // Keep import
	"errors"  // Import errors package
	"fmt"
	"heart" // Use ExecutionHandle, Execute etc.
	"heart/nodes/openai"
	"heart/store"
	"strings"
	"sync"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock OpenAI Client (remains the same) ---
type mockOpenAIClient struct {
	responses                 map[string]goopenai.ChatCompletionResponse
	errors                    map[string]error
	callsMtx                  sync.Mutex
	CreateChatCompletionCalls []goopenai.ChatCompletionRequest
}

// CreateChatCompletion implementation (remains the same logic)
func (m *mockOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.callsMtx.Lock()
	m.CreateChatCompletionCalls = append(m.CreateChatCompletionCalls, req)
	m.callsMtx.Unlock()

	// Determine key based on system prompt content for routing mock responses
	key := "default" // Default key if no specific system prompt matches
	for _, msg := range req.Messages {
		if msg.Role == goopenai.ChatMessageRoleSystem {
			content := msg.Content
			// Match based on keywords in system prompts used in the workflow
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

	// Check context cancellation before returning mock response/error
	if err := ctx.Err(); err != nil {
		// Simulate context cancellation error if the test context is done
		fmt.Printf("[Mock OpenAI Fanin Test] Context cancelled or deadline exceeded for key '%s'\n", key)
		return goopenai.ChatCompletionResponse{}, err
	}

	// Return predefined error if exists for the key
	if err, exists := m.errors[key]; exists && err != nil {
		fmt.Printf("[Mock OpenAI Fanin Test] Returning error for key '%s'\n", key)
		return goopenai.ChatCompletionResponse{}, err
	}
	// Return predefined response if exists for the key
	if resp, exists := m.responses[key]; exists {
		fmt.Printf("[Mock OpenAI Fanin Test] Returning response for key '%s'\n", key)
		return resp, nil
	}

	// Fallback to default response if no specific key matched or defined
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

func TestThreePerspectivesWorkflowIntegration(t *testing.T) {
	t.Parallel()

	// Add cleanup hook to reset DI state after test run
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 1. Setup Mock Client
	mockClient := &mockOpenAIClient{
		responses: map[string]goopenai.ChatCompletionResponse{
			"optimist":  {Choices: []goopenai.ChatCompletionChoice{{Message: goopenai.ChatCompletionMessage{Content: "Mocked positive"}, FinishReason: goopenai.FinishReasonStop}}},
			"pessimist": {Choices: []goopenai.ChatCompletionChoice{{Message: goopenai.ChatCompletionMessage{Content: "Mocked negative"}, FinishReason: goopenai.FinishReasonStop}}},
			"realistic": {Choices: []goopenai.ChatCompletionChoice{{Message: goopenai.ChatCompletionMessage{Content: "Mocked balanced"}, FinishReason: goopenai.FinishReasonStop}}},
		},
		errors: make(map[string]error), // Initialize errors map
	}

	// 2. Setup Dependencies & Store
	// Ensure DI state is clean if tests interfere globally. DI is global.
	err := heart.Dependencies(openai.Inject(mockClient))          // DI setup remains the same
	require.NoError(t, err, "Failed to inject mock dependencies") // Use require for setup errors
	memStore := store.NewMemoryStore()

	// 3. Define Workflow using DefineNode
	// Define the workflow node
	threePerspectiveWorkflowDef := heart.WorkflowFromFunc("threePerspectivesTest", threePerspectivesWorkflowHandler)

	// 4. Prepare Input
	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start the workflow LAZILY
	resultHandle := threePerspectiveWorkflowDef.Start(heart.Into(inputQuestion))

	// Execute the workflow and wait for the result
	// Pass workflow options (like store) to Execute
	perspectivesResult, err := heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 7. Assertions
	// Check context error first
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("Test timed out waiting for workflow result")
	}
	require.NoError(t, err, "Workflow execution failed") // Use require for the main execution error check

	// Lock before accessing shared state for assertion
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()

	// Check that the mock was called as expected
	require.Len(t, mockClient.CreateChatCompletionCalls, 3, "Expected three calls to OpenAI mock")

	// Check the content using the mock responses
	assert.Equal(t, "Mocked positive", perspectivesResult.Optimistic, "Optimistic response mismatch") // Use Equal for exact match
	assert.Equal(t, "Mocked negative", perspectivesResult.Pessimistic, "Pessimistic response mismatch")
	assert.Equal(t, "Mocked balanced", perspectivesResult.Realistic, "Realistic response mismatch")

	// Optional: Verify prompts received by the mock if needed
	// Example: Check system prompt of the first call (optimist)
	require.GreaterOrEqual(t, len(mockClient.CreateChatCompletionCalls[0].Messages), 1, "Optimist call missing messages")
	assert.Contains(t, mockClient.CreateChatCompletionCalls[0].Messages[0].Content, "technology evangelist", "Optimist system prompt mismatch")
	// Example: Check user prompt of the first call (optimist)
	require.GreaterOrEqual(t, len(mockClient.CreateChatCompletionCalls[0].Messages), 2, "Optimist call missing user message")
	assert.Equal(t, inputQuestion, mockClient.CreateChatCompletionCalls[0].Messages[1].Content, "Optimist user prompt mismatch")

	// Add similar checks for pessimist and realistic calls if desired
}
