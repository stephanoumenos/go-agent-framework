//go:build e2e

package main

import (
	"context"
	"heart"
	"heart/nodes/openai"
	"heart/store"
	"os"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test Function ---

func TestThreePerspectivesWorkflowE2E(t *testing.T) {
	// This tag ensures this test only runs with `go test -tags=e2e ./...`

	// 1. Check for API Key and Skip if missing
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}

	t.Parallel() // Mark test as parallelizable

	// 2. Setup REAL Client, Dependencies & Store
	realClient := goopenai.NewClient(apiKey)

	// Inject REAL client
	err := heart.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")

	memStore := store.NewMemoryStore() // Memory store is still fine

	// 3. Define Workflow
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(memStore))

	// 4. Prepare Input
	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// 5. Execute Workflow - Use a longer timeout for real API calls
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // Longer timeout
	defer cancel()
	resultHandle := threePerspectiveWorkflowDef.New(ctx, inputQuestion)

	// 6. Get Result & Assert
	perspectivesResult, err := resultHandle.Out()

	// 7. Assertions
	// We expect success, but check for context deadline exceeded specifically
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("E2E test failed: Timeout exceeded (%v)", err)
	}
	require.NoError(t, err, "E2E Workflow execution failed")

	// Assert that fields are non-empty, but NOT specific content
	assert.NotEmpty(t, perspectivesResult.Optimistic, "Optimistic perspective should not be empty")
	assert.NotEmpty(t, perspectivesResult.Pessimistic, "Pessimistic perspective should not be empty")
	assert.NotEmpty(t, perspectivesResult.Realistic, "Realistic perspective should not be empty")

	// Optional: Log the output for manual inspection if needed
	t.Logf("Optimistic: %s", perspectivesResult.Optimistic)
	t.Logf("Pessimistic: %s", perspectivesResult.Pessimistic)
	t.Logf("Realistic: %s", perspectivesResult.Realistic)
}
