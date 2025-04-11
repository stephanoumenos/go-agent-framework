//go:build e2e

// ./examples/tools/example_e2e_test.go
package main

import (
	"context"
	"errors"
	"heart"
	"heart/nodes/openai"                         // Core openai node definition
	openaiiface "heart/nodes/openai/clientiface" // Interface package (though not directly used here)
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

func TestToolWorkflowE2E(t *testing.T) {
	// This tag ensures this test only runs with `go test -tags=e2e ./...`

	_ = openaiiface.ClientInterface(nil) // Prevent unused import error

	// 1. Check for API Key and Skip if missing
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}

	// 2. Setup REAL Client, Dependencies & Store
	realClient := goopenai.NewClient(apiKey)

	// Inject REAL client
	// Note: Ensure heart.Dependencies resets or handles multiple injections if necessary
	// across different E2E tests running potentially in parallel (though test functions themselves are atomic).
	err := heart.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")

	// Use a unique directory for file store per test run to avoid conflicts if run concurrently
	// Or stick to MemoryStore which is simpler for testing isolation.
	// testDir := fmt.Sprintf("workflows_e2e_tools_%d", time.Now().UnixNano())
	// fileStore, err := store.NewFileStore(testDir)
	// require.NoError(t, err, "Failed to create unique file store for E2E test")
	// defer os.RemoveAll(testDir) // Clean up the test directory
	memStore := store.NewMemoryStore()

	// 3. Define Workflow
	toolWorkflowDef := heart.DefineWorkflow(toolWorkflowHandler, heart.WithStore(memStore))

	// 4. Prepare Input
	initialRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Use a model known to support tool calling well
		Messages: []goopenai.ChatCompletionMessage{
			// Ask about a location handled by the mock tool logic
			{Role: goopenai.ChatMessageRoleUser, Content: "What is the current weather in London, UK?"},
		},
		// Keep MaxTokens reasonable for the final answer
		MaxTokens: 256,
	}

	// 5. Execute Workflow - Use a longer timeout for real API calls
	// Tool calling involves at least two API calls, so give ample time.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resultHandle := toolWorkflowDef.New(ctx, initialRequest)

	// 6. Get Result & Assert
	finalResponse, err := resultHandle.Out()

	// 7. Assertions
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed: Timeout exceeded (%v)", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed: Context canceled (%v)", err)
	}
	// Check for specific OpenAI errors if needed (e.g., API key issues, quota)
	var apiErr *goopenai.APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("E2E test failed with OpenAI API Error: Type=%s, Message=%s, Code=%v", apiErr.Type, apiErr.Message, apiErr.Code)
	}
	// General error check
	require.NoError(t, err, "E2E Workflow execution failed")

	// Assert basic structure of the response
	require.NotEmpty(t, finalResponse.Choices, "E2E final response should have choices")
	finalChoice := finalResponse.Choices[0]
	finalMessage := finalChoice.Message

	// Assert the final message content is non-empty
	assert.NotEmpty(t, finalMessage.Content, "E2E final response content should not be empty")
	assert.Equal(t, goopenai.ChatMessageRoleAssistant, finalMessage.Role, "E2E final message role should be assistant")

	// Assert the finish reason is 'stop' (not 'tool_calls')
	assert.Equal(t, goopenai.FinishReasonStop, finalChoice.FinishReason, "E2E final finish reason should be 'stop'")

	// Optional: Log the output for manual inspection
	t.Logf("E2E Final Response Content: %s", finalMessage.Content)
	t.Logf("E2E Finish Reason: %s", finalChoice.FinishReason)

	// More specific assertion (might be brittle): Check if the response mentions the mocked data
	assert.Contains(t, strings.ToLower(finalMessage.Content), "london", "Final response should mention the location")
	// These depend on the tool's *internal* mock returning consistent data
	assert.Contains(t, finalMessage.Content, "15", "Final response should mention the mocked temperature")
	assert.Contains(t, strings.ToLower(finalMessage.Content), "cloudy", "Final response should mention the mocked description")
}
