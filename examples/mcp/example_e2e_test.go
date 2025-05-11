// ./examples/mcp/example_e2e_test.go
//go:build e2e

package main

import (
	"context"
	"errors"
	"fmt"
	gaf "go-agent-framework"
	"os"
	"testing"
	"time"

	"go-agent-framework/mcp"
	"go-agent-framework/nodes/openai"
	"go-agent-framework/store"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test Function ---

func TestMCPWorkflowE2E(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping E2E test: OPENAI_API_KEY environment variable not set.")
	}

	inputA := 123
	inputB := 456
	expectedResultStr := fmt.Sprintf("%d", inputA+inputB)
	inputPrompt := fmt.Sprintf("Hi! Could you calculate %d + %d for me please?", inputA, inputB)

	addNodeDef := DefineAddNode("adder_e2e")
	addWorkflowDef := gaf.WorkflowFromFunc("adderWorkflow_e2e", func(ctx gaf.Context, input AddInput) gaf.ExecutionHandle[AddOutput] {
		return addNodeDef.Start(gaf.Into(input))
	})
	adaptedTool := mcp.IntoTool(
		addWorkflowDef, addToolSchema, mapToolRequest, mapToolResponse,
	)

	mcpServerURL, stopMCPServer, err := setupAndStartMCPServer(adaptedTool)
	require.NoError(t, err, "E2E: Failed to setup MCP server")
	defer stopMCPServer()

	mcpTestClient, stopMCPClient, err := setupAndStartMCPClient(mcpServerURL)
	require.NoError(t, err, "E2E: Failed to setup MCP client")
	defer stopMCPClient()

	openaiClient := goopenai.NewClient(apiKey)
	fmt.Println("E2E: Real OpenAI Client created.")

	err = gaf.Dependencies(
		openai.Inject(openaiClient),
		openai.InjectMCPClient(mcpTestClient),
	)
	require.NoError(t, err, "E2E: Failed to inject dependencies")
	t.Cleanup(func() {
		fmt.Println("E2E: Resetting go-agent-framework dependencies...")
		gaf.ResetDependencies()
	})

	workflowID := gaf.NodeID("e2eMCPWorkflow")
	mainWorkflowDef := gaf.WorkflowFromFunc(workflowID, mainWorkflowHandler)

	memStore := store.NewMemoryStore()
	execCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Println("--- Starting E2E Workflow Execution ---")
	finalResult, err := gaf.ExecuteWorkflow(execCtx, mainWorkflowDef, inputPrompt, gaf.WithStore(memStore))
	fmt.Println("--- Finished E2E Workflow Execution ---")

	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed waiting for result: Timeout exceeded (%v)", execCtx.Err())
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed waiting for result: Context canceled (%v)", execCtx.Err())
	}
	require.NoError(t, err, "E2E Workflow execution failed")

	require.NotNil(t, finalResult, "E2E: Final result should not be nil")
	require.NotEmpty(t, finalResult.Choices, "E2E: Expected at least one choice in the final response")

	finalChoice := finalResult.Choices[0]
	finalContent := finalChoice.Message.Content

	t.Logf("E2E Final Response Content: %s", finalContent)
	t.Logf("E2E Final Finish Reason: %s", finalChoice.FinishReason)

	assert.Contains(t, finalContent, expectedResultStr, "E2E: Final response content mismatch")

	assert.Equal(t, goopenai.FinishReasonStop, finalChoice.FinishReason, "E2E: Final finish reason should ideally be 'stop'")

	fmt.Println("E2E test completed successfully.")
}
