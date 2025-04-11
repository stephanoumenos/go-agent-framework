// ./examples/tools/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai"
	openaimiddleware "heart/nodes/openai/middleware" // Assuming middleware is in this path
	"heart/store"
	"os"
	"strings"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// --- Simple Weather Tool Implementation ---

// WeatherParams defines the structure for our tool's parameters.
type WeatherParams struct {
	Location string `json:"location"`
}

// GetCurrentWeatherTool implements the heart.Tool interface.
type GetCurrentWeatherTool struct {
	// No dependencies needed for this simple tool
}

// DefineTool describes the tool for the OpenAI API.
func (t *GetCurrentWeatherTool) DefineTool() openaimiddleware.ToolDefinition {
	return openaimiddleware.ToolDefinition{
		Name:        "get_current_weather",
		Description: "Get the current weather in a given location",
		Parameters: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"location": {
					Type:        jsonschema.String,
					Description: "The city and state, e.g. San Francisco, CA",
				},
			},
			Required: []string{"location"},
		},
	}
}

// ParseParams parses the arguments map into WeatherParams.
func (t *GetCurrentWeatherTool) ParseParams(arguments map[string]any) (any, error) {
	var params WeatherParams
	location, ok := arguments["location"].(string)
	if !ok {
		return nil, errors.New("missing or invalid type for 'location' parameter")
	}
	params.Location = location
	return params, nil
}

// Call executes the tool's logic.
func (t *GetCurrentWeatherTool) Call(ctx context.Context, params any) (string, error) {
	weatherParams, ok := params.(WeatherParams)
	if !ok {
		return "", errors.New("invalid parameters type passed to weather tool call")
	}

	// In a real tool, you'd call a weather API here.
	location := weatherParams.Location
	var weatherData string
	if strings.Contains(strings.ToLower(location), "london") {
		weatherData = `{"location": "` + location + `", "temperature": "15", "unit": "celsius", "description": "Cloudy"}`
	} else if strings.Contains(strings.ToLower(location), "tokyo") {
		weatherData = `{"location": "` + location + `", "temperature": "22", "unit": "celsius", "description": "Sunny"}`
	} else {
		weatherData = `{"location": "` + location + `", "temperature": "20", "unit": "celsius", "description": "Partly sunny"}`
	}

	fmt.Printf("[Tool Execution: Returning weather for %s]\n", location)
	return weatherData, nil // Return JSON string as per OpenAI examples
}

// Ensure GetCurrentWeatherTool implements the interface
var _ openaimiddleware.Tool = (*GetCurrentWeatherTool)(nil)

// --- Workflow Definition ---

// toolWorkflowHandler defines the workflow using the tool middleware.
// Returns heart.Output containing the final response.
func toolWorkflowHandler(ctx heart.Context, in goopenai.ChatCompletionRequest) heart.Output[goopenai.ChatCompletionResponse] {

	// 1. Instantiate the tool(s)
	weatherTool := &GetCurrentWeatherTool{}

	// 2. Define the underlying LLM node definition (primarily for config)
	llmNodeDef := openai.CreateChatCompletion(ctx, "llm_call_config") // ID for the underlying node config

	// 3. Define the Tool Middleware, wrapping the LLM config node
	toolsMiddlewareDef := openaimiddleware.WithTools(
		ctx,                 // Pass the context
		"weather_tool_step", // Node ID for the middleware step
		llmNodeDef,          // Pass the underlying node def (potentially for config)
		weatherTool,         // Pass the instantiated tool(s)
	)

	// 4. Bind the initial input request to the middleware node. Bind starts execution.
	finalOutputNode := toolsMiddlewareDef.Bind(heart.Into(in)) // Returns Node

	// 5. Return the output handle from the middleware node (Node satisfies Output)
	return finalOutputNode
}

// --- Main Execution ---

func main() {
	fmt.Println("Starting Tool Calling Example...")

	// 1. Setup OpenAI Client & Dependencies
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}
	client := goopenai.NewClient(apiKey)

	// Inject the client dependency
	err := heart.Dependencies(openai.Inject(client))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}

	fileStore, err := store.NewFileStore("workflows_tools") // Use separate dir
	if err != nil {
		fmt.Println("error creating file store: ", err)
		return
	}

	// 2. Define Workflow
	toolWorkflow := heart.DefineWorkflow(toolWorkflowHandler, heart.WithStore(fileStore))

	// 3. Prepare Initial Request
	initialRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Ensure model supports tool calling
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleUser, Content: "What's the weather like in London?"},
			// {Role: goopenai.ChatMessageRoleUser, Content: "What's the weather like in Tokyo and London?"}, // Test multi-tool call
		},
		MaxTokens: 256,
	}

	// 4. Execute Workflow - New returns immediately
	fmt.Println("Executing workflow...")
	workflowCtx := context.Background()
	resultHandle := toolWorkflow.New(workflowCtx, initialRequest) // Returns Output

	// 5. Get Result (blocking)
	finalResponse, err := resultHandle.Out() // Call Out on the Output handle
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		os.Exit(1)
	}

	// 6. Print Final Output
	if len(finalResponse.Choices) > 0 {
		finalMessage := finalResponse.Choices[0].Message
		fmt.Println("\n--- Final Assistant Response ---")
		fmt.Println(finalMessage.Content)
		fmt.Println("------------------------------")
		fmt.Printf("Finish Reason: %s\n", finalResponse.Choices[0].FinishReason)
	} else {
		fmt.Println("Workflow completed, but no choices returned in the final response.")
	}

	fmt.Println("\nExample finished.")
}
