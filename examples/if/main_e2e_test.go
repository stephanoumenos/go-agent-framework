//go:build e2e

package main

import (
	"context"
	"errors"
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

// Helper function without client creation or DI setup
func runE2ECase(t *testing.T, workflowDef heart.WorkflowDefinition[string, string], topic string) {
	// 3. Define Workflow (already done outside)
	// 4. Prepare Input (using function argument 'topic')

	// 5. Execute Workflow - Use a longer timeout for real API calls
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // Longer timeout
	defer cancel()
	resultHandle := workflowDef.New(ctx, topic)

	// 6. Get Result & Assert
	finalStatement, err := resultHandle.Out()

	// 7. Assertions
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("E2E test failed for topic '%s': Timeout exceeded (%v)", topic, err)
	}
	// Check for context canceled specifically
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed for topic '%s': Context canceled (%v)", topic, err)
	}
	// Check for other errors
	require.NoError(t, err, "E2E Workflow execution failed for topic '%s'", topic)

	// Assert that the final statement is non-empty
	assert.NotEmpty(t, finalStatement, "Final statement should not be empty for topic '%s'", topic)

	// Optional: Log the output
	t.Logf("E2E Test Topic: %s", topic)
	t.Logf("E2E Final Statement: %s", finalStatement)
}

// --- Test Function ---

func TestConditionalWorkflowLLMConditionE2E(t *testing.T) {
	// This tag ensures this test only runs with `go test -tags=e2e ./...`

	// 1. Check for API Key and Skip if missing
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}

	// 2. Setup REAL Client, Dependencies & Store - ONCE
	realClient := goopenai.NewClient(apiKey)

	// Inject REAL client - ONCE
	// TODO: Ideally add a DI reset mechanism call here if implemented in the library,
	// in case other E2E tests modified the global state before this one.
	err := heart.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")

	memStore := store.NewMemoryStore() // Memory store is fine for E2E

	// Define Workflow - ONCE
	conditionalWorkflowDef := heart.DefineWorkflow(conditionalWorkflowLLMCondition, heart.WithStore(memStore))

	// --- Run Sub-Tests ---
	// Sub-tests can run in parallel as they don't modify shared DI state anymore.

	t.Run("E2E_PositiveTopic", func(t *testing.T) {
		t.Parallel() // Sub-test runs in parallel
		runE2ECase(t, conditionalWorkflowDef, "Successful renewable energy adoption")
	})

	t.Run("E2E_NegativeTopic", func(t *testing.T) {
		t.Parallel() // Sub-test runs in parallel
		runE2ECase(t, conditionalWorkflowDef, "Challenges in global economic stability")
	})
}
