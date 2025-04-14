// ./examples/fanin/example_e2e_test.go
//go:build e2e

package main

import (
	"context"
	"errors"
	"heart" // Use Execute etc.
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
	// t.Parallel() // Running E2E in parallel might cause DI issues if not reset properly

	// Add cleanup hook to reset DI state after test run
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 2. Setup REAL Client, Dependencies & Store
	realClient := goopenai.NewClient(apiKey)
	// NOTE: DI is global. Ensure clean state if running multiple E2E tests.
	// Consider a DI reset mechanism or running E2E tests sequentially.
	err := heart.Dependencies(openai.Inject(realClient))
	// Use require.NoError here for cleaner setup failure reporting
	require.NoError(t, err, "Failed to inject real dependencies")

	memStore := store.NewMemoryStore() // Store per test is fine

	// 3. Define Workflow using DefineNode
	workflowResolver := heart.NewWorkflowResolver("threePerspectivesE2E", threePerspectivesWorkflowHandler)
	threePerspectiveWorkflowDef := heart.DefineNode[string, perspectives](
		"threePerspectivesE2E",
		workflowResolver,
	)

	// 4. Prepare Input
	inputQuestion := "Should companies invest heavily in custom AGI research?"

	// 5. Execute Workflow - Use a longer timeout for real API calls
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // Longer timeout
	defer cancel()

	// Start lazily
	resultHandle := threePerspectiveWorkflowDef.Start(heart.Into(inputQuestion))

	// Execute and wait
	perspectivesResult, err := heart.Execute(ctx, resultHandle, heart.WithStore(memStore)) // Pass store to Execute

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
