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
For each argument, provide a clear, concise description that captures the essential reasoning or claim being made. Focus on the underlying logic or justification being used to defend non-vegan practices, rather than just paraphrasing the surface-level statements.
Your output must be structured in the JSON format specified by the provided schema.
`

var carnistUserMsg = `"Look, veganism is completely unnatural - our ancestors have been eating meat for millions of years and that's just how nature intended it. Plus, studies show that plants actually feel pain too, so you're not really saving anything by eating them instead of animals. And what about all the small family farms that would go bankrupt if everyone stopped eating meat? You're just trying to destroy people's livelihoods with your extreme ideology."`

func handleCarnism(ctx heart.Context, in goopenai.ChatCompletionRequest) heart.Outputer[QuestionAnswer] {
	threeQuestions := openaimiddleware.WithStructuredOutput[VeganQuestions](
		openai.CreateChatCompletion(ctx, "three-questions"),
	).Bind(heart.Into(in))

	firstQuestionRequest := heart.Transform(threeQuestions, func(questions VeganQuestions) (goopenai.ChatCompletionRequest, error) {
		return goopenai.ChatCompletionRequest{
			Model:       goopenai.GPT4oMini,
			MaxTokens:   2048,
			Temperature: 1.000000,
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
	})

	answerToFirstQuestion := openaimiddleware.WithStructuredOutput[QuestionAnswer](
		openai.CreateChatCompletion(ctx, "first-question-answer"),
	).Bind(firstQuestionRequest)

	return answerToFirstQuestion
}

func main() {
	// Idea: add WithConnector to get individual nodes.
	authToken := os.Getenv("OPENAI_API_KEY")
	client := goopenai.NewClient(authToken)

	ctx := context.Background()

	heart.Dependencies(openai.Inject(client))

	fileStore, err := store.NewFileStore("workflows")
	if err != nil {
		fmt.Println("error creating file store: ", err)
		return
	}

	carnistDebunkerWorkflow := heart.DefineWorkflow(handleCarnism, heart.WithStore(fileStore))

	answer, err := carnistDebunkerWorkflow.New(ctx, goopenai.ChatCompletionRequest{
		Messages: []goopenai.ChatCompletionMessage{
			{Content: systemMsg, Role: goopenai.ChatMessageRoleSystem},
			{Content: carnistUserMsg, Role: goopenai.ChatMessageRoleUser},
		},
		Model:       goopenai.GPT4oMini,
		MaxTokens:   2048,
		Temperature: 1.000000,
	})
	if err != nil {
		fmt.Println("error running workflow: ", err)
		return
	}
	fmt.Println("Success!")
	fmt.Println(answer.Answer)
}
