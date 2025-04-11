// ./examples/main.go
package main

import (
	"context"
	"fmt"
	"heart"
	"heart/nodes/openai"
	openaimiddleware "heart/nodes/openai/middleware"
	"heart/store"
	"os"

	goopenai "github.com/sashabaranov/go-openai"
)

type VeganQuestions struct {
	CarnistArgumentOne   string `json:"carnist_argument_one" jsonschema_description:"First carnist argument that is implicitly present in the person's phrase"`
	CarnistArgumentTwo   string `json:"carnist_argument_two" jsonschema_description:"Second carnist argument that is implicitly present in the person's phrase"`
	CarnistArgumentThree string `json:"carnist_argument_three" jsonschema_description:"Third carnist argument that is implicitly present in the person's phrase"`
}

type QuestionAnswer struct {
	Answer string `json:"answer" jsonschema_description:"The best debunk for the anti-vegan carnist argument. I.e., what an effective vegan activist would say."`
}

var systemMsg = `
You are an AI trained to analyze anti-vegan messages and identify the core carnist arguments being made. When presented with a message, extract exactly three main carnist arguments that are either explicitly stated or implicitly present in the text.
For each argument, provide a clear, concise description that captures the essential reasoning or claim being used to defend non-vegan practices, rather than just paraphrasing the surface-level statements.
Your output must be structured in the JSON format specified by the provided schema.
`

var carnistUserMsg = `"Look, veganism is completely unnatural - our ancestors have been eating meat for millions of years and that's just how nature intended it. Plus, studies show that plants actually feel pain too, so you're not really saving anything by eating them instead of animals. And what about all the small family farms that would go bankrupt if everyone stopped eating meat? You're just trying to destroy people's livelihoods with your extreme ideology."`

// Updated function signature to return heart.Output
func handleCarnism(ctx heart.Context, in goopenai.ChatCompletionRequest) heart.Output[QuestionAnswer] {
	threeQuestionsLLMNode := openai.CreateChatCompletion(ctx, "three-questions-llm")

	threeQuestionsMiddlewareDef := openaimiddleware.WithStructuredOutput[VeganQuestions](
		ctx,                     // Pass the current context
		"parse-three-questions", // Give the middleware step a unique ID within this context
		threeQuestionsLLMNode,   // Tell the middleware which node definition to wrap
	)
	// Bind the initial input to the *middleware* definition.
	// This returns the Node for the middleware step.
	threeQuestionsNode := threeQuestionsMiddlewareDef.Bind(heart.Into(in)) // Node[Request, VeganQuestions]

	// Use the output handle (Output[VeganQuestions]) from the node
	firstQuestionRequest := heart.Transform(
		ctx,
		"first-question-request",
		threeQuestionsNode, // Pass the Node (which satisfies Output)
		func(ctx context.Context, questions VeganQuestions) (goopenai.ChatCompletionRequest, error) {
			return goopenai.ChatCompletionRequest{
				Model:     goopenai.GPT4oMini,
				MaxTokens: 2048,
				Messages: []goopenai.ChatCompletionMessage{
					{
						Content: "You are the best vegan activist ever. Please debunk this non-vegan argument.",
						Role:    goopenai.ChatMessageRoleSystem,
					},
					{
						Content: questions.CarnistArgumentOne,
						Role:    goopenai.ChatMessageRoleUser,
					},
				},
			}, nil
		}) // Returns Node[VeganQuestions, Request]

	firstQuestionAnswerLLMNode := openai.CreateChatCompletion(ctx, "first-question-answer-llm") // Node for the actual LLM call
	answerToFirstQuestionMiddlewareDef := openaimiddleware.WithStructuredOutput[QuestionAnswer](
		ctx,                        // Pass the current context
		"parse-first-answer",       // Give the middleware step a unique ID
		firstQuestionAnswerLLMNode, // Tell the middleware to wrap the second LLM call
	)
	// Bind the request (output of the transform) to the *second middleware* definition.
	answerToFirstQuestionNode := answerToFirstQuestionMiddlewareDef.Bind(firstQuestionRequest) // Node[Request, QuestionAnswer]

	// Return the final output handle (Node satisfies Output)
	return answerToFirstQuestionNode
}

func main() {
	// Idea: add WithConnector to get individual nodes.
	authToken := os.Getenv("OPENAI_API_KEY")
	client := goopenai.NewClient(authToken)

	ctx := context.Background()

	err := heart.Dependencies(openai.Inject(client))
	if err != nil {
		fmt.Println("error setting dependencies: ", err)
		return
	}

	fileStore, err := store.NewFileStore("workflows")
	if err != nil {
		fmt.Println("error creating file store: ", err)
		return
	}

	carnistDebunkerWorkflow := heart.DefineWorkflow(handleCarnism, heart.WithStore(fileStore))

	// New is now non-blocking and returns a result handle (WorkflowOutput)
	resultHandle := carnistDebunkerWorkflow.New(ctx, goopenai.ChatCompletionRequest{
		Messages: []goopenai.ChatCompletionMessage{
			{Content: systemMsg, Role: goopenai.ChatMessageRoleSystem},
			{Content: carnistUserMsg, Role: goopenai.ChatMessageRoleUser},
		},
		Model:     goopenai.GPT4oMini,
		MaxTokens: 2048,
	})

	fmt.Println("Workflow started...")

	// Block and wait for the result using Out() on the WorkflowOutput
	answer, err := resultHandle.Out()
	if err != nil {
		fmt.Println("error running workflow: ", err)
		return
	}

	fmt.Println("Success!")
	fmt.Println(answer.Answer)
}
