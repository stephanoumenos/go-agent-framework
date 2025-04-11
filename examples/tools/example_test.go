//go:build integration

// ./examples/tools/example_test.go
package main

import (
	"context"
	"encoding/json"
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

// --- Mock OpenAI Client --- (Keep the mock definition)
type mockToolOpenAIClient struct {
	// ... (same as before) ...
	responses []goopenai.ChatCompletionResponse
	errors    []error
	callsMtx  sync.Mutex
	callIndex int
	Calls     []goopenai.ChatCompletionRequest
}

func (m *mockToolOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.callsMtx.Lock()
	defer m.callsMtx.Unlock()

	m.Calls = append(m.Calls, req) // Track call safely
	callNum := m.callIndex
	m.callIndex++

	// Check if there's a specific error defined for this call number
	if len(m.errors) > callNum && m.errors[callNum] != nil {
		fmt.Printf("[Mock OpenAI Tool Test] Returning error for call #%d\n", callNum)
		return goopenai.ChatCompletionResponse{}, m.errors[callNum]
	}

	// Check if there's a specific response defined for this call number
	if len(m.responses) > callNum {
		fmt.Printf("[Mock OpenAI Tool Test] Returning predefined response for call #%d\n", callNum)
		return m.responses[callNum], nil
	}

	fmt.Printf("[Mock OpenAI Tool Test] No predefined response/error for call #%d. Returning default empty success.\n", callNum)
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{{
			Message:      goopenai.ChatCompletionMessage{Content: "Mocked default tool response"},
			FinishReason: goopenai.FinishReasonStop,
		}},
	}, nil
}

// --- Helpers --- (Keep the helpers createMockToolCallResponse, createMockFinalResponse)
func createMockToolCallResponse(toolCallID, funcName, funcArgs string) goopenai.ChatCompletionResponse {
	// ... (same as before) ...
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{{
			FinishReason: goopenai.FinishReasonToolCalls,
			Message: goopenai.ChatCompletionMessage{
				Role: goopenai.ChatMessageRoleAssistant,
				ToolCalls: []goopenai.ToolCall{{
					ID:   toolCallID,
					Type: goopenai.ToolTypeFunction,
					Function: goopenai.FunctionCall{
						Name:      funcName,
						Arguments: funcArgs,
					},
				}},
			},
		}},
	}
}

func createMockFinalResponse(content string) goopenai.ChatCompletionResponse {
	// ... (same as before) ...
	return goopenai.ChatCompletionResponse{
		Choices: []goopenai.ChatCompletionChoice{{
			FinishReason: goopenai.FinishReasonStop,
			Message: goopenai.ChatCompletionMessage{
				Role:    goopenai.ChatMessageRoleAssistant,
				Content: content,
			},
		}},
	}
}

// --- Test Function ---

func TestToolWorkflowIntegration(t *testing.T) {
	// t.Parallel() // REMOVED for debugging

	// 1. Setup Mock Client
	expectedArgs := WeatherParams{Location: "London"}
	argsJSON, err := json.Marshal(expectedArgs)
	require.NoError(t, err, "Failed to marshal expected tool arguments for mock")
	toolCallID := "call_123"

	// Reset mock state for this specific test run
	mockClient := &mockToolOpenAIClient{
		responses: []goopenai.ChatCompletionResponse{
			createMockToolCallResponse(toolCallID, "get_current_weather", string(argsJSON)),
			createMockFinalResponse("Mocked response: The weather in London is currently 15 degrees celsius and Cloudy."),
		},
		errors:    []error{nil, nil},
		Calls:     make([]goopenai.ChatCompletionRequest, 0), // Ensure Calls slice is reset
		callIndex: 0,                                         // Ensure call index is reset
	}

	// 2. Setup Dependencies & Store
	// NOTE: If heart.Dependencies has global state, running tests sequentially is crucial
	// or a reset mechanism is needed. Assuming sequential execution for now.
	err = heart.Dependencies(openai.Inject(mockClient)) // Inject the mock
	require.NoError(t, err, "Failed to inject mock dependencies")
	memStore := store.NewMemoryStore()

	// 3. Define Workflow
	toolWorkflowDef := heart.DefineWorkflow(toolWorkflowHandler, heart.WithStore(memStore))

	// 4. Prepare Input
	initialRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleUser, Content: "What's the weather like in London?"},
		},
	}

	// 5. Execute Workflow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resultHandle := toolWorkflowDef.New(ctx, initialRequest)

	// 6. Get Result & Assert
	finalResponse, err := resultHandle.Out()

	// 7. Assertions
	require.NoError(t, err, "Workflow execution failed")

	// Access mock calls directly (no need for mutex if not parallel)
	calls := mockClient.Calls

	// Check that the mock was called twice
	require.Len(t, calls, 2, "Expected exactly two calls to OpenAI mock")

	// --- Assertions on the calls made TO the mock ---
	// Call 1: Should contain the initial user message AND tools
	call1 := calls[0]
	call1Msgs := call1.Messages
	require.NotEmpty(t, call1Msgs, "Call 1 should have messages")
	assert.Equal(t, goopenai.ChatMessageRoleUser, call1Msgs[0].Role, "Call 1 first message role mismatch")
	assert.Contains(t, call1Msgs[0].Content, "What's the weather like in London?", "Call 1 content mismatch")
	// Check Tools field AFTER applying the fix in tools.go
	assert.NotEmpty(t, call1.Tools, "Call 1 should have tools defined by the middleware")
	// Optional deeper check if needed:
	if len(call1.Tools) > 0 {
		assert.Equal(t, "get_current_weather", call1.Tools[0].Function.Name, "Tool name mismatch in Call 1")
	}

	// Call 2: Should contain initial message, assistant tool call, and tool response message
	call2 := calls[1]
	call2Msgs := call2.Messages
	// fmt.Printf("DEBUG: Call 2 Messages Received by Mock: %+v\n", call2Msgs) // DEBUG Line
	require.Len(t, call2Msgs, 3, fmt.Sprintf("Call 2 should have 3 messages (user, assistant tool call, tool result), but got %d", len(call2Msgs)))
	assert.Equal(t, goopenai.ChatMessageRoleUser, call2Msgs[0].Role)
	assert.Equal(t, goopenai.ChatMessageRoleAssistant, call2Msgs[1].Role)
	require.NotEmpty(t, call2Msgs[1].ToolCalls, "Call 2 Assistant message should contain ToolCalls")
	assert.Equal(t, goopenai.ChatMessageRoleTool, call2Msgs[2].Role) // Check role of the third message
	assert.Equal(t, toolCallID, call2Msgs[2].ToolCallID, "Tool call ID in message mismatch")
	assert.Contains(t, call2Msgs[2].Content, `"location": "London"`)
	assert.Contains(t, call2Msgs[2].Content, `"temperature": "15"`)
	assert.Contains(t, call2Msgs[2].Content, `"description": "Cloudy"`)
	assert.Nil(t, call2.Tools, "Call 2 should NOT redefine tools")

	// --- Assertions on the final workflow output ---
	require.NotEmpty(t, finalResponse.Choices, "Final response should have choices")
	finalContent := finalResponse.Choices[0].Message.Content
	assert.Equal(t, "Mocked response: The weather in London is currently 15 degrees celsius and Cloudy.", finalContent, "Final response content mismatch")
	assert.Equal(t, goopenai.FinishReasonStop, finalResponse.Choices[0].FinishReason, "Final finish reason should be stop")
}
