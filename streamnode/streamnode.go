package StreamNode

import (
	"context"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/ssestream"
)

type StreamNode struct {
	started, completed bool
	params             openai.CompletionNewParams
	generatedTokens    int
	response           *strings.Builder
	stream             *ssestream.Stream[openai.Completion]
}

func NewStreamNode(stream *ssestream.Stream[openai.Completion]) *StreamNode {
	return &StreamNode{
		started:         false,
		completed:       false,
		generatedTokens: 0,
		response:        &strings.Builder{},
		stream:          stream,
	}
}

func (n *StreamNode) Start(ctx context.Context, client *openai.Client) {
	if n.started {
		return
	}
	n.stream = client.Completions.NewStreaming(ctx, n.params)
	n.started = true
}

func (n *StreamNode) Next() bool {
	if !n.stream.Next() {
		n.completed = true
		return false
	}
	return true
}

func (n *StreamNode) Token() (token string) {
	evt := n.stream.Current()
	if len(evt.Choices) > 0 {
		n.generatedTokens++
		n.response.WriteString(evt.Choices[0].Text)
		return evt.Choices[0].Text
	}
	return ""
}
