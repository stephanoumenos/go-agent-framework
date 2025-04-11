//go:build integration

package main

import (
	"context"
	"fmt"
	"heart"
	"heart/nodes/openai"             // Adjust import path if needed
	"heart/nodes/openai/clientiface" // Import the interface package
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
type mockSimpleChainOpenAIClient struct {
	// Store predefined responses based on a characteristic of the request (e.g., system prompt)
	responses map[string]goopenai.ChatCompletionResponse
	errors    map[string]error
	// Track calls for assertion
	callsMtx sync.Mutex // Protects Calls slice
	Calls    []goopenai.ChatCompletionRequest
}

// Ensure mock implements the interface
var _ clientiface.ClientInterface = (*mockSimpleChainOpenAIClient)(nil)

// CreateChatCompletion implements the required method from ClientInterface.
func (m *mockSimpleChainOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.callsMtx.Lock()
	m.Calls = append(m.Calls, req) // Track call safely
	m.callsMtx.Unlock()

	// Determine which response to give based on system prompt content
	key := ""
	var systemPrompt string
	for _, msg := range req.Messages {
		if msg.Role == goopenai.ChatMessageRoleSystem {
			systemPrompt = msg.Content
			break // Assume only one system message for identification
		}
	}

	if strings.Contains(systemPrompt, "Generate a concise and engaging title") {
		key = "generate-title"
	} else if strings.Contains(systemPrompt, "Write a short, engaging introductory paragraph") {
		key = "generate-paragraph"
	} else {
		key = "unknown" // Fallback key
	}

	// Assuming maps are read-only after setup for simplicity in test
	if err, exists := m.errors[key]; exists && err != nil {
		fmt.Printf("[Mock OpenAI] Returning error for key '%s'\n", key)
		return goopenai.ChatCompletionResponse{}, err
	}
	if resp, exists := m.responses[key]; exists {
		fmt.Printf("[Mock OpenAI] Returning response for key '%s'\n", key)
		return resp, nil
	}

	fmt.Printf("[Mock OpenAI] No predefined response/error for key '%s'. Returning default empty success.\n", key)
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{{
			Message:      goopenai.ChatCompletionMessage{Content: "Mocked default response for " + key},
			FinishReason: goopenai.FinishReasonStop,
		}},
	}, nil
}

// --- Test Function ---

func TestTitleAndParagraphWorkflow_Simple_Integration(t *testing.T) {
	t.Parallel() // Mark test as parallelizable

	// 1. Setup Mock Client with expected responses
	mockTitle := "AI: The New Frontier in Storytelling"
	mockParagraph := "Artificial intelligence is rapidly changing the landscape of creative writing, offering new tools and possibilities for authors."

	mockClient := &mockSimpleChainOpenAIClient{
		Calls: make([]goopenai.ChatCompletionRequest, 0), // Initialize/reset calls
		responses: map[string]goopenai.ChatCompletionResponse{
			"generate-title": { // Response for the first call
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: mockTitle},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
			"generate-paragraph": { // Response for the second call
				Choices: []goopenai.ChatCompletionChoice{{
					Message:      goopenai.ChatCompletionMessage{Content: mockParagraph},
					FinishReason: goopenai.FinishReasonStop,
				}},
			},
		},
		errors: map[string]error{ // No errors expected in happy path
			"generate-title":     nil,
			"generate-paragraph": nil,
		},
	}

	// 2. Setup Dependencies & Store
	// NOTE: If heart.Dependencies has global state, parallel tests might interfere.
	// Assuming Dependencies can be called per test or manages state appropriately.
	// For maximum safety, run tests sequentially or ensure DI scoping/reset.
	// For this example, we proceed assuming parallel is safe for DI setup.
	err := heart.Dependencies(openai.Inject(mockClient))
	require.NoError(t, err, "Failed to inject mock dependencies")

	memStore := store.NewMemoryStore() // Use memory store for speed and isolation

	// 3. Define Workflow
	// Use the simplified handler function directly
	titleParaWorkflow := heart.DefineWorkflow(titleAndParagraphWorkflow_Simple, heart.WithStore(memStore))

	// 4. Prepare Input
	inputTopic := "The impact of artificial intelligence on creative writing"

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Short timeout for mocked test
	defer cancel()
	resultHandle := titleParaWorkflow.New(ctx, inputTopic)

	// 6. Get Result & Assert
	finalResponse, err := resultHandle.Out() // Blocks until workflow completes

	// 7. Assertions
	require.NoError(t, err, "Workflow execution failed") // Check for errors during execution

	// --- Assert Mock Calls ---
	mockClient.callsMtx.Lock() // Lock before accessing shared Calls slice
	calls := make([]goopenai.ChatCompletionRequest, len(mockClient.Calls))
	copy(calls, mockClient.Calls) // Make a copy to analyze
	mockClient.callsMtx.Unlock()  // Unlock after copying

	require.Len(t, calls, 2, "Expected exactly two calls to OpenAI mock")

	// Call 1: Title Generation
	call1 := calls[0]
	assert.Contains(t, call1.Messages[0].Content, "Generate a concise and engaging title", "Call 1 System Prompt mismatch")
	assert.Contains(t, call1.Messages[1].Content, inputTopic, "Call 1 User Prompt mismatch")

	// Call 2: Paragraph Generation
	call2 := calls[1]
	assert.Contains(t, call2.Messages[0].Content, "Write a short, engaging introductory paragraph", "Call 2 System Prompt mismatch")
	// IMPORTANT: Verify the input to the second call uses the *mocked title* from the first call's response
	assert.Contains(t, call2.Messages[1].Content, fmt.Sprintf("Title: %s", mockTitle), "Call 2 User Prompt should contain the title from Mock Call 1")

	// --- Assert Final Output ---
	require.NotEmpty(t, finalResponse.Choices, "Final response should have choices")
	finalContent, extractErr := extractContent(finalResponse)
	require.NoError(t, extractErr, "Failed to extract content from final mock response")
	assert.Equal(t, mockParagraph, finalContent, "Final output content mismatch")
}
