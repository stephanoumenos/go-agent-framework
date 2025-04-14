// ./examples/if/example_e2e_test.go
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

// runE2ECase executes a workflow instance and performs basic assertions.
func runE2ECase(t *testing.T, workflowDef heart.WorkflowDefinition[string, string], topic string) {
	// Use a longer timeout for real API calls (sentiment + generation)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resultHandle := workflowDef.New(ctx, topic)

	finalStatement, err := resultHandle.Out(ctx) // Wait for result using the test context

	// Assertions
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed for topic '%s': Timeout exceeded (%v)", topic, err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed for topic '%s': Context canceled (%v)", topic, err)
	}
	require.NoError(t, err, "E2E Workflow execution failed for topic '%s'", topic)

	assert.NotEmpty(t, finalStatement, "Final statement should not be empty for topic '%s'", topic)

	// Log the output for review
	t.Logf("E2E Test Topic: %s", topic)
	t.Logf("E2E Final Statement: %s", finalStatement)
}

// --- Test Function ---

func TestConditionalWorkflowLLMConditionE2E(t *testing.T) {
	// Check for API Key and Skip if missing
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}

	// Setup REAL Client, Dependencies & Store - ONCE for the parent test
	realClient := goopenai.NewClient(apiKey)

	// Inject REAL client - ONCE
	// Ensure DI is clean if other E2E tests run concurrently/sequentially.
	err := heart.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")

	memStore := store.NewMemoryStore() // Memory store is fine for E2E isolation

	// Define Workflow - ONCE
	conditionalWorkflowDef := heart.DefineWorkflow(conditionalWorkflowLLMCondition, heart.WithStore(memStore))

	// --- Run Sub-Tests ---
	// E2E sub-tests can run in parallel as DI is setup once before they start.
	// Each run hits the real API independently.

	t.Run("E2E_PositiveTopic", func(t *testing.T) {
		t.Parallel() // Sub-test runs in parallel
		runE2ECase(t, conditionalWorkflowDef, "Successful renewable energy adoption and community benefits")
	})

	t.Run("E2E_NegativeTopic", func(t *testing.T) {
		t.Parallel() // Sub-test runs in parallel
		runE2ECase(t, conditionalWorkflowDef, "Challenges and complexities in global economic stability and trade wars")
	})
}
