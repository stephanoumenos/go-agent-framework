// ./examples/mcp/example.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"time"

	"heart"
	"heart/mcp"                              // Provides the Heart MCP adapter.
	"heart/nodes/openai"                     // Provides OpenAI nodes and DI functions.
	openaimw "heart/nodes/openai/middleware" // Provides OpenAI middleware like WithMCP.
	"heart/store"                            // Provides workflow state storage options.

	// Used to start a local test server for the MCP service.

	mcpclient "github.com/mark3labs/mcp-go/client" // MCP Go client library.
	mcpschema "github.com/mark3labs/mcp-go/mcp"    // MCP Go schema definitions.
	mcpserver "github.com/mark3labs/mcp-go/server" // MCP Go server library.
	goopenai "github.com/sashabaranov/go-openai"   // OpenAI Go client library.
)

// --- 1. Define Heart Node Logic ---

// AddInput defines the input structure for the 'add' heart node.
type AddInput struct {
	A int `json:"A"` // First number to add.
	B int `json:"B"` // Second number to add.
}

// AddOutput defines the output structure for the 'add' heart node.
type AddOutput struct {
	Result int `json:"Result"` // The sum of A and B.
}

// addNodeResolver implements the core logic for the 'add' heart node.
type addNodeResolver struct{}

// addNodeNodeTypeID is the unique type identifier used for dependency injection
// related to the addNodeResolver. This example doesn't use DI for this node,
// but it's good practice to define it.
const addNodeNodeTypeID heart.NodeTypeID = "example:addNumbers"

// addNodeInitializer is the initializer for the 'add' node.
// It's required by the NodeResolver interface but doesn't need specific
// dependencies in this simple example.
type addNodeInitializer struct{}

// ID implements the NodeInitializer interface, returning the node type ID.
func (i *addNodeInitializer) ID() heart.NodeTypeID { return addNodeNodeTypeID }

// Init implements the NodeResolver interface. It returns the specific
// NodeInitializer for this node type.
func (r *addNodeResolver) Init() heart.NodeInitializer {
	return &addNodeInitializer{}
}

// Get implements the NodeResolver interface. It performs the addition.
func (r *addNodeResolver) Get(ctx context.Context, in AddInput) (AddOutput, error) {
	fmt.Printf("[Tool Execution] Adding %d + %d\n", in.A, in.B)
	return AddOutput{Result: in.A + in.B}, nil
}

// DefineAddNode creates a heart.NodeDefinition for the adder node.
// This blueprint can be reused within workflows.
func DefineAddNode(nodeID heart.NodeID) heart.NodeDefinition[AddInput, AddOutput] {
	return heart.DefineNode(nodeID, &addNodeResolver{})
}

// --- 2. Define MCP Tool Schema and Mappers ---

// addToolSchema defines the schema for the 'add_numbers' tool according to the
// Multi-Capability Protocol (MCP) specification. This schema is used by the
// MCP server to advertise the tool and by the LLM (potentially) to understand
// how to call the tool.
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

// mapToolRequest converts the arguments from an incoming MCP tool request
// (mcp.CallToolRequest) into the input type (AddInput) required by the
// corresponding heart node (addNodeResolver). It handles type conversions
// and validation.
func mapToolRequest(ctx context.Context, req mcpschema.CallToolRequest) (AddInput, error) {
	var input AddInput
	// MCP arguments are initially a map[string]interface{}.
	argsMap := req.Params.Arguments
	if argsMap == nil {
		// Handle cases where no arguments were provided, although 'A' and 'B' are required.
		return input, errors.New("missing required arguments 'A' and 'B'")
	}

	// Helper function to extract and convert integer arguments.
	// Handles potential float64 (from JSON unmarshal), int, or string types.
	extractInt := func(key string) (int, error) {
		val, ok := argsMap[key]
		if !ok {
			return 0, fmt.Errorf("missing required argument '%s'", key)
		}
		switch v := val.(type) {
		case float64:
			// JSON numbers often unmarshal as float64
			return int(v), nil
		case int:
			return v, nil
		case string:
			i, err := strconv.Atoi(v)
			if err != nil {
				return 0, fmt.Errorf("invalid format for argument '%s': expected integer string, got '%s'", key, v)
			}
			return i, nil
		default:
			return 0, fmt.Errorf("invalid type for argument '%s': expected number or numeric string, got %T", key, val)
		}
	}

	var err error
	input.A, err = extractInt("A")
	if err != nil {
		return input, err
	}
	input.B, err = extractInt("B")
	if err != nil {
		return input, err
	}

	return input, nil
}

// mapToolResponse converts the output (AddOutput) from the heart node execution
// back into the format expected by the MCP server (*mcp.CallToolResult).
// In this case, it converts the integer result into an MCP text content block.
func mapToolResponse(ctx context.Context, out AddOutput) (*mcpschema.CallToolResult, error) {
	// MCP expects results typically as a slice of content blocks.
	// Here, we return the result as a simple text string.
	return &mcpschema.CallToolResult{
		Content: []mcpschema.Content{mcpschema.NewTextContent(strconv.Itoa(out.Result))},
	}, nil
}

// --- 3. Define Main Workflow ---

// mainWorkflowHandler defines the logic for the main workflow.
// It takes a user prompt, sets up an OpenAI chat completion node wrapped with
// the MCP middleware, sends the prompt to the LLM, and returns the final response handle.
// The WithMCP middleware handles the tool discovery and execution loop if the LLM
// decides to use the 'add_numbers' tool.
func mainWorkflowHandler(ctx heart.Context, prompt string) heart.ExecutionHandle[goopenai.ChatCompletionResponse] {
	// Define the base LLM call node blueprint.
	llmNodeDef := openai.CreateChatCompletion("base_llm_call")

	// Wrap the LLM node with the MCP middleware.
	// This middleware will:
	// 1. Use the injected MCP client to list available tools (our 'add_numbers' tool).
	// 2. Add the tool schema to the request sent to the OpenAI API.
	// 3. If the LLM response requests a tool call:
	//    - Use the injected MCP client to execute the tool call via the MCP server.
	//    - Format the tool result and add it to the message history.
	//    - Send the updated history back to the LLM.
	// 4. Repeat step 3 until the LLM responds without a tool call or limits are reached.
	mcpNodeDef := openaimw.WithMCP(llmNodeDef)

	// Prepare the initial request for the OpenAI API.
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Or another model supporting tool calls.
		Messages: []goopenai.ChatCompletionMessage{
			// It's good practice to give the LLM instructions on using tools.
			{Role: goopenai.ChatMessageRoleSystem, Content: "You are a helpful assistant. Use tools when necessary to perform calculations."},
			{Role: goopenai.ChatMessageRoleUser, Content: prompt},
		},
		MaxTokens: 500,
	}

	// Start the workflow by starting the MCP-wrapped node.
	// Input is the prepared OpenAI request.
	// The handle represents the eventual final response from the LLM after any tool interactions.
	finalResponseHandle := mcpNodeDef.Start(heart.Into(request))

	// Return the handle to the final response.
	return finalResponseHandle
}

// --- 4. Main Execution Logic ---

func main() {
	fmt.Println("--- Starting Heart MCP Example ---")

	// --- Setup Tool Node ---
	// Create the heart node definition for the adder tool.
	addNodeDef := DefineAddNode("adder")

	// --- Adapt Tool for MCP ---
	// Use heart/mcp.IntoTool to bridge the heart node definition (addNodeDef),
	// its MCP schema (addToolSchema), and the mapping functions (mapToolRequest, mapToolResponse).
	// This creates an MCPTool implementation that can be used with an MCP server.
	adaptedTool := mcp.IntoTool(
		addNodeDef,
		addToolSchema,
		mapToolRequest,
		mapToolResponse,
	)
	fmt.Printf("Adapted heart node as MCP tool '%s'\n", adaptedTool.Definition().Name)

	// --- Setup Local MCP Server ---
	// Create a new MCP server instance.
	mcpServer := mcpserver.NewMCPServer(
		"heart-mcp-example-server", // Server name
		"1.0.0",                    // Server version
		mcpserver.WithLogging(),    // Enable logging for server activity.
	)
	// Register the adapted Heart tool with the MCP server.
	// mcp.AddTools handles the conversion to the server's expected format.
	mcp.AddTools(mcpServer, adaptedTool)
	fmt.Printf("Added tool '%s' to MCP Server\n", adaptedTool.Definition().Name)

	// --- Wrap MCP Server with SSE Server ---
	// The mcp-go library often uses SSE (Server-Sent Events) for communication.
	// Wrap the core MCP server logic in an SSE handler.
	sseServer := mcpserver.NewSSEServer(mcpServer)
	fmt.Println("Wrapped MCP Server with SSE Server")

	// Start a local HTTP test server using the SSEServer as the handler.
	// This makes the MCP server accessible via HTTP.
	testServer := httptest.NewServer(sseServer)
	defer testServer.Close() // Ensure the server is shut down when main exits.
	fmt.Printf("MCP Server (via SSE wrapper) running locally at: %s\n", testServer.URL)

	// --- Setup MCP Client ---
	// Construct the full URL for the SSE endpoint provided by the SSEServer.
	sseEndpointURL := testServer.URL + sseServer.CompleteSsePath()
	fmt.Printf("Attempting to connect SSE client to: %s\n", sseEndpointURL)

	// Create an MCP client that communicates over SSE, pointing to the local server.
	mcpClient, err := mcpclient.NewSSEMCPClient(sseEndpointURL)
	if err != nil {
		log.Fatalf("Error creating MCP SSE client: %v", err)
	}

	// Start the client's background processing loop required for SSE communication.
	// Use a cancellable context to manage the client's lifecycle.
	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel() // Ensure client stops when main exits.

	go func() {
		fmt.Println("Starting SSE Client background processing...")
		// Start blocks until context is cancelled or an error occurs.
		startErr := mcpClient.Start(clientCtx)
		// Log errors unless it's just the context being cancelled on shutdown.
		if startErr != nil && !errors.Is(startErr, context.Canceled) {
			log.Printf("SSE Client error during run: %v", startErr)
		}
		fmt.Println("SSE Client background processing stopped.")
	}()

	// Initialize the connection between the client and server using the MCP handshake.
	// Give the client a moment to establish the connection before initializing.
	time.Sleep(100 * time.Millisecond) // Small delay to allow connection setup.
	initReq := mcpschema.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpschema.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpschema.Implementation{Name: "heart-mcp-example-client", Version: "1.0.0"}
	// Use a timeout for the Initialize call.
	initCtx, initTimeoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initTimeoutCancel()
	_, initErr := mcpClient.Initialize(initCtx, initReq)
	if initErr != nil {
		// It's crucial initialization succeeds for the middleware to list/call tools.
		log.Fatalf("Failed to initialize MCP client: %v", initErr)
	}
	fmt.Println("MCP SSE Client created and initialized, pointing to local server.")

	// --- Setup Real OpenAI Client ---
	// Get the API key from environment variables.
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatalln("Error: OPENAI_API_KEY environment variable not set.")
	}
	openaiClient := goopenai.NewClient(apiKey)
	fmt.Println("Real OpenAI Client created.")

	// --- Setup Dependency Injection ---
	// Inject both the real OpenAI client and the MCP client (connected to our local server).
	// openai.Inject makes the OpenAI client available to CreateChatCompletion nodes.
	// openai.InjectMCPClient makes the MCP client available to the internal nodes
	// used by the WithMCP middleware (ListTools, CallTool).
	diErr := heart.Dependencies(
		openai.Inject(openaiClient),
		openai.InjectMCPClient(mcpClient),
	)
	if diErr != nil {
		log.Fatalf("Error setting up dependencies: %v", diErr)
	}
	// Reset dependencies at the end, mainly useful if this code were part of a test.
	defer heart.ResetDependencies()
	fmt.Println("Dependencies injected (Real OpenAI + MCP Client).")

	// --- Define the Main Workflow ---
	// Use the handler function defined earlier.
	mainWorkflowDef := heart.WorkflowFromFunc("mainToolWorkflow", mainWorkflowHandler)
	fmt.Println("Main workflow defined.")

	// --- Execute Workflow ---
	// Set a timeout for the entire workflow execution.
	execCtx, execCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer execCancel()

	inputPrompt := "Can you please tell me what 5 plus 7 equals?"
	fmt.Printf("\n--- Executing Workflow ---\nInput Prompt: \"%s\"\n", inputPrompt)

	// Start the workflow lazily.
	workflowHandle := mainWorkflowDef.Start(heart.Into(inputPrompt))

	// Setup a store for workflow state persistence. FileStore is used here.
	fileStore, storeErr := store.NewFileStore("workflows_mcp")
	if storeErr != nil {
		log.Fatalf("Error setting up file store: %v", storeErr)
	}
	// Optional: Clean up the store directory afterwards.
	// defer os.RemoveAll("./workflows_mcp")

	// Execute the workflow and wait for the final result.
	// heart.Execute triggers the resolution process starting from the handle.
	finalResult, execErr := heart.Execute(execCtx, workflowHandle, heart.WithStore(fileStore))

	fmt.Println("\n--- Workflow Execution Finished ---")

	// --- Process Results ---
	if execErr != nil {
		// Handle execution errors, providing context.
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", execErr)
		if errors.Is(execErr, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "Timeout likely occurred during execution.")
		}
		// Check for common DI errors which might indicate missing InjectMCPClient call.
		if strings.Contains(execErr.Error(), "dependency injection failed") || strings.Contains(execErr.Error(), "no dependency provided") {
			fmt.Fprintln(os.Stderr, "Hint: A dependency injection error occurred. Ensure InjectMCPClient was called.")
		}
		os.Exit(1)
	}

	// Print the final LLM response.
	fmt.Println("\nFinal LLM Response:")
	if len(finalResult.Choices) > 0 {
		choice := finalResult.Choices[0]
		fmt.Printf("Content: %s\n", choice.Message.Content)
		fmt.Printf("Finish Reason: %s\n", choice.FinishReason)

		// --- Simple Assertions for Example ---
		fmt.Println("\n--- Assertions ---")
		success := true // Track overall success

		// 1. Check if the final answer is correct (contains "12").
		if strings.Contains(choice.Message.Content, "12") {
			fmt.Println("[SUCCESS] Final response content contains '12'.")
		} else {
			fmt.Println("[FAILURE] Final response content does NOT contain '12'.")
			success = false
		}

		// 2. Check if the final finish reason is 'stop'.
		// For this specific prompt, we expect the LLM to stop after getting the tool result.
		if choice.FinishReason == goopenai.FinishReasonStop {
			fmt.Println("[SUCCESS] Final finish reason is 'stop'.")
		} else {
			// Finishing with tool_calls might indicate the model wanted to call another tool,
			// which is unexpected here but not necessarily a hard failure of the middleware.
			fmt.Printf("[WARNING] Final finish reason was '%s', expected 'stop'.\n", choice.FinishReason)
			if choice.FinishReason == goopenai.FinishReasonToolCalls {
				fmt.Println("  Hint: The model requested further tool calls after the initial one.")
			}
		}

		fmt.Println("--- End Assertions ---")
		if !success {
			// Exit if critical assertions failed.
			log.Println("\nOne or more critical assertions failed.")
			os.Exit(1)
		}

	} else {
		// This case should ideally not happen if execution succeeded without error.
		log.Println("[FAILURE] No choices received in final response despite successful execution.")
		os.Exit(1)
	}

	fmt.Println("\n--- MCP Example Finished Successfully ---")
}
