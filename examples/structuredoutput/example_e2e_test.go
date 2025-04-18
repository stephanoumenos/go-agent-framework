// ./examples/structuredoutput/example_e2e_test.go
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

// --- Test Function ---

func TestStructuredOutputWorkflowE2E(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")

	// Add cleanup hook to reset DI state after test run
	t.Cleanup(func() {
		heart.ResetDependencies()
	})

	// 2. Setup REAL Client, Dependencies & Store
	realClient := goopenai.NewClient(apiKey)
	// NOTE: DI is global. Ensure clean state if running multiple E2E tests.
	err := heart.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")

	memStore := store.NewMemoryStore() // Store per test is fine

	// 3. Define Workflow using DefineNode
	workflowID := heart.NodeID("recipeGeneratorWorkflowE2E")
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// 4. Prepare Input
	inputTopic := "Super quick and easy microwave mug cake with chocolate"

	// 5. Execute Workflow - Use a longer timeout for real API calls
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // Longer timeout
	defer cancel()

	// Start lazily
	resultHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))

	// Execute and wait
	recipeResult, err := heart.Execute(ctx, resultHandle, heart.WithStore(memStore))

	// 6. Assertions
	// Check for specific context errors first
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed waiting for result: Timeout exceeded (%v)", ctx.Err())
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed waiting for result: Context canceled (%v)", ctx.Err())
	}
	// Check for general workflow execution errors
	require.NoError(t, err, "E2E Workflow execution failed")

	// Assert that fields are non-empty (as exact content is variable)
	assert.NotEmpty(t, recipeResult.RecipeName, "RecipeName should not be empty")
	assert.NotEmpty(t, recipeResult.PrepTime, "PrepTime should not be empty")
	assert.NotEmpty(t, recipeResult.Ingredients, "Ingredients slice should not be empty")
	assert.NotEmpty(t, recipeResult.Steps, "Steps slice should not be empty")

	// Optional: Log the output for review
	t.Logf("E2E Recipe Name: %s", recipeResult.RecipeName)
	t.Logf("E2E Prep Time: %s", recipeResult.PrepTime)
	t.Logf("E2E Ingredients: %v", recipeResult.Ingredients)
	t.Logf("E2E Steps: %v", recipeResult.Steps)

	// Add basic sanity checks
	assert.Greater(t, len(recipeResult.Ingredients), 1, "Expected more than 1 ingredient")
	assert.Greater(t, len(recipeResult.Steps), 1, "Expected more than 1 step")
}
