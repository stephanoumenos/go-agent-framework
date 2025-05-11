package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	gaf "go-agent-framework"
	"go-agent-framework/nodes/openai"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoryWorkflowHandler(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		gaf.ResetDependencies()
		var callCount int32 // Tracks the number of API calls to the mock server.

		// Expected outputs for the two LLM calls
		expectedStoryIdea := "A curious squirrel invents a time machine powered by acorns."
		expectedExpandedStory := "Deep in a sun-dappled forest, Squeaky, a squirrel with an unusually bright glint in his eye, tinkered relentlessly. Driven by an insatiable curiosity and an abundance of acorns, he finally completed his masterpiece: a miniature time machine, its core humming with the concentrated energy of a thousand roasted nuts, ready to whisk him through the ages."

		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/chat/completions", r.URL.Path, "Mock server called with unexpected API endpoint")
			require.Equal(t, http.MethodPost, r.Method, "Expected POST request to mock server")

			bodyBytes, err := io.ReadAll(r.Body)
			require.NoError(t, err, "Failed to read request body in mock server")
			defer r.Body.Close()

			var req goopenai.ChatCompletionRequest
			err = json.Unmarshal(bodyBytes, &req)
			require.NoError(t, err, "Failed to unmarshal request body in mock server")

			currentCall := atomic.AddInt32(&callCount, 1)
			var resp goopenai.ChatCompletionResponse

			if currentCall == 1 {
				// First call: Generate story idea
				expectedPromptContent := "Tell me a short, one-sentence creative story idea."
				require.Len(t, req.Messages, 1, "Expected 1 message in the first request")
				assert.Equal(t, goopenai.ChatMessageRoleUser, req.Messages[0].Role, "Expected user role for the first request's message")
				assert.Equal(t, expectedPromptContent, req.Messages[0].Content, "Unexpected content for the first request's prompt")
				assert.Equal(t, 50, req.MaxTokens, "Unexpected MaxTokens for the first request")

				resp = goopenai.ChatCompletionResponse{
					Choices: []goopenai.ChatCompletionChoice{
						{Message: goopenai.ChatCompletionMessage{Content: expectedStoryIdea}},
					},
				}
			} else if currentCall == 2 {
				// Second call: Expand story idea
				expectedPromptContent := fmt.Sprintf("Expand this one-sentence story idea into a short paragraph: \"%s\"", expectedStoryIdea)
				require.Len(t, req.Messages, 1, "Expected 1 message in the second request")
				assert.Equal(t, goopenai.ChatMessageRoleUser, req.Messages[0].Role, "Expected user role for the second request's message")
				assert.Equal(t, expectedPromptContent, req.Messages[0].Content, "Unexpected content for the second request's prompt")
				assert.Equal(t, 150, req.MaxTokens, "Unexpected MaxTokens for the second request")

				resp = goopenai.ChatCompletionResponse{
					Choices: []goopenai.ChatCompletionChoice{
						{Message: goopenai.ChatCompletionMessage{Content: expectedExpandedStory}},
					},
				}
			} else {
				t.Fatalf("Mock server received an unexpected number of API calls: %d", currentCall)
			}

			w.Header().Set("Content-Type", "application/json")
			err = json.NewEncoder(w).Encode(resp)
			require.NoError(t, err, "Failed to encode response in mock server")
		}))
		defer mockServer.Close()

		config := goopenai.DefaultConfig("test-api-key")
		config.BaseURL = mockServer.URL
		mockClient := goopenai.NewClientWithConfig(config)

		err := gaf.Dependencies(openai.Inject(mockClient))
		require.NoError(t, err, "Error setting up GAF dependencies with mock client")

		workflowInput := "Tell me a short, one-sentence creative story idea."
		ctx := context.Background()

		finalResponse, err := gaf.ExecuteWorkflow(ctx, storyChainWorkflow, workflowInput)

		require.NoError(t, err, "ExecuteWorkflow returned an unexpected error")
		require.NotNil(t, finalResponse, "Final response from ExecuteWorkflow should not be nil")
		require.Len(t, finalResponse.Choices, 1, "Expected 1 choice in the final response")
		assert.Equal(t, expectedExpandedStory, finalResponse.Choices[0].Message.Content, "The final expanded story does not match the expected output")

		assert.EqualValues(t, 2, atomic.LoadInt32(&callCount), "Expected exactly two API calls to be made")
	})

	t.Run("FirstCallNoContent", func(t *testing.T) {
		gaf.ResetDependencies()
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := goopenai.ChatCompletionResponse{
				Choices: []goopenai.ChatCompletionChoice{},
			}
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(resp)
			require.NoError(t, err, "Failed to encode empty response in mock server")
		}))
		defer mockServer.Close()

		config := goopenai.DefaultConfig("test-api-key")
		config.BaseURL = mockServer.URL
		mockClient := goopenai.NewClientWithConfig(config)

		err := gaf.Dependencies(openai.Inject(mockClient))
		require.NoError(t, err, "Error setting up GAF dependencies with mock client for error test")

		workflowInput := "Tell me a short, one-sentence creative story idea."
		ctx := context.Background()

		_, err = gaf.ExecuteWorkflow(ctx, storyChainWorkflow, workflowInput)

		require.Error(t, err, "Expected an error when the first LLM call returns no content")
		assert.Contains(t, err.Error(), "first LLM call returned no content", "The error message does not match the expected output for no content")
	})

	t.Run("FirstCallEmptyMessageContent", func(t *testing.T) {
		gaf.ResetDependencies()
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := goopenai.ChatCompletionResponse{
				Choices: []goopenai.ChatCompletionChoice{
					{Message: goopenai.ChatCompletionMessage{Content: ""}},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(resp)
			require.NoError(t, err, "Failed to encode response with empty message in mock server")
		}))
		defer mockServer.Close()

		config := goopenai.DefaultConfig("test-api-key")
		config.BaseURL = mockServer.URL
		mockClient := goopenai.NewClientWithConfig(config)

		err := gaf.Dependencies(openai.Inject(mockClient))
		require.NoError(t, err, "Error setting up GAF dependencies with mock client for empty content test")

		workflowInput := "Tell me a short, one-sentence creative story idea."
		ctx := context.Background()

		_, err = gaf.ExecuteWorkflow(ctx, storyChainWorkflow, workflowInput)

		require.Error(t, err, "Expected an error when the first LLM call returns empty message content")
		assert.Contains(t, err.Error(), "first LLM call returned no content", "The error message does not match the expected output for empty message content")
	})
}
