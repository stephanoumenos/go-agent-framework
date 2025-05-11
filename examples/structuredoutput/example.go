// ./examples/structuredoutput/example.go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	gaf "go-agent-framework"                              // Provides core workflow definitions and execution.
	"go-agent-framework/nodes/openai"                     // Provides the base OpenAI chat completion node.
	openaimw "go-agent-framework/nodes/openai/middleware" // Provides middleware like WithStructuredOutput.
	"go-agent-framework/store"                            // Provides storage options for workflow state.

	goopenai "github.com/sashabaranov/go-openai" // OpenAI Go client library.
)

// Recipe defines the desired structured output Go type.
// The WithStructuredOutput middleware will generate a JSON schema from this struct
// and instruct the LLM to return JSON conforming to that schema.
// JSON struct tags are used by the schema generator and the final JSON unmarshalling.
type Recipe struct {
	RecipeName  string   `json:"recipe_name"`                     // The name of the recipe.
	Ingredients []string `json:"ingredients"`                     // List of ingredients required.
	Steps       []string `json:"steps"`                           // Step-by-step instructions.
	PrepTime    string   `json:"prep_time"`                       // Estimated preparation time (e.g., "15 minutes").
	Description string   `json:"description,omitempty"`           // Optional short description of the recipe.
	Servings    int      `json:"servings,omitempty"`              // Optional number of servings.
	Keywords    []string `json:"keywords,omitempty"`              // Optional list of keywords (e.g., "vegan", "quick", "dessert").
	Difficulty  string   `json:"difficulty,omitempty"`            // Optional difficulty level (e.g., "Easy", "Medium", "Hard").
	Notes       string   `json:"notes,omitempty"`                 // Optional additional notes or tips.
	SourceURL   string   `json:"source_url,omitempty"`            // Optional URL of the original recipe source.
	Author      string   `json:"author,omitempty"`                // Optional author of the recipe.
	Rating      float32  `json:"rating,omitempty"`                // Optional rating (e.g., 4.5).
	Nutrition   NutInfo  `json:"nutrition_information,omitempty"` // Optional nested struct for nutritional info.
}

// NutInfo is a nested struct for nutritional information.
type NutInfo struct {
	Calories      string `json:"calories_per_serving,omitempty"`
	Protein       string `json:"protein_per_serving,omitempty"`
	Fat           string `json:"fat_per_serving,omitempty"`
	Carbohydrates string `json:"carbohydrates_per_serving,omitempty"`
}

// structuredOutputWorkflowHandler defines the logic for a workflow that uses
// the WithStructuredOutput middleware to generate a Recipe struct from an LLM.
//
// Parameters:
//   - ctx: The workflow's execution context.
//   - recipeTopic: A string describing the desired recipe (e.g., "Quick vegan cookies").
//
// Returns:
//   - An ExecutionHandle that will resolve to the generated Recipe struct or an error.
func structuredOutputWorkflowHandler(ctx gaf.Context, recipeTopic string) gaf.ExecutionHandle[Recipe] {
	// 1. Define the underlying LLM call node blueprint.
	// This is the node that will actually make the API call to OpenAI.
	// The middleware will wrap this definition.
	llmNodeDef := openai.CreateChatCompletion("generate_recipe_json")

	// 2. Wrap the LLM node definition with the WithStructuredOutput middleware.
	// Specify the target Go type (Recipe) as the generic parameter.
	// The middleware automatically handles schema generation, request modification,
	// and response parsing into the specified type.
	structuredOutputNodeDef := openaimw.WithStructuredOutput[Recipe](llmNodeDef)

	// 3. Prepare the initial ChatCompletionRequest for the *wrapped* node.
	// It's CRUCIAL that the system prompt instructs the model to output JSON
	// adhering to the schema named "output" (this name is fixed by the middleware).
	request := goopenai.ChatCompletionRequest{
		Model: goopenai.GPT4oMini, // Use a model supporting JSON Schema output mode.
		Messages: []goopenai.ChatCompletionMessage{
			{
				Role: goopenai.ChatMessageRoleSystem,
				// Instruct the model clearly to use the provided schema.
				// Reference the schema by the name "output".
				Content: "You are a helpful cooking assistant specialized in generating recipes. You MUST output a JSON object that strictly adheres to the JSON schema provided under the name 'output'. Do not include any text outside of the JSON object.",
			},
			{
				Role:    goopenai.ChatMessageRoleUser,
				Content: fmt.Sprintf("Generate a recipe based on the following topic, ensuring all fields in the 'output' schema are populated appropriately if possible: %s", recipeTopic),
			},
		},
		MaxTokens:   1024,
		Temperature: 0.5,
		// NOTE: Do NOT set ResponseFormat here; the middleware handles it.
		// Setting it here would result in an errDuplicatedResponseFormat error.
	}

	// 4. Start the structured output node (which internally starts the llmNodeDef).
	// The input is the prepared ChatCompletionRequest.
	// The output handle will resolve to the parsed Recipe struct.
	// The context is implicitly passed via the node definition's creation scope.
	recipeHandle := structuredOutputNodeDef.Start(gaf.Into(request))

	// 5. Return the handle to the final result.
	// When this handle is resolved by gaf.Execute, the middleware and the LLM call
	// will run, and the parsed Recipe struct will be returned.
	return recipeHandle
}

// main sets up dependencies, defines the structured output workflow, executes it,
// and prints the resulting Recipe struct.
func main() {
	fmt.Println("Starting Structured Output Example...")

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: OPENAI_API_KEY environment variable not set.")
		os.Exit(1)
	}

	// --- Dependency Injection ---
	// Create the OpenAI client instance.
	client := goopenai.NewClient(apiKey)
	// Inject the client instance using the standard openai.Inject function.
	// This makes the client available to the underlying openai.CreateChatCompletion node.
	if err := gaf.Dependencies(openai.Inject(client)); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up dependencies: %v\n", err)
		os.Exit(1)
	}

	// --- Store Setup ---
	// Configure a file store for persisting workflow state.
	fileStore, err := store.NewFileStore("workflows_structured_output")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file store: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Using FileStore at './workflows_structured_output'")

	// --- Workflow Definition ---
	// Define the workflow blueprint using the handler function.
	workflowID := gaf.NodeID("recipeGeneratorWorkflow")
	recipeWorkflowDef := gaf.WorkflowFromFunc(workflowID, structuredOutputWorkflowHandler)

	// --- Workflow Execution ---
	fmt.Println("Defining and executing recipe workflow...")
	// Create a Go context with a timeout for the execution.
	workflowCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	inputTopic := "Quick vegan chocolate chip cookies"

	// Execute the workflow graph and wait for the result (Recipe struct).
	fmt.Println("Executing workflow and waiting for result...")
	recipeResult, err := gaf.ExecuteWorkflow(workflowCtx, recipeWorkflowDef, inputTopic, gaf.WithStore(fileStore))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow execution failed: %v\n", err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "Waiting for workflow result cancelled or timed out: %v\n", workflowCtx.Err())
		}
		os.Exit(1)
	}

	// --- Output ---
	// Print the fields of the successfully parsed Recipe struct.
	fmt.Println("\nWorkflow completed successfully!")
	fmt.Printf("📜 Recipe Name: %s\n", recipeResult.RecipeName)
	fmt.Printf("⏱️ Prep Time:   %s\n", recipeResult.PrepTime)
	fmt.Printf("⚙️ Difficulty:  %s\n", recipeResult.Difficulty)
	fmt.Printf("🧍 Servings:    %d\n", recipeResult.Servings)
	fmt.Printf("📄 Description: %s\n", recipeResult.Description)
	fmt.Printf("🔑 Keywords:    %v\n", recipeResult.Keywords)

	fmt.Println("\n🛒 Ingredients:")
	for _, ingredient := range recipeResult.Ingredients {
		fmt.Printf("  - %s\n", ingredient)
	}

	fmt.Println("\n📝 Steps:")
	for i, step := range recipeResult.Steps {
		fmt.Printf("  %d. %s\n", i+1, step)
	}

	if recipeResult.Nutrition != (NutInfo{}) {
		fmt.Println("\n🍎 Nutrition (per serving):")
		fmt.Printf("  Calories: %s\n", recipeResult.Nutrition.Calories)
		fmt.Printf("  Protein:  %s\n", recipeResult.Nutrition.Protein)
		fmt.Printf("  Fat:      %s\n", recipeResult.Nutrition.Fat)
		fmt.Printf("  Carbs:    %s\n", recipeResult.Nutrition.Carbohydrates)
	}

	if recipeResult.Notes != "" {
		fmt.Println("\n💡 Notes:")
		fmt.Println(recipeResult.Notes)
	}
	if recipeResult.SourceURL != "" {
		fmt.Printf("\n🔗 Source: %s (Author: %s)\n", recipeResult.SourceURL, recipeResult.Author)
	}
	if recipeResult.Rating > 0 {
		fmt.Printf("\n⭐ Rating: %.1f\n", recipeResult.Rating)
	}

	fmt.Println("\nExample finished.")
}
