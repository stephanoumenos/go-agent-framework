//go:build e2e

package main

import (
	"context"
	"os"
	"testing"

	gaf "go-agent-framework"
	oainode "go-agent-framework/nodes/openai"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoryWorkflowHandler_E2E performs an end-to-end test of the storyChainWorkflow,
// making actual calls to the OpenAI API.
// It requires the OPENAI_API_KEY environment variable to be set.
func TestStoryWorkflowHandler_E2E(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping E2E test")
	}

	// Reset dependencies to ensure a clean state for this E2E test run.
	// This is important if unit tests in the same package might have configured
	// mock dependencies.
	gaf.ResetDependencies()

	// 1. Setup OpenAI Client
	client := openai.NewClient(apiKey)

	// Inject the real OpenAI client dependency.
	err := gaf.Dependencies(oainode.Inject(client))
	require.NoError(t, err, "Error setting up GAF dependencies with real OpenAI client")

	// Define the input for this specific workflow run.
	workflowInput := "Tell me a very short, one-sentence creative story idea about a robot learning to paint."

	ctx := context.Background()

	// Execute triggers the lazy computation graph and waits for the final result.
	finalResponse, err := gaf.ExecuteWorkflow(ctx, storyChainWorkflow, workflowInput)

	// Assertions for the successful E2E workflow execution
	require.NoError(t, err, "ExecuteWorkflow returned an unexpected error during E2E test")
	require.NotNil(t, finalResponse, "Final response from ExecuteWorkflow should not be nil in E2E test")

	// Check that we got some response from the second LLM call
	require.Len(t, finalResponse.Choices, 1, "Expected 1 choice in the final E2E response")
	assert.NotEmpty(t, finalResponse.Choices[0].Message.Content, "The final expanded story should not be empty in E2E test")

	t.Logf("E2E Test Completed. Final story idea: %s", finalResponse.Choices[0].Message.Content)
}
