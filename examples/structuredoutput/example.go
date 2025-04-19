// ./examples/structuredoutput/example.go
package main

import (
	"context"
	"errors"
	"fmt"
	"heart"
	"heart/nodes/openai"
	openaimw "heart/nodes/openai/middleware"
	"heart/store"
	"os"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
)

// Recipe defines the desired structured output from the LLM.
type Recipe struct {
	RecipeName  string   `json:"recipe_name"`
	Ingredients []string `json:"ingredients"`
	Steps       []string `json:"steps"`
	PrepTime    string   `json:"prep_time"` // e.g., "15 minutes"
}

// structuredOutputWorkflowHandler is the handler function for the workflow.
// It takes the workflow's context and input, returning a handle to the final output.
func structuredOutputWorkflowHandler(ctx heart.Context, recipeTopic string) heart.ExecutionHandle[Recipe] {

	// 1. Define the underlying LLM call node.
	// Node IDs are local to the workflow context path.
	llmNodeDef := openai.CreateChatCompletion("generate_recipe_json")

	// 2. Wrap the LLM node with WithStructuredOutput.
	// The middleware will modify the request to ask for JSON matching the Recipe struct
	// and parse the response into the Recipe struct.
	structuredOutputNodeDef := openaimw.WithStructuredOutput[Recipe](llmNodeDef)

	// 3. Prepare the initial request for the wrapped node.
	// It's crucial to instruct the LLM to provide JSON matching the schema.
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Or any model supporting JSON Schema output
		Messages: []goopenai.ChatCompletionMessage{
			{
				Role: goopenai.ChatMessageRoleSystem,
				// Instruct the model to use the provided schema
				Content: "You are a helpful cooking assistant. Generate recipes based on user requests. You MUST output a JSON object that strictly adheres to the provided JSON Schema named 'output'.",
			},
			{
				Role:    goopenai.ChatMessageRoleUser,
				Content: fmt.Sprintf("Generate a simple recipe based on the following topic: %s", recipeTopic),
			},
		},
		MaxTokens:   1024,
		Temperature: 0.5,
		// ResponseFormat will be automatically added by the WithStructuredOutput middleware.
	}

	// 4. Start the structured output node (which internally starts the LLM node).
	// The input is the ChatCompletionRequest, the output is the Recipe struct.
	// Start only requires the input handle. Context is implicitly handled.
	recipeHandle := structuredOutputNodeDef.Start(heart.Into(request)) // CORRECTED: Removed ctx argument

	// The handle returned corresponds to the final Recipe output.
	return recipeHandle
}

func main() {
	fmt.Println("Starting Structured Output Example...")

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}

	// --- Dependency Injection ---
	client := goopenai.NewClient(apiKey)
	if err := heart.Dependencies(openai.Inject(client)); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}

	// --- Store Setup ---
	fileStore, err := store.NewFileStore("workflows_structured_output")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file store: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Using FileStore at './workflows_structured_output'")
	// defer os.RemoveAll("./workflows_structured_output") // Optional cleanup

	// --- Workflow Definition ---
	// CORRECTED: Use NewWorkflowResolver and DefineNode
	workflowID := heart.NodeID("recipeGeneratorWorkflow")
	recipeWorkflowDef := heart.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// --- Workflow Execution ---
	fmt.Println("Defining and executing recipe workflow...")
	workflowCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	inputTopic := "Quick vegan chocolate chip cookies"

	// Start the workflow LAZILY, get an ExecutionHandle
	fmt.Println("Starting workflow (lazy)...")
	// Start the workflow definition like any other node
	workflowHandle := recipeWorkflowDef.Start(heart.Into(inputTopic))

	// Execute the workflow and wait for the result (Recipe struct)
	fmt.Println("Executing workflow and waiting for result...")
	// Pass the handle and options (like store) to Execute
	recipeResult, err := heart.Execute(
		workflowCtx,
		workflowHandle,
		heart.WithStore(fileStore), // Apply store option during execution
	)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "Waiting for workflow result cancelled or timed out: %v\n", workflowCtx.Err())
		}
		os.Exit(1)
	}

	// --- Output ---
	fmt.Println("\nWorkflow completed successfully!")
	fmt.Printf("📜 Recipe Name: %s\n", recipeResult.RecipeName)
	fmt.Printf("⏱️ Prep Time: %s\n", recipeResult.PrepTime)
	fmt.Println("🛒 Ingredients:")
	for _, ingredient := range recipeResult.Ingredients {
		fmt.Printf("  - %s\n", ingredient)
	}
	fmt.Println("📝 Steps:")
	for i, step := range recipeResult.Steps {
		fmt.Printf("  %d. %s\n", i+1, step)
	}

	fmt.Println("\nExample finished.")
}
