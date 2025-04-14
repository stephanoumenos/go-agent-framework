//go:build e2e

package main

import (
	"context"
	"errors" // Import errors for checking
	"heart"  // Use WorkflowResultHandle etc.
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
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}
	t.Parallel()

	// 2. Setup REAL Client, Dependencies & Store
	realClient := goopenai.NewClient(apiKey)
	err := heart.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")
	memStore := store.NewMemoryStore()

	// 3. Define Workflow
	// DefineWorkflow returns WorkflowDefinition
	threePerspectiveWorkflowDef := heart.DefineWorkflow(threePerspectivesWorkflow, heart.WithStore(memStore))

	// 4. Prepare Input
	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// 5. Execute Workflow - Use a longer timeout for real API calls
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // Longer timeout
	defer cancel()
	// New returns WorkflowResultHandle
	resultHandle := threePerspectiveWorkflowDef.New(ctx, inputQuestion)

	// 6. Get Result & Assert
	// Use Out(ctx) on the WorkflowResultHandle
	perspectivesResult, err := resultHandle.Out(ctx) // Pass context

	// 7. Assertions
	// Check for specific context errors first
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed waiting for result: Timeout exceeded (%v)", ctx.Err())
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed waiting for result: Context canceled (%v)", ctx.Err())
	}
	// Check for general workflow execution errors
	require.NoError(t, err, "E2E Workflow execution failed")

	// Assert that fields are non-empty
	assert.NotEmpty(t, perspectivesResult.Optimistic, "Optimistic perspective should not be empty")
	assert.NotEmpty(t, perspectivesResult.Pessimistic, "Pessimistic perspective should not be empty")
	assert.NotEmpty(t, perspectivesResult.Realistic, "Realistic perspective should not be empty")

	// Optional: Log the output
	t.Logf("Optimistic: %s", perspectivesResult.Optimistic)
	t.Logf("Pessimistic: %s", perspectivesResult.Pessimistic)
	t.Logf("Realistic: %s", perspectivesResult.Realistic)
}
