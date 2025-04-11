//go:build integration

// ./examples/if/main_test.go
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
// (Definition remains the same)
type mockOpenAIClient struct {
	responses map[string]goopenai.ChatCompletionResponse
	errors    map[string]error
	// Track calls for assertion - Use a slice accessible by sub-tests
	CreateChatCompletionCalls []goopenai.ChatCompletionRequest
}

// CreateChatCompletion implements the required method from clientiface.ClientInterface.
func (m *mockOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	// Appending is now safe because sub-tests run sequentially
	m.CreateChatCompletionCalls = append(m.CreateChatCompletionCalls, req) // Track call

	// Determine which response to give based on system prompt content and user message
	key := ""
	var systemPrompt, userPrompt string
	if len(req.Messages) > 0 {
		for _, msg := range req.Messages {
			if msg.Role == goopenai.ChatMessageRoleSystem {
				systemPrompt = msg.Content
			} else if msg.Role == goopenai.ChatMessageRoleUser {
				userPrompt = msg.Content // Get user prompt for sentiment routing
			}
		}
	}

	if strings.Contains(systemPrompt, "Analyze the sentiment") {
		if strings.Contains(userPrompt, "PositiveTopic") {
			key = "sentiment_positive"
		} else if strings.Contains(userPrompt, "NegativeTopic") {
			key = "sentiment_negative"
		}
	} else if strings.Contains(systemPrompt, "optimistic assistant") {
		key = "true_branch"
	} else if strings.Contains(systemPrompt, "cautious assistant") {
		key = "false_branch"
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
			FinishReason: goopenai.FinishReasonStop,
		}},
	}, nil
}

// --- Test Function ---

func TestConditionalWorkflowLLMConditionIntegration(t *testing.T) {
	// Parent test remains sequential to ensure DI setup completes first.

	// 1. Setup ONE Mock Client for ALL Paths
	mockClient := &mockOpenAIClient{
		CreateChatCompletionCalls: make([]goopenai.ChatCompletionRequest, 0), // Reset calls
		responses: map[string]goopenai.ChatCompletionResponse{
			"sentiment_positive": {
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: " POSITIVE "},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
			"sentiment_negative": {
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Negative"},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
			"true_branch": {
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Mocked uplifting statement!"},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
			"false_branch": {
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Mocked cautionary statement."},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
		},
		errors: nil,
	}

	// 2. Setup Dependencies & Store - ONLY ONCE
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. Define Workflow - ONLY ONCE
	conditionalWorkflowDef := heart.DefineWorkflow(conditionalWorkflowLLMCondition, heart.WithStore(memStore))

	// --- Test Case 1: Positive Path ---
	t.Run("PositiveSentimentPath", func(t *testing.T) {
		// t.Parallel() // REMOVED - Run sub-tests sequentially

		inputTopicPositive := "PositiveTopic: A great success"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Execute Workflow
		startCallCount := len(mockClient.CreateChatCompletionCalls) // Should be 0 here
		resultHandle := conditionalWorkflowDef.New(ctx, inputTopicPositive)
		finalStatement, err := resultHandle.Out()

		// Assertions for Positive Path
		require.NoError(t, err, "Workflow execution failed on positive path")
		assert.Equal(t, "Mocked uplifting statement!", finalStatement, "Expected uplifting statement")

		// Analyze calls made *during this sub-test's execution*
		endCallCount := len(mockClient.CreateChatCompletionCalls) // Should be 2 here
		require.Equal(t, 2, endCallCount-startCallCount, "Expected two calls to OpenAI mock for positive path")
		callsMade := mockClient.CreateChatCompletionCalls[startCallCount:endCallCount]
		assert.Contains(t, callsMade[0].Messages[0].Content, "Analyze the sentiment")
		assert.Contains(t, callsMade[1].Messages[0].Content, "optimistic assistant")
	})

	// --- Test Case 2: Negative Path ---
	t.Run("NegativeSentimentPath", func(t *testing.T) {
		// t.Parallel() // REMOVED - Run sub-tests sequentially

		inputTopicNegative := "NegativeTopic: A difficult challenge"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Execute Workflow
		startCallCount := len(mockClient.CreateChatCompletionCalls) // Should be 2 here (from previous test)
		resultHandle := conditionalWorkflowDef.New(ctx, inputTopicNegative)
		finalStatement, err := resultHandle.Out()

		// Assertions for Negative Path
		require.NoError(t, err, "Workflow execution failed on negative path")
		assert.Equal(t, "Mocked cautionary statement.", finalStatement, "Expected cautionary statement")

		// Analyze calls made *during this sub-test's execution*
		endCallCount := len(mockClient.CreateChatCompletionCalls) // Should be 4 here
		require.Equal(t, 2, endCallCount-startCallCount, "Expected two calls to OpenAI mock for negative path")
		callsMade := mockClient.CreateChatCompletionCalls[startCallCount:endCallCount] // Check calls with index 2 and 3
		assert.Contains(t, callsMade[0].Messages[0].Content, "Analyze the sentiment")
		assert.Contains(t, callsMade[1].Messages[0].Content, "cautious assistant")
	})

}
