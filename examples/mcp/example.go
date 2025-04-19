package main

import (
	"context"
	"errors"
	"fmt"
	"heart"
	"heart/mcp"
	"heart/nodes/openai"
	openaimw "heart/nodes/openai/middleware"
	"heart/store"
	"log" // Added for server logging if needed

	"net/http/httptest"
	"os"
	"strconv"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpschema "github.com/mark3labs/mcp-go/mcp" // Alias to avoid collision
	mcpserver "github.com/mark3labs/mcp-go/server"
	goopenai "github.com/sashabaranov/go-openai"
)

// --- Structs, Interfaces, Node Definitions (AddInput, AddOutput, addNodeResolver, etc.) remain the same ---
// --- ... (Keep the code from Section 1 & 2: DefineAddNode, addToolSchema, mapToolRequest, mapToolResponse) ... ---

// Input/Output structs for our simple adder tool
type AddInput struct {
	A int `json:"A"`
	B int `json:"B"`
}

type AddOutput struct {
	Result int `json:"Result"`
}

// Resolver for the adder node
type addNodeResolver struct{}

const addNodeNodeTypeID heart.NodeTypeID = "example:addNumbers"

type addNodeInitializer struct{}

func (i *addNodeInitializer) ID() heart.NodeTypeID { return addNodeNodeTypeID }
func (r *addNodeResolver) Init() heart.NodeInitializer {
	return &addNodeInitializer{} // Simple initializer
}

func (r *addNodeResolver) Get(ctx context.Context, in AddInput) (AddOutput, error) {
	fmt.Printf("[Tool Execution] Adding %d + %d\n", in.A, in.B)
	return AddOutput{Result: in.A + in.B}, nil
}

// Define the heart node blueprint
func DefineAddNode(nodeID heart.NodeID) heart.NodeDefinition[AddInput, AddOutput] {
	return heart.DefineNode(nodeID, &addNodeResolver{})
}

// --- 2. Define MCP Tool Schema and Mappers ---

// Define the schema for the MCP tool
var addToolSchema = mcpschema.Tool{
	Name:        "add_numbers",
	Description: "Adds two integer numbers.",
	InputSchema: mcpschema.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"A": map[string]string{"type": "integer", "description": "First number"},
			"B": map[string]string{"type": "integer", "description": "Second number"},
		},
		Required: []string{"A", "B"},
	},
}

// mapToolRequest converts MCP request arguments to the heart node's input type
func mapToolRequest(ctx context.Context, req mcpschema.CallToolRequest) (AddInput, error) {
	var input AddInput
	argsMap := req.Params.Arguments
	if argsMap == nil && req.Params.Arguments != nil {
		return input, fmt.Errorf("expected arguments to be a map[string]interface{}, got %T", req.Params.Arguments)
	}
	if argsMap == nil {
		argsMap = make(map[string]interface{})
	}

	if aVal, ok := argsMap["A"]; ok {
		switch v := aVal.(type) {
		case float64:
			input.A = int(v)
		case int:
			input.A = v
		case string:
			var err error
			input.A, err = strconv.Atoi(v)
			if err != nil {
				return input, fmt.Errorf("invalid type or format for argument 'A': expected number, got string '%s'", v)
			}
		default:
			return input, fmt.Errorf("invalid type for argument 'A': expected number, got %T", aVal)
		}
	} else {
		return input, errors.New("missing required argument 'A'")
	}

	if bVal, ok := argsMap["B"]; ok {
		switch v := bVal.(type) {
		case float64:
			input.B = int(v)
		case int:
			input.B = v
		case string:
			var err error
			input.B, err = strconv.Atoi(v)
			if err != nil {
				return input, fmt.Errorf("invalid type or format for argument 'B': expected number, got string '%s'", v)
			}
		default:
			return input, fmt.Errorf("invalid type for argument 'B': expected number, got %T", bVal)
		}
	} else {
		return input, errors.New("missing required argument 'B'")
	}

	return input, nil
}

// mapToolResponse converts the heart node's output to the MCP result format
func mapToolResponse(ctx context.Context, out AddOutput) (*mcpschema.CallToolResult, error) {
	return &mcpschema.CallToolResult{
		Content: []mcpschema.Content{mcpschema.NewTextContent(strconv.Itoa(out.Result))},
	}, nil
}

// --- 4. Main Workflow Definition (remains the same) ---
func mainWorkflowHandler(ctx heart.Context, prompt string) heart.ExecutionHandle[goopenai.ChatCompletionResponse] {
	llmNodeDef := openai.CreateChatCompletion("base_llm_call")
	mcpNodeDef := openaimw.WithMCP("mcp_tool_adder", llmNodeDef)
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a helpful assistant. Use tools when necessary."},
			{Role: goopenai.ChatMessageRoleUser, Content: prompt},
		},
		MaxTokens: 500,
	}
	finalResponseHandle := mcpNodeDef.Start(heart.Into(request))
	return finalResponseHandle
}

// --- 5. Main Execution Logic (Updated) ---
func main() {
	fmt.Println("--- Starting Heart MCP Example ---")

	// --- Setup Tool Node ---
	addNodeDef := DefineAddNode("adder")

	// --- Adapt Tool for MCP ---
	adaptedTool := mcp.IntoTool(
		addNodeDef,
		addToolSchema,
		mapToolRequest,
		mapToolResponse,
	)
	fmt.Printf("Adapted heart node as MCP tool '%s'\n", adaptedTool.Definition().Name)

	// --- Setup MCP Server ---
	mcpServer := mcpserver.NewMCPServer(
		"heart-mcp-example-server",
		"1.0.0",
		mcpserver.WithLogging(),
	)
	mcp.AddTools(mcpServer, adaptedTool)
	fmt.Printf("Added tool '%s' to MCP Server\n", adaptedTool.Definition().Name)

	// --- Wrap MCP Server with SSE Server for HTTP access ---
	sseServer := mcpserver.NewSSEServer(mcpServer)
	fmt.Println("Wrapped MCP Server with SSE Server")

	// Start a local test server using the SSEServer as the handler
	testServer := httptest.NewServer(sseServer)
	defer testServer.Close()
	fmt.Printf("MCP Server (via SSE wrapper) running locally at: %s\n", testServer.URL)

	// --- Setup Clients ---
	// Construct the full SSE endpoint URL
	sseEndpointURL := testServer.URL + sseServer.CompleteSsePath() // Or often just testServer.URL + "/sse" if defaults are used
	fmt.Printf("Attempting to connect SSE client to: %s\n", sseEndpointURL)

	mcpClient, err := mcpclient.NewSSEMCPClient(sseEndpointURL) // Use the specific SSE endpoint URL
	if err != nil {
		log.Fatalf("Error creating MCP SSE client: %v", err)
	}

	// It's often necessary to start the SSE client's connection loop
	// Use a cancellable context for the client's background processing
	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel() // Ensure client processing stops when main exits

	// Start the client's background processing in a separate goroutine
	fmt.Println("Starting SSE Client background processing...")
	if startErr := mcpClient.Start(clientCtx); startErr != nil && !errors.Is(startErr, context.Canceled) {
		// Log non-cancel errors from the client
		log.Printf("SSE Client error: %v", startErr)
	}

	// You might need to initialize the client explicitly after starting it
	// This depends on whether the heart middleware handles initialization
	initReq := mcpschema.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpschema.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpschema.Implementation{Name: "heart-mcp-example-client", Version: "1.0.0"}
	ctx := context.Background()
	_, initErr := mcpClient.Initialize(ctx, initReq)
	if initErr != nil {
		log.Fatalf("Failed to initialize MCP client: %v", initErr)
	}
	fmt.Println("MCP SSE Client created and initialized, pointing to local server.")

	// Setup Real OpenAI Client
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatalln("Error: OPENAI_API_KEY environment variable not set.")
	}
	openaiClient := goopenai.NewClient(apiKey)
	fmt.Println("Real OpenAI Client created.")

	// --- Setup Dependency Injection ---
	// Pass the MCPClient interface (SSEMCPClient implements this)
	diErr := heart.Dependencies(
		openai.Inject(openaiClient),
		openai.InjectMCPClient(mcpClient), // Inject the SSEMCPClient instance
	)
	if diErr != nil {
		log.Fatalf("Error setting up dependencies: %v", diErr)
	}
	fmt.Println("Dependencies injected (Real OpenAI + MCP Client).")

	// --- Define the Main Workflow ---
	mainWorkflowDef := heart.WorkflowFromFunc("mainToolWorkflow", mainWorkflowHandler)
	fmt.Println("Main workflow defined.")

	// --- Execute Workflow ---
	// execCtx timeout is already defined above

	inputPrompt := "Can you please tell me what 5 plus 7 equals?"
	fmt.Printf("\n--- Executing Workflow ---\nInput Prompt: \"%s\"\n", inputPrompt)

	workflowHandle := mainWorkflowDef.Start(heart.Into(inputPrompt))
	fileStore, err := store.NewFileStore("workflows")
	if err != nil {
		log.Fatalf("error setting up file store: %v", fileStore)
	}
	finalResult, err := heart.Execute(ctx, workflowHandle, heart.WithStore(fileStore))

	fmt.Println("\n--- Workflow Execution Finished ---")

	// --- Assert Results (same as before) ---
	// ... (assertion logic remains the same) ...
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "Timeout likely occurred.")
		}
		if strings.Contains(err.Error(), "dependency injection failed") || strings.Contains(err.Error(), "no dependency provided") {
			fmt.Fprintln(os.Stderr, "Hint: A dependency injection error occurred.")
		}
		os.Exit(1)
	}

	fmt.Println("\nFinal LLM Response:")
	if len(finalResult.Choices) > 0 {
		choice := finalResult.Choices[0]
		fmt.Printf("Content: %s\n", choice.Message.Content)
		fmt.Printf("Finish Reason: %s\n", choice.FinishReason)

		// --- Corrected Assertions ---
		fmt.Println("\n--- Assertions ---")
		success := true // Assume success initially

		// 1. Check if the final answer is correct
		if strings.Contains(choice.Message.Content, "12") {
			fmt.Println("[SUCCESS] Final response content contains '12'.")
		} else {
			fmt.Println("[FAILURE] Final response content does NOT contain '12'.")
			success = false
		}

		// 2. Check if the final finish reason is 'stop' (expected after successful tool use)
		if choice.FinishReason == goopenai.FinishReasonStop {
			fmt.Println("[SUCCESS] Final finish reason is 'stop'.")
		} else {
			// It's possible a model might use a tool and *still* finish with tool_calls if it needs another one,
			// but for this simple example, 'stop' is expected. Treat other reasons as unexpected.
			fmt.Printf("[WARNING] Final finish reason was '%s', expected 'stop'.\n", choice.FinishReason)
			// Optional: Log hint if it was tool_calls again
			if choice.FinishReason == goopenai.FinishReasonToolCalls {
				fmt.Println("  Hint: The model requested further tool calls after the initial one.")
			}
		}

		// Optional: Check if the tool was actually called by inspecting workflow data - skipped for simplicity

		fmt.Println("--- End Assertions ---")
		if !success {
			log.Println("\nOne or more critical assertions failed.") // Use log
			os.Exit(1)                                               // Exit if critical assertions failed
		}

	} else {
		log.Println("[FAILURE] No choices received in final response.") // Use log
		os.Exit(1)
	}

	fmt.Println("\n--- MCP Example Finished ---")
}
