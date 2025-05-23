// ./examples/mcp/example_test.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stephanoumenos/go-agent-framework/mcp"

	"github.com/stephanoumenos/go-agent-framework/nodes/openai"

	gaf "github.com/stephanoumenos/go-agent-framework"

	"github.com/stephanoumenos/go-agent-framework/store"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpschema "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	goopenai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Helper: Setup Local MCP Server ---
func setupAndStartMCPServer(tool mcp.MCPTool) (string, func(), error) {
	mcpServer := mcpserver.NewMCPServer(
		"test-mcp-server",
		"1.0.0",
		mcpserver.WithLogging(),
	)
	mcp.AddTools(mcpServer, tool)

	sseServer := mcpserver.NewSSEServer(mcpServer)
	testServer := httptest.NewServer(sseServer)
	sseURL := testServer.URL + sseServer.CompleteSsePath()
	fmt.Printf("[Test Helper] MCP Server running at: %s\n", testServer.URL)
	fmt.Printf("[Test Helper] MCP SSE Endpoint: %s\n", sseURL)

	stopFunc := func() {
		fmt.Println("[Test Helper] Stopping MCP Test Server...")
		testServer.Close()
		fmt.Println("[Test Helper] MCP Test Server stopped.")
	}
	return sseURL, stopFunc, nil
}

// --- Helper: Setup Local MCP Client ---
func setupAndStartMCPClient(serverSSEURL string) (mcpclient.MCPClient, func(), error) {
	client, err := mcpclient.NewSSEMCPClient(serverSSEURL)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating test MCP SSE client: %w", err)
	}

	clientCtx, clientCancel := context.WithCancel(context.Background())

	clientErrChan := make(chan error, 1)
	go func() {
		fmt.Println("[Test Helper] Starting SSE Client background processing...")
		startErr := client.Start(clientCtx)
		if startErr != nil && !errors.Is(startErr, context.Canceled) {
			log.Printf("[Test Helper] SSE Client error during run: %v", startErr)
			clientErrChan <- startErr
		} else {
			fmt.Println("[Test Helper] SSE Client background processing stopped.")
		}
		close(clientErrChan)
	}()

	time.Sleep(100 * time.Millisecond)

	initReq := mcpschema.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpschema.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpschema.Implementation{Name: "gaf-mcp-test-client", Version: "1.0.0"}
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()

	_, initErr := client.Initialize(initCtx, initReq)
	if initErr != nil {
		clientCancel()
		return nil, nil, fmt.Errorf("failed to initialize test MCP client: %w", initErr)
	}
	fmt.Println("[Test Helper] Test MCP SSE Client created and initialized.")

	stopFunc := func() {
		fmt.Println("[Test Helper] Stopping Test MCP Client...")
		clientCancel()
		if err := <-clientErrChan; err != nil {
			log.Printf("[Test Helper] Error received from client Stop: %v", err)
		}
		fmt.Println("[Test Helper] Test MCP Client stopped.")
	}

	return client, stopFunc, nil
}

// --- Mock OpenAI Client for MCP Flow ---
type mockMCPEnabledOpenAIClient struct {
	t *testing.T

	callCount int
	mu        sync.Mutex

	ExpectedPrompt1 string
	ToolCallRequest goopenai.ToolCall
	ToolCallID      string

	ExpectedMessages2 []goopenai.ChatCompletionMessage
	FinalResponse     string
}

func (m *mockMCPEnabledOpenAIClient) CreateChatCompletion(ctx context.Context, req goopenai.ChatCompletionRequest) (goopenai.ChatCompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++

	callNum := m.callCount

	fmt.Printf("[Mock OpenAI Test] Received call #%d\n", callNum)
	for i, msg := range req.Messages {
		fmt.Printf("  Msg %d: Role=%s, Content=%q, ToolCalls=%+v, ToolCallID=%s\n", i, msg.Role, msg.Content, msg.ToolCalls, msg.ToolCallID)
	}

	if callNum == 1 {
		fmt.Println("[Mock OpenAI Test] Responding with tool call request.")
		require.Len(m.t, req.Messages, 2, "Call 1: Expected 2 messages (system, user)")
		assert.Equal(m.t, goopenai.ChatMessageRoleSystem, req.Messages[0].Role, "Call 1: Message 0 Role")
		assert.Equal(m.t, goopenai.ChatMessageRoleUser, req.Messages[1].Role, "Call 1: Message 1 Role")
		assert.Contains(m.t, req.Messages[1].Content, m.ExpectedPrompt1, "Call 1: User message content mismatch")

		require.NotNil(m.t, req.Tools, "Call 1: Tools definition missing (should be added by WithMCP middleware)")
		require.Len(m.t, req.Tools, 1, "Call 1: Expected 1 tool definition")
		assert.Equal(m.t, "add_numbers", req.Tools[0].Function.Name, "Call 1: Tool name mismatch")

		return goopenai.ChatCompletionResponse{
			ID:      "mock-resp-1",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []goopenai.ChatCompletionChoice{
				{
					Index: 0,
					Message: goopenai.ChatCompletionMessage{
						Role:      goopenai.ChatMessageRoleAssistant,
						ToolCalls: []goopenai.ToolCall{m.ToolCallRequest},
					},
					FinishReason: goopenai.FinishReasonToolCalls,
				},
			},
		}, nil
	} else if callNum == 2 {
		fmt.Println("[Mock OpenAI Test] Responding with final answer.")
		require.Len(m.t, req.Messages, len(m.ExpectedMessages2), "Call 2: Message count mismatch")

		assert.Equal(m.t, m.ExpectedMessages2[0].Role, req.Messages[0].Role, "Call 2: Message 0 Role (System)")
		assert.Equal(m.t, m.ExpectedMessages2[0].Content, req.Messages[0].Content, "Call 2: Message 0 Content (System)")

		assert.Equal(m.t, m.ExpectedMessages2[1].Role, req.Messages[1].Role, "Call 2: Message 1 Role (User)")
		assert.Equal(m.t, m.ExpectedMessages2[1].Content, req.Messages[1].Content, "Call 2: Message 1 Content (User)")

		assert.Equal(m.t, m.ExpectedMessages2[2].Role, req.Messages[2].Role, "Call 2: Message 2 Role (Assistant)")
		assert.Empty(m.t, req.Messages[2].Content, "Call 2: Message 2 Content should be empty for assistant tool call message")
		require.Len(m.t, req.Messages[2].ToolCalls, 1, "Call 2: Message 2 ToolCalls count")
		assert.Equal(m.t, m.ExpectedMessages2[2].ToolCalls[0].ID, req.Messages[2].ToolCalls[0].ID, "Call 2: Message 2 ToolCall ID")
		assert.Equal(m.t, m.ExpectedMessages2[2].ToolCalls[0].Type, req.Messages[2].ToolCalls[0].Type, "Call 2: Message 2 ToolCall Type")
		assert.Equal(m.t, m.ExpectedMessages2[2].ToolCalls[0].Function.Name, req.Messages[2].ToolCalls[0].Function.Name, "Call 2: Message 2 ToolCall Function Name")
		assert.JSONEq(m.t, m.ExpectedMessages2[2].ToolCalls[0].Function.Arguments, req.Messages[2].ToolCalls[0].Function.Arguments, "Call 2: Message 2 ToolCall Function Arguments")

		assert.Equal(m.t, m.ExpectedMessages2[3].Role, req.Messages[3].Role, "Call 2: Message 3 Role (Tool)")
		assert.Equal(m.t, m.ExpectedMessages2[3].Content, req.Messages[3].Content, "Call 2: Message 3 Content (Tool Result)")
		assert.Equal(m.t, m.ExpectedMessages2[3].ToolCallID, req.Messages[3].ToolCallID, "Call 2: Message 3 ToolCallID")

		return goopenai.ChatCompletionResponse{
			ID:      "mock-resp-2",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []goopenai.ChatCompletionChoice{
				{
					Index: 0,
					Message: goopenai.ChatCompletionMessage{
						Role:    goopenai.ChatMessageRoleAssistant,
						Content: m.FinalResponse,
					},
					FinishReason: goopenai.FinishReasonStop,
				},
			},
		}, nil
	}

	m.t.Errorf("Mock OpenAI client called unexpectedly (%d times)", callNum)
	return goopenai.ChatCompletionResponse{}, fmt.Errorf("unexpected call #%d to mock OpenAI client", callNum)
}

// --- Test Function ---

func TestMCPWorkflowIntegration(t *testing.T) {
	toolCallID := "call_abc123_test"
	inputA := 55
	inputB := 77
	expectedResult := inputA + inputB
	inputPrompt := fmt.Sprintf("What is %d + %d?", inputA, inputB)
	expectedToolArgsJSON := fmt.Sprintf(`{"A":%d,"B":%d}`, inputA, inputB)
	expectedToolResultContent := strconv.Itoa(expectedResult)

	mockClient := &mockMCPEnabledOpenAIClient{
		t:               t,
		ExpectedPrompt1: inputPrompt,
		ToolCallID:      toolCallID,
		ToolCallRequest: goopenai.ToolCall{
			ID:   toolCallID,
			Type: goopenai.ToolTypeFunction,
			Function: goopenai.FunctionCall{
				Name:      "add_numbers",
				Arguments: expectedToolArgsJSON,
			},
		},
		ExpectedMessages2: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a helpful assistant. Use tools when necessary to perform calculations."},
			{Role: goopenai.ChatMessageRoleUser, Content: inputPrompt},
			{
				Role: goopenai.ChatMessageRoleAssistant,
				ToolCalls: []goopenai.ToolCall{
					{ID: toolCallID, Type: goopenai.ToolTypeFunction, Function: goopenai.FunctionCall{Name: "add_numbers", Arguments: expectedToolArgsJSON}},
				},
			},
			{
				Role:       goopenai.ChatMessageRoleTool,
				Content:    expectedToolResultContent,
				ToolCallID: toolCallID,
			},
		},
		FinalResponse: fmt.Sprintf("The sum of %d and %d is %d.", inputA, inputB, expectedResult),
	}

	addNodeDef := DefineAddNode("adder_test_integration")
	addWorkflowDef := gaf.WorkflowFromFunc("adderWorkflow_test_integration", func(ctx gaf.Context, input AddInput) gaf.ExecutionHandle[AddOutput] {
		return addNodeDef.Start(gaf.Into(input))
	})
	adaptedTool := mcp.IntoTool(
		addWorkflowDef, addToolSchema, mapToolRequest, mapToolResponse,
	)

	mcpServerURL, stopMCPServer, err := setupAndStartMCPServer(adaptedTool)
	require.NoError(t, err, "Failed to setup MCP server")
	defer stopMCPServer()

	mcpTestClient, stopMCPClient, err := setupAndStartMCPClient(mcpServerURL)
	require.NoError(t, err, "Failed to setup MCP client")
	defer stopMCPClient()

	err = gaf.Dependencies(
		openai.Inject(mockClient),
		openai.InjectMCPClient(mcpTestClient),
	)
	require.NoError(t, err, "Failed to inject dependencies")
	t.Cleanup(func() {
		fmt.Println("[Test Cleanup] Resetting go-agent-framework dependencies...")
		gaf.ResetDependencies()
		fmt.Println("[Test Cleanup] Dependencies reset.")
	})

	workflowID := gaf.NodeID("testMCPWorkflow_Integration_" + strconv.Itoa(time.Now().Nanosecond()))
	mainWorkflowDef := gaf.WorkflowFromFunc(workflowID, mainWorkflowHandler)

	memStore := store.NewMemoryStore()
	execCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fmt.Println("--- Starting Workflow Execution in Test ---")
	finalResult, err := gaf.ExecuteWorkflow(execCtx, mainWorkflowDef, inputPrompt, gaf.WithStore(memStore))
	fmt.Println("--- Finished Workflow Execution in Test ---")

	require.NoError(t, err, "Workflow execution failed unexpectedly")
	require.NotNil(t, finalResult, "Final result should not be nil")
	require.Len(t, finalResult.Choices, 1, "Expected one choice in the final response")

	finalChoice := finalResult.Choices[0]

	mockClient.mu.Lock()
	assert.Equal(t, 2, mockClient.callCount, "Expected mock OpenAI client to be called twice")
	mockClient.mu.Unlock()

	assert.Equal(t, mockClient.FinalResponse, finalChoice.Message.Content, "Final response content mismatch")
	assert.Equal(t, goopenai.FinishReasonStop, finalChoice.FinishReason, "Final finish reason should be 'stop'")

	fmt.Println("Integration test completed successfully.")
}

// TestE2EMCPWorkflow tests the full MCP flow with a real OpenAI API call (if OPENAI_API_KEY is set).
// It uses a live local MCP server and client.
func TestE2EMCPWorkflow(t *testing.T) {
	// t.Parallel()

	// --- Skip if no API Key ---
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping E2E test.")
	}
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode.")
	}

	// --- Test Setup ---
	// Basic inputs, similar to the integration test but will go to the real API
	inputA := 7
	inputB := 8
	// expectedSum := inputA + inputB // 15 // Unused, LLM will provide the sum
	inputPrompt := fmt.Sprintf("Please calculate %d plus %d using your tool and tell me the result. Then tell me a joke about the sum.", inputA, inputB)

	// --- Setup Tool Node (Adder) ---
	addNodeDef := DefineAddNode("e2e_adder") // Use a unique ID for the E2E test
	// Wrap addNodeDef in a workflow for the MCP adapter
	addWorkflowDefE2E := gaf.WorkflowFromFunc("adderWorkflow_e2e", func(ctx gaf.Context, input AddInput) gaf.ExecutionHandle[AddOutput] {
		// Use the addNodeDef captured from the outer scope.
		return addNodeDef.Start(gaf.Into(input))
	})

	// --- Adapt Tool for MCP ---
	adaptedTool := mcp.IntoTool(
		addWorkflowDefE2E, // Use addWorkflowDefE2E
		addToolSchema,     // Defined in example.go
		mapToolRequest,    // Defined in example.go
		mapToolResponse,   // Defined in example.go
	)

	// --- Setup Local MCP Server ---
	serverSSEURL, stopServerFunc, serverErr := setupAndStartMCPServer(adaptedTool)
	require.NoError(t, serverErr, "E2E: Failed to start MCP server")
	defer stopServerFunc()

	// --- Setup Local MCP Client ---
	mcpTestClient, stopClientFunc, clientErr := setupAndStartMCPClient(serverSSEURL)
	require.NoError(t, clientErr, "E2E: Failed to start MCP client")
	defer stopClientFunc()

	// --- Setup Real OpenAI Client ---
	openaiClient := goopenai.NewClient(apiKey)

	// --- Setup Dependency Injection ---
	// Inject REAL OpenAI client and REAL MCP client (connected to local test server)
	injectErr := gaf.Dependencies( // Use a different variable name to avoid shadowing
		openai.Inject(openaiClient),           // Use the REAL OpenAI client
		openai.InjectMCPClient(mcpTestClient), // Use the REAL client connected to the test server
	)
	require.NoError(t, injectErr, "E2E: Failed to inject dependencies")
	t.Cleanup(func() {
		fmt.Println("E2E: Resetting go-agent-framework dependencies...")
		gaf.ResetDependencies()
	}) // Reset DI state after test

	// --- Define Workflow ---
	workflowID := gaf.NodeID("e2eMCPWorkflow")
	mainWorkflowDef := gaf.WorkflowFromFunc(workflowID, mainWorkflowHandler) // mainWorkflowHandler from example.go

	// --- Execute Workflow ---
	memStore := store.NewMemoryStore() // Use memory store for E2E test simplicity
	// Use a longer timeout for real API calls
	execCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Println("--- Starting E2E Workflow Execution ---")
	finalResult, execWorkflowErr := gaf.ExecuteWorkflow(execCtx, mainWorkflowDef, inputPrompt, gaf.WithStore(memStore)) // New way, new error variable
	fmt.Println("--- Finished E2E Workflow Execution ---")

	// --- Assertions ---
	// Check for specific context errors first
	if errors.Is(execWorkflowErr, context.DeadlineExceeded) {
		t.Fatalf("E2E test failed waiting for result: Timeout exceeded (%v)", execCtx.Err())
	}
	if errors.Is(execWorkflowErr, context.Canceled) {
		t.Fatalf("E2E test failed waiting for result: Context canceled (%v)", execCtx.Err())
	}
	// Check for general workflow execution errors (like API errors, tool errors)
	require.NoError(t, execWorkflowErr, "E2E Workflow execution failed")

	require.NotNil(t, finalResult, "E2E: Final result should not be nil")
	require.NotEmpty(t, finalResult.Choices, "E2E: Expected at least one choice in the final response")

	finalChoice := finalResult.Choices[0]
	finalContent := finalChoice.Message.Content

	t.Logf("E2E Final Response Content: %s", finalContent)
	t.Logf("E2E Final Finish Reason: %s", finalChoice.FinishReason)

	// 1. Verify the final response content contains the expected sum (15).
	//    The exact wording can vary, so just check for the number.
	assert.Contains(t, finalContent, strconv.Itoa(inputA+inputB), "E2E: Final response content should contain the sum")

	// 2. Verify the final finish reason is 'stop'.
	assert.Equal(t, goopenai.FinishReasonStop, finalChoice.FinishReason, "E2E: Final finish reason should be 'stop'")

	// TODO: Add tests for error cases (e.g., tool execution fails, MCP client error, OpenAI error in one of the calls)
}
