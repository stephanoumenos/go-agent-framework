// ./examples/if/example_test.go
//go:build integration

package main

import (
	"context"
	"fmt"
	"heart"
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

// --- Mock OpenAI Client ---
type mockIfOpenAIClient struct {
	responses                 map[string]goopenai.ChatCompletionResponse
	errors                    map[string]error
	callsMtx                  sync.Mutex // Protects Calls slice
	CreateChatCompletionCalls []goopenai.ChatCompletionRequest
}

// CreateChatCompletion implements the required method.
func (m *mockIfOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.callsMtx.Lock()
	m.CreateChatCompletionCalls = append(m.CreateChatCompletionCalls, req) // Track call
	m.callsMtx.Unlock()

	key := "unknown"
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

	// Determine key based on prompts
	if strings.Contains(systemPrompt, "Analyze sentiment") {
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

	// Return mock response/error based on key
	if err, exists := m.errors[key]; exists && err != nil {
		fmt.Printf("[Mock OpenAI If Test] Returning error for key '%s'\n", key)
		return goopenai.ChatCompletionResponse{}, err
	}
	if resp, exists := m.responses[key]; exists {
		fmt.Printf("[Mock OpenAI If Test] Returning response for key '%s'\n", key)
		return resp, nil
	}

	fmt.Printf("[Mock OpenAI If Test] No predefined response/error for key '%s'. Returning default empty success.\n", key)
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{{
			Message:      goopenai.ChatCompletionMessage{Content: "Mocked default response"},
			FinishReason: goopenai.FinishReasonStop,
		}},
	}, nil
}

// --- Test Function ---

func TestConditionalWorkflowLLMConditionIntegration(t *testing.T) {
	// Parent test runs sequentially to set up shared mock and DI correctly.

	// 1. Setup ONE Mock Client for ALL Paths
	mockClient := &mockIfOpenAIClient{
		CreateChatCompletionCalls: make([]goopenai.ChatCompletionRequest, 0), // Reset calls
		responses: map[string]goopenai.ChatCompletionResponse{
			"sentiment_positive": { // Response for positive sentiment check
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: " POSITIVE "}, // Note leading/trailing spaces
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
			"sentiment_negative": { // Response for negative sentiment check
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Negative"},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
			"true_branch": { // Response for the optimistic branch LLM call
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Mocked uplifting statement!"},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
			"false_branch": { // Response for the cautionary branch LLM call
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: "Mocked cautionary statement."},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
		},
		errors: make(map[string]error), // No errors expected
	}

	// 2. Setup Dependencies & Store - ONLY ONCE for the parent test
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. Define Workflow - ONLY ONCE
	conditionalWorkflowDef := heart.DefineWorkflow(conditionalWorkflowLLMCondition, heart.WithStore(memStore))

	// --- Test Case 1: Positive Path ---
	t.Run("PositiveSentimentPath", func(t *testing.T) {
		// Sub-tests run sequentially due to shared mockClient call tracking.
		// Do NOT add t.Parallel() here unless mock client is made safe for concurrent call tracking.

		inputTopicPositive := "PositiveTopic: A great success"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Execute Workflow
		mockClient.callsMtx.Lock()
		startCallCount := len(mockClient.CreateChatCompletionCalls) // Record call count before execution
		mockClient.callsMtx.Unlock()

		resultHandle := conditionalWorkflowDef.New(ctx, inputTopicPositive)
		finalStatement, err := resultHandle.Out(ctx)

		// Assertions for Positive Path
		require.NoError(t, err, "Workflow execution failed on positive path")
		assert.Equal(t, "Mocked uplifting statement!", finalStatement, "Expected uplifting statement")

		// Analyze calls made *during this sub-test's execution*
		mockClient.callsMtx.Lock()
		endCallCount := len(mockClient.CreateChatCompletionCalls)
		callsMade := mockClient.CreateChatCompletionCalls[startCallCount:endCallCount] // Get calls specific to this run
		mockClient.callsMtx.Unlock()

		require.Equal(t, 2, endCallCount-startCallCount, "Expected two calls to OpenAI mock for positive path")
		// Call 1: Sentiment check
		assert.Contains(t, callsMade[0].Messages[0].Content, "Analyze sentiment", "Call 1 (Positive Path) system prompt mismatch")
		assert.Contains(t, callsMade[0].Messages[1].Content, "PositiveTopic", "Call 1 (Positive Path) user prompt mismatch")
		// Call 2: Optimistic branch
		assert.Contains(t, callsMade[1].Messages[0].Content, "optimistic assistant", "Call 2 (Positive Path) system prompt mismatch")
	})

	// --- Test Case 2: Negative Path ---
	t.Run("NegativeSentimentPath", func(t *testing.T) {
		// Sub-tests run sequentially.

		inputTopicNegative := "NegativeTopic: A difficult challenge"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Execute Workflow
		mockClient.callsMtx.Lock()
		startCallCount := len(mockClient.CreateChatCompletionCalls) // Record call count before execution
		mockClient.callsMtx.Unlock()

		resultHandle := conditionalWorkflowDef.New(ctx, inputTopicNegative)
		finalStatement, err := resultHandle.Out(ctx)

		// Assertions for Negative Path
		require.NoError(t, err, "Workflow execution failed on negative path")
		assert.Equal(t, "Mocked cautionary statement.", finalStatement, "Expected cautionary statement")

		// Analyze calls made *during this sub-test's execution*
		mockClient.callsMtx.Lock()
		endCallCount := len(mockClient.CreateChatCompletionCalls)
		callsMade := mockClient.CreateChatCompletionCalls[startCallCount:endCallCount] // Get calls specific to this run
		mockClient.callsMtx.Unlock()

		require.Equal(t, 2, endCallCount-startCallCount, "Expected two calls to OpenAI mock for negative path")
		// Call 1: Sentiment check
		assert.Contains(t, callsMade[0].Messages[0].Content, "Analyze sentiment", "Call 1 (Negative Path) system prompt mismatch")
		assert.Contains(t, callsMade[0].Messages[1].Content, "NegativeTopic", "Call 1 (Negative Path) user prompt mismatch")
		// Call 2: Cautionary branch
		assert.Contains(t, callsMade[1].Messages[0].Content, "cautious assistant", "Call 2 (Negative Path) system prompt mismatch")
	})
}
