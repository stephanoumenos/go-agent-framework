//go:build e2e

package main

import (
	"context"
	"errors"
	"heart"
	"heart/nodes/openai" // Adjust import path if needed
	"heart/store"
	"os"
	"strings"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test Function ---

func TestTitleAndParagraphWorkflow_Simple_E2E(t *testing.T) {
	// This tag ensures this test only runs with `go test -tags=e2e ./...`

	// 1. Check for API Key and Skip if missing
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}

	// 2. Setup REAL Client, Dependencies & Store
	realClient := goopenai.NewClient(apiKey)

	// Inject REAL client
	// Ensure DI setup is clean if running multiple E2E tests.
	// Assuming sequential execution or proper DI management for now.
	err := heart.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")

	memStore := store.NewMemoryStore() // Memory store is fine for E2E isolation

	// 3. Define Workflow
	// Use the simplified handler function
	titleParaWorkflow := heart.DefineWorkflow(titleAndParagraphWorkflow_Simple, heart.WithStore(memStore))

	// 4. Prepare Input
	inputTopic := "The impact of artificial intelligence on creative writing"

	// 5. Execute Workflow - Use a longer timeout for real API calls
	// Needs time for two sequential LLM calls.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resultHandle := titleParaWorkflow.New(ctx, inputTopic)

	// 6. Get Result & Assert
	finalResponse, err := resultHandle.Out() // Blocks until workflow completes

	// 7. Assertions
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed: Timeout exceeded (%v)", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed: Context canceled (%v)", err)
	}
	// Check for specific OpenAI errors if helpful
	var apiErr *goopenai.APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("E2E test failed with OpenAI API Error: Type=%s, Message=%s, Code=%v", apiErr.Type, apiErr.Message, apiErr.Code)
	}
	// General error check
	require.NoError(t, err, "E2E Workflow execution failed")

	// Assert basic structure and content of the final response
	require.NotEmpty(t, finalResponse.Choices, "E2E final response should have choices")
	finalChoice := finalResponse.Choices[0]
	finalMessage := finalChoice.Message

	// Assert the final message content (the paragraph) is non-empty
	assert.NotEmpty(t, finalMessage.Content, "E2E final paragraph content should not be empty")
	assert.Equal(t, goopenai.ChatMessageRoleAssistant, finalMessage.Role, "E2E final message role should be assistant")
	assert.Equal(t, goopenai.FinishReasonStop, finalChoice.FinishReason, "E2E final finish reason should be 'stop'")

	// Optional: Log the output for manual inspection
	t.Logf("E2E Input Topic: %s", inputTopic)
	t.Logf("E2E Final Paragraph: %s", finalMessage.Content)

	// Optional: Check if content seems relevant (might be brittle)
	assert.Contains(t, strings.ToLower(finalMessage.Content), "artificial intelligence", "Final paragraph seems unrelated to the topic")
	assert.Contains(t, strings.ToLower(finalMessage.Content), "writing", "Final paragraph seems unrelated to the topic")
}
