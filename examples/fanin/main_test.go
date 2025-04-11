//go:build integration

// ./examples/fanin/main_test.go
package main

import (
	"context"
	"fmt"
	"heart"
	"heart/nodes/openai" // Ensure this import path matches your project structure
	"heart/store"
	"strings"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock OpenAI Client ---
// This mock satisfies the clientiface.ClientInterface defined previously.

type mockOpenAIClient struct {
	// Store predefined responses based on a characteristic of the request
	responses map[string]goopenai.ChatCompletionResponse
	errors    map[string]error
	// Track calls for assertion
	CreateChatCompletionCalls []goopenai.ChatCompletionRequest
}

// CreateChatCompletion implements the required method from ClientInterface.
func (m *mockOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.CreateChatCompletionCalls = append(m.CreateChatCompletionCalls, req) // Track call

	// Determine which response to give based on system prompt content
	key := ""
	for _, msg := range req.Messages {
		if msg.Role == goopenai.ChatMessageRoleSystem {
			if strings.Contains(msg.Content, "technology evangelist") {
				key = "optimist"
				break
			}
			if strings.Contains(msg.Content, "doom-and-gloom analyst") {
				key = "pessimist"
				break
			}
			if strings.Contains(msg.Content, "balanced, realistic synthesizer") {
				key = "realistic"
				break
			}
		}
	}

	if err, exists := m.errors[key]; exists {
		fmt.Printf("[Mock OpenAI] Returning error for key '%s'\n", key)
		return goopenai.ChatCompletionResponse{}, err
	}
	if resp, exists := m.responses[key]; exists {
		fmt.Printf("[Mock OpenAI] Returning response for key '%s'\n", key)
		return resp, nil
	}

	fmt.Printf("[Mock OpenAI] No predefined response/error for key '%s'. Returning default empty success.\n", key)
	// Default response or error if no match
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{{
			Message:      goopenai.ChatCompletionMessage{Content: "Mocked default response"},
			FinishReason: goopenai.FinishReasonStop, // Default needs FinishReason too
		}},
	}, nil
}

// --- Test Function ---

func TestThreePerspectivesWorkflowIntegration(t *testing.T) {
	// This build tag allows running this test with `go test -tags=integration`
	// It will be skipped during a default `go test` run.

	t.Parallel() // Mark test as parallelizable

	// 1. Setup Mock Client with expected responses, including FinishReason
	mockClient := &mockOpenAIClient{
		responses: map[string]goopenai.ChatCompletionResponse{
			"optimist": {
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Mocked positive: AGI research will unlock unprecedented progress!"},
					FinishReason: goopenai.FinishReasonStop,
				}},
				// Example Usage data (optional, add if your code uses it)
				// Usage: goopenai.Usage{ PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15 },
			},
			"pessimist": {
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Mocked negative: Custom AGI is a dangerous path filled with risk."},
					FinishReason: goopenai.FinishReasonStop,
				}},
				// Usage: goopenai.Usage{ PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15 },
			},
			"realistic": {
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Mocked balanced: Investing in AGI requires careful consideration of benefits and risks."},
					FinishReason: goopenai.FinishReasonStop,
				}},
				// Usage: goopenai.Usage{ PromptTokens: 25, CompletionTokens: 15, TotalTokens: 40 },
			},
		},
		errors: nil, // No errors expected in this happy-path test
	}

	// 2. Setup Dependencies & Store
	// Assuming a mechanism to reset global DI state isn't implemented,
	// running tests in parallel might interfere if DI state is truly global.
	// For now, we proceed assuming injection works per test or is managed.
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")

	memStore := store.NewMemoryStore() // Use memory store for speed and isolation

	// 3. Define Workflow
	// Assuming `threePerspectivesWorkflow` from the original example is accessible.
	// This might require adjustments based on your actual package structure.
	// If this file is in `examples/fanin` and package is `main_test`,
	// you might need to move `threePerspectivesWorkflow` out of main or duplicate logic.
	// For this example, we assume it's callable.
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(memStore))

	// 4. Prepare Input
	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Short timeout for mocked test
	defer cancel()
	resultHandle := threePerspectiveWorkflowDef.New(ctx, inputQuestion)

	// 6. Get Result & Assert
	perspectivesResult, err := resultHandle.Out()

	// 7. Assertions
	require.NoError(t, err, "Workflow execution failed") // Should pass now

	// Check that the mock was called as expected
	require.Len(t, mockClient.CreateChatCompletionCalls, 3, "Expected three calls to OpenAI mock (optimist, pessimist, realistic)")

	// Check the content using the mock responses
	assert.NotEmpty(t, perspectivesResult.Optimistic)
	assert.Contains(t, perspectivesResult.Optimistic, "Mocked positive", "Optimistic response mismatch")

	assert.NotEmpty(t, perspectivesResult.Pessimistic)
	assert.Contains(t, perspectivesResult.Pessimistic, "Mocked negative", "Pessimistic response mismatch")

	assert.NotEmpty(t, perspectivesResult.Realistic)
	assert.Contains(t, perspectivesResult.Realistic, "Mocked balanced", "Realistic response mismatch")

	assert.Contains(t, mockClient.CreateChatCompletionCalls[0].Messages[0].Content, "technology evangelist")
	assert.Contains(t, mockClient.CreateChatCompletionCalls[1].Messages[0].Content, "doom-and-gloom")
	assert.Contains(t, mockClient.CreateChatCompletionCalls[2].Messages[1].Content, "Based on the following two perspectives")
}
