package main

import (
	"fmt"
	"heart"
	"heart/nodetypes/openai"
	"net/http"

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

func handleCarnism(ctx heart.WorkflowContext, in goopenai.ChatCompletionRequest) heart.Output[goopenai.ChatCompletionRequest, QuestionAnswer] {
	threeQuestions := openai.StructuredOutput[VeganQuestions](ctx, "three-questions").Input(in)
	answerToFirstQuestion := openai.StructuredOutput[QuestionAnswer](ctx, "first-question-answer").
		FanIn(func(nc heart.NodeContext) (goopenai.ChatCompletionRequest, error) {
			req := goopenai.ChatCompletionRequest{
				Model:       "model/",
				MaxTokens:   2048,
				Temperature: 1.000000,
			}
			questions, err := threeQuestions.Get(nc)
			if err != nil {
				return req, err
			}

			req.Messages = []goopenai.ChatCompletionMessage{
				{
					Content: "You are the best vegan activist ever. Please debunk this non-vegan argument.",
					Role:    goopenai.ChatMessageRoleSystem,
				},
				{
					Content: questions.Output.CarnistArgumentOne,
					Role:    goopenai.ChatMessageRoleUser,
				},
			}
			return req, nil
		})

	return answerToFirstQuestion
}

func main() {
	// Idea: add WithConnector to get individual nodes.
	client := goopenai.NewClientWithConfig(goopenai.ClientConfig{
		BaseURL:    "http://localhost:7999/v1",
		HTTPClient: &http.Client{},
	})

	heart.Dependencies(openai.Inject(client))

	carnistDebunkerWorkflowFactory := heart.NewWorkflowFactory(handleCarnism)

	answer, err := carnistDebunkerWorkflowFactory.New(goopenai.ChatCompletionRequest{
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem},
			{Content: carnistUserMsg, Role: goopenai.ChatMessageRoleUser},
		},
		Model:       "model/",
		MaxTokens:   2048,
		Temperature: 1.000000,
	})
	if err != nil {
		fmt.Println("error runnign workflow: ", err)
		return
	}
	fmt.Println("Success!")
	fmt.Println(answer)
}
