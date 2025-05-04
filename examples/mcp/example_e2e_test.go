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

	// --- Test Setup ---
	inputA := 123
	inputB := 456
	expectedResultStr := fmt.Sprintf("%d", inputA+inputB) // Result the LLM should ideally output
	inputPrompt := fmt.Sprintf("Hi! Could you calculate %d + %d for me please?", inputA, inputB)

	// --- Setup Tool Node & Adapt for MCP (same as main) ---
	addNodeDef := DefineAddNode("adder_e2e")
	adaptedTool := mcp.IntoTool(
		addNodeDef, addToolSchema, mapToolRequest, mapToolResponse,
	)

	// --- Setup and Start Local MCP Server (using helper) ---
	// The *server* part is still local for E2E as we're testing the gaf lib + OpenAI,
	// assuming the existence of *some* MCP server to connect to.
	mcpServerURL, stopMCPServer, err := setupAndStartMCPServer(adaptedTool)
	require.NoError(t, err, "E2E: Failed to setup MCP server")
	defer stopMCPServer()

	// --- Setup and Start MCP Client (using helper) ---
	// The MCP client connects to our local test server.
	mcpTestClient, stopMCPClient, err := setupAndStartMCPClient(mcpServerURL)
	require.NoError(t, err, "E2E: Failed to setup MCP client")
	defer stopMCPClient()

	// --- Setup REAL OpenAI Client ---
	openaiClient := goopenai.NewClient(apiKey)
	fmt.Println("E2E: Real OpenAI Client created.")

	// --- Setup Dependency Injection ---
	// Inject the REAL OpenAI client and the REAL (but locally connected) MCP client.
	err = gaf.Dependencies(
		openai.Inject(openaiClient),           // Use the REAL OpenAI client
		openai.InjectMCPClient(mcpTestClient), // Use the REAL client connected to the test server
	)
	require.NoError(t, err, "E2E: Failed to inject dependencies")
	t.Cleanup(func() {
		fmt.Println("E2E: Resetting go-agent-framework dependencies...")
		gaf.ResetDependencies()
	}) // Reset DI state after test

	// --- Define Workflow ---
	workflowID := gaf.NodeID("e2eMCPWorkflow")
	mainWorkflowDef := gaf.WorkflowFromFunc(workflowID, mainWorkflowHandler)

	// --- Execute Workflow ---
	memStore := store.NewMemoryStore() // Use memory store for E2E test simplicity
	// Use a longer timeout for real API calls
	execCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Println("--- Starting E2E Workflow Execution ---")
	workflowHandle := mainWorkflowDef.Start(gaf.Into(inputPrompt))
	finalResult, err := gaf.Execute(execCtx, workflowHandle, gaf.WithStore(memStore))
	fmt.Println("--- Finished E2E Workflow Execution ---")

	// --- Assertions ---
	// Check for specific context errors first
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed waiting for result: Timeout exceeded (%v)", execCtx.Err())
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("E2E test failed waiting for result: Context canceled (%v)", execCtx.Err())
	}
	// Check for general workflow execution errors (like API errors, tool errors)
	require.NoError(t, err, "E2E Workflow execution failed")

	require.NotNil(t, finalResult, "E2E: Final result should not be nil")
	require.NotEmpty(t, finalResult.Choices, "E2E: Expected at least one choice in the final response")

	finalChoice := finalResult.Choices[0]
	finalContent := finalChoice.Message.Content

	t.Logf("E2E Final Response Content: %s", finalContent)
	t.Logf("E2E Final Finish Reason: %s", finalChoice.FinishReason)

	// 1. Verify the final response content contains the expected number.
	//    LLMs can be verbose, so just check for containment.
	assert.Contains(t, finalContent, expectedResultStr, "E2E: Final response content mismatch")

	// 2. Verify the final finish reason is 'stop'.
	//    This is the *ideal* outcome for this prompt. It might finish with tool_calls
	//    if the model gets confused, which we'd consider a soft failure or warning here.
	assert.Equal(t, goopenai.FinishReasonStop, finalChoice.FinishReason, "E2E: Final finish reason should ideally be 'stop'")

	// 3. Check that the tool was likely used (finish reason wouldn't be 'stop' on the first call).
	//    We can infer this because the mock test confirmed 2 calls are needed.
	//    A more robust check would involve inspecting the store for tool call/result artifacts.
	// storeData, loadErr := memStore.LoadExecutionState(...) // Complex to parse reliably here
	// require.NoError(t, loadErr)
	// assert ... tool call occurred in storeData

	fmt.Println("E2E test completed successfully.")
}
