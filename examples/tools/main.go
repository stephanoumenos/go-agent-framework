package main

import (
	"context"
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai"
	openaimiddleware "heart/nodes/openai/middleware" // Assuming middleware is in this path
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

// defineTool describes the tool for the OpenAI API.
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

// parseParams parses the arguments map into WeatherParams.
func (t *GetCurrentWeatherTool) ParseParams(arguments map[string]any) (any, error) {
	var params WeatherParams
	location, ok := arguments["location"].(string)
	if !ok {
		return nil, errors.New("missing or invalid type for 'location' parameter")
	}
	params.Location = location
	return params, nil
}

// call executes the tool's logic.
func (t *GetCurrentWeatherTool) Call(ctx context.Context, params any) (string, error) {
	weatherParams, ok := params.(WeatherParams)
	if !ok {
		return "", errors.New("invalid parameters type passed to weather tool call")
	}

	// In a real tool, you'd call a weather API here.
	// For the example, we'll return a hardcoded string.
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
func toolWorkflowHandler(ctx heart.Context, in goopenai.ChatCompletionRequest) heart.Outputer[goopenai.ChatCompletionResponse] {

	// 1. Instantiate the tool(s)
	weatherTool := &GetCurrentWeatherTool{}

	// 2. Define the underlying LLM node definition
	// This definition might primarily provide default config now.
	llmNodeDef := openai.CreateChatCompletion(ctx, "llm_call_config") // ID for the underlying node config

	// 3. Define the Tool Middleware, wrapping the LLM config node
	toolsMiddlewareDef := openaimiddleware.WithTools(
		ctx,                 // Pass the context
		"weather_tool_step", // Node ID for the middleware step
		llmNodeDef,          // Pass the underlying node def (potentially for config)
		weatherTool,         // Pass the instantiated tool(s)
	)

	// 4. Bind the initial input request to the middleware node
	finalOutput := toolsMiddlewareDef.Bind(heart.Into(in))

	// 5. Return the outputer from the middleware node
	return finalOutput
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

	// 2. Define Workflow
	toolWorkflow := heart.DefineWorkflow(toolWorkflowHandler) // Using default memory store

	// 3. Prepare Initial Request
	// Use a model capable of tool calling (e.g., gpt-4o-mini, gpt-4-turbo)
	initialRequest := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleUser, Content: "What's the weather like in London?"},
		},
		MaxTokens: 256, // Keep it reasonable for the example
	}

	// 4. Execute Workflow
	fmt.Println("Executing workflow...")
	workflowCtx := context.Background() // Use a background context for this example
	resultHandle := toolWorkflow.New(workflowCtx, initialRequest)

	// 5. Get Result (blocking)
	finalResponse, err := resultHandle.Get()
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
		// You could also inspect finalResponse.Choices[0].FinishReason
		// If it was 'tool_calls', something might have gone wrong in the final step.
		fmt.Printf("Finish Reason: %s\n", finalResponse.Choices[0].FinishReason)
	} else {
		fmt.Println("Workflow completed, but no choices returned in the final response.")
		// Optionally print the full response for debugging
		// responseBytes, _ := json.MarshalIndent(finalResponse, "", "  ")
		// fmt.Println(string(responseBytes))
	}

	fmt.Println("\nExample finished.")
}
