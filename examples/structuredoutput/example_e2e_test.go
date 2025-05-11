// ./examples/structuredoutput/example_e2e_test.go
//go:build e2e

package main

import (
	"context"
	"errors"
	gaf "go-agent-framework"
	"os"
	"testing"
	"time"

	// Provides core workflow definitions and execution.
	"go-agent-framework/nodes/openai" // Provides OpenAI nodes and dependency injection.
	"go-agent-framework/store"        // Provides storage options.

	goopenai "github.com/sashabaranov/go-openai" // OpenAI Go client.
	"github.com/stretchr/testify/assert"         // For assertions.
	"github.com/stretchr/testify/require"        // For setup checks and fatal assertions.
)

// TestStructuredOutputWorkflowE2E performs an end-to-end test of the
// structuredOutputWorkflowHandler using a real OpenAI client.
// It requires the OPENAI_API_KEY environment variable to be set.
// It verifies that the workflow completes successfully against the actual API
// and returns a Recipe struct with non-empty fields, demonstrating that the
// LLM successfully generated JSON conforming to the schema.
func TestStructuredOutputWorkflowE2E(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}

	t.Cleanup(func() {
		gaf.ResetDependencies()
	})

	realClient := goopenai.NewClient(apiKey)
	err := gaf.Dependencies(openai.Inject(realClient))
	require.NoError(t, err, "Failed to inject real dependencies")

	memStore := store.NewMemoryStore()

	workflowID := gaf.NodeID("recipeGeneratorWorkflowE2E")
	recipeWorkflowDef := gaf.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	inputTopic := "Super quick and easy microwave chocolate mug cake, maybe vegan?"

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	recipeResult, err := gaf.ExecuteWorkflow(ctx, recipeWorkflowDef, inputTopic, gaf.WithStore(memStore))

	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed waiting for result: Timeout exceeded (%v)", ctx.Err())
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed waiting for result: Context canceled (%v)", ctx.Err())
	}
	require.NoError(t, err, "E2E Workflow execution failed unexpectedly")

	assert.NotEmpty(t, recipeResult.RecipeName, "RecipeName should not be empty")
	assert.NotEmpty(t, recipeResult.PrepTime, "PrepTime should not be empty")
	assert.NotEmpty(t, recipeResult.Ingredients, "Ingredients slice should not be empty")
	assert.NotEmpty(t, recipeResult.Steps, "Steps slice should not be empty")

	t.Logf("E2E Recipe Name: %s", recipeResult.RecipeName)
	t.Logf("E2E Prep Time: %s", recipeResult.PrepTime)
	t.Logf("E2E Ingredients: %v", recipeResult.Ingredients)
	t.Logf("E2E Steps: %v", recipeResult.Steps)
	t.Logf("E2E Description: %s", recipeResult.Description)
	t.Logf("E2E Servings: %d", recipeResult.Servings)
	t.Logf("E2E Difficulty: %s", recipeResult.Difficulty)

	assert.Greater(t, len(recipeResult.Ingredients), 1, "Expected more than 1 ingredient")
	assert.Greater(t, len(recipeResult.Steps), 1, "Expected more than 1 step")
}
