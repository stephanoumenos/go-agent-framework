// ./examples/fanin/example_test.go
package main

import (
	"context"
	"fmt"
	"heart" // Use ExecutionHandle, WorkflowResultHandle etc.
	"heart/nodes/openai"
	"heart/store"
	"strings" // <<< ADD THIS IMPORT
	"sync"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock OpenAI Client ---
type mockOpenAIClient struct {
	responses                 map[string]goopenai.ChatCompletionResponse
	errors                    map[string]error
	callsMtx                  sync.Mutex
	CreateChatCompletionCalls []goopenai.ChatCompletionRequest
}

// CreateChatCompletion implementation (Corrected)
func (m *mockOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.callsMtx.Lock()
	m.CreateChatCompletionCalls = append(m.CreateChatCompletionCalls, req)
	m.callsMtx.Unlock() // Unlock earlier is fine

	key := "default" // Start with a default key, can be anything not in your maps
	for _, msg := range req.Messages {
		if msg.Role == goopenai.ChatMessageRoleSystem {
			content := msg.Content
			// --- FIX START ---
			// Check the content of the system prompt to determine the key
			if strings.Contains(content, "technology evangelist") {
				key = "optimist"
			} else if strings.Contains(content, "doom-and-gloom analyst") {
				key = "pessimist"
			} else if strings.Contains(content, "balanced, realistic synthesizer") {
				key = "realistic"
			}
			// --- FIX END ---
			break // Found the system message, no need to check further
		}
	}

	// Check for specific error for the determined key
	if err, exists := m.errors[key]; exists && err != nil {
		fmt.Printf("[Mock OpenAI Fanin Test] Returning error for key '%s'\n", key)
		return goopenai.ChatCompletionResponse{}, err
	}
	// Check for specific response for the determined key
	if resp, exists := m.responses[key]; exists {
		fmt.Printf("[Mock OpenAI Fanin Test] Returning response for key '%s'\n", key)
		return resp, nil
	}

	// If key is still "default" or not found in maps, return the default response
	fmt.Printf("[Mock OpenAI Fanin Test] No predefined response/error for key '%s'. Returning default.\n", key)
	return goopenai.ChatCompletionResponse{Choices: []goopenai.ChatCompletionChoice{{Message: goopenai.ChatCompletionMessage{Content: "Mocked default"}, FinishReason: goopenai.FinishReasonStop}}}, nil
}

// --- Test Function ---

func TestThreePerspectivesWorkflowIntegration(t *testing.T) {
	t.Parallel()

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
	// Ensure DI state is clean if tests interfere globally. Consider sequential runs or a DI reset.
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. Define Workflow
	// DefineWorkflow returns WorkflowDefinition
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(memStore))

	// 4. Prepare Input
	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// New returns WorkflowResultHandle
	resultHandle := threePerspectiveWorkflowDef.New(ctx, inputQuestion)

	// 6. Get Result & Assert
	// Use Out(ctx) on the WorkflowResultHandle
	perspectivesResult, err := resultHandle.Out(ctx) // Pass context

	// 7. Assertions
	require.NoError(t, err, "Workflow execution failed")

	// Lock before accessing shared state for assertion
	mockClient.callsMtx.Lock()
	defer mockClient.callsMtx.Unlock()

	// Check that the mock was called as expected
	require.Len(t, mockClient.CreateChatCompletionCalls, 3, "Expected three calls to OpenAI mock")

	// Check the content using the mock responses
	assert.Contains(t, perspectivesResult.Optimistic, "Mocked positive", "Optimistic response mismatch")
	assert.Contains(t, perspectivesResult.Pessimistic, "Mocked negative", "Pessimistic response mismatch")
	assert.Contains(t, perspectivesResult.Realistic, "Mocked balanced", "Realistic response mismatch")

	// ... (rest of prompt verification assertions remain the same) ...
	// You might want to add assertions here to check the prompts received by the mock,
	// similar to how you do it in TestTitleAndParagraphWorkflow_Simple_Integration
}
