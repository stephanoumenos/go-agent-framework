// ./examples/fanin/example_e2e_test.go
//go:build e2e

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	gaf "go-agent-framework"
	"go-agent-framework/nodes/openai" // Provides OpenAI nodes used by the workflow.
	"go-agent-framework/store"        // Provides storage options (MemoryStore used here).

	// Provides core workflow definitions and execution.
	goopenai "github.com/sashabaranov/go-openai" // OpenAI Go client.
	"github.com/stretchr/testify/assert"         // For assertions.
	"github.com/stretchr/testify/require"        // For setup checks and fatal assertions.
)

// TestThreePerspectivesWorkflowE2E performs an end-to-end test of the
// threePerspectivesWorkflowHandler.
// It uses a real OpenAI client (requires OPENAI_API_KEY) and executes the workflow
// against the actual OpenAI API.
// It verifies that the workflow completes successfully and returns non-empty
// strings for each perspective.
func TestThreePerspectivesWorkflowE2E(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}
	// t.Parallel() // Avoid parallel execution for E2E tests using global DI state

	// --- Test Setup ---
	// 1. Add cleanup hook to reset global dependency injection state after the test.
	// This is crucial for maintaining test isolation, especially if tests run sequentially
	// without restarting the process.
	t.Cleanup(func() {
		gaf.ResetDependencies()
	})

	// 2. Setup REAL OpenAI Client, Dependencies & Store
	realClient := goopenai.NewClient(apiKey)
	// Inject the real client. gaf.Dependencies is global, hence the need for ResetDependencies.
	err := gaf.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies") // Use require for setup failures.

	// Use a MemoryStore for simplicity in E2E testing, as we mainly care about execution success.
	memStore := store.NewMemoryStore()

	// 3. Define the workflow using the production handler.
	// Give it a unique name for clarity in potential tracing/logging.
	threePerspectiveWorkflowDef := gaf.WorkflowFromFunc("threePerspectivesE2E", threePerspectivesWorkflowHandler)

	// 4. Prepare Input for the workflow.
	inputQuestion := "What are the potential benefits and drawbacks of federated learning for edge devices?"

	// 5. Execute Workflow - Use a longer timeout for real API calls.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // Increased timeout for multiple API calls.
	defer cancel()

	// Start the workflow lazily.
	// resultHandle := threePerspectiveWorkflowDef.Start(gaf.Into(inputQuestion)) // Old way

	// Execute the workflow and wait for the final result.
	// Pass the context and store options to gaf.Execute.
	// perspectivesResult, err := gaf.Execute(ctx, resultHandle, gaf.WithStore(memStore)) // Old way
	perspectivesResult, err := gaf.ExecuteWorkflow(ctx, threePerspectiveWorkflowDef, inputQuestion, gaf.WithStore(memStore)) // New way

	// --- Assertions ---
	// 7. Check for execution errors, prioritizing context errors.
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed waiting for result: Timeout exceeded (%v)", ctx.Err())
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed waiting for result: Context canceled (%v)", ctx.Err())
	}
	// Check for any other error returned by the workflow execution.
	require.NoError(t, err, "E2E Workflow execution failed") // Use require for the main execution error check.

	// Assert that the result fields are non-empty. The exact content will vary.
	assert.NotEmpty(t, perspectivesResult.Optimistic, "Optimistic perspective should not be empty")
	assert.NotEmpty(t, perspectivesResult.Pessimistic, "Pessimistic perspective should not be empty")
	assert.NotEmpty(t, perspectivesResult.Realistic, "Realistic perspective should not be empty")

	// Optional: Log the output for manual review or debugging.
	t.Logf("Optimistic: %s", perspectivesResult.Optimistic)
	t.Logf("Pessimistic: %s", perspectivesResult.Pessimistic)
	t.Logf("Realistic: %s", perspectivesResult.Realistic)
}
