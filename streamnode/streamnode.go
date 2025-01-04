package streamnode

import (
	"context"
	"go-cot/supervisor"
	"strings"
	"sync"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/ssestream"
)

type StreamNode struct {
	started, completed bool
	client             *openai.Client
	params             openai.CompletionNewParams
	generatedTokens    int
	response           *strings.Builder
	stream             *ssestream.Stream[openai.Completion]
	once               *sync.Once
	err                error
}

type StreamNodeDefinition struct{}

func (s *StreamNodeDefinition) Define(supervisor.LLMContext, openai.CompletionNewParams) *StreamNode {
	return &StreamNode{}
}

func (s *StreamNodeDefinition) Marshal(openai.CompletionNewParams) ([]byte, error) {
	panic("implement me")
}

func (s *StreamNodeDefinition) Unmarshal([]byte) (*openai.CompletionNewParams, error) {
	panic("implement me")
}

func NewStreamNodeDefinition(ctx supervisor.LLMContext, params openai.CompletionNewParams) StreamNodeDefinition {
	panic("implement me")
}

func NewStreamNode2(openai.CompletionNewParams) supervisor.NodeDefiner[openai.CompletionNewParams, string] {
	return nil
}

type StreamNodeResult struct {
	params openai.CompletionNewParams
	result string
}

func NewStreamNode(ctx context.Context, client *openai.Client, params openai.CompletionNewParams) *StreamNode {
	return &StreamNode{
		started:         false,
		completed:       false,
		client:          client,
		params:          params,
		generatedTokens: 0,
		response:        &strings.Builder{},
		once:            &sync.Once{},
	}
}

func (n *StreamNode) start(ctx context.Context) {
	if n.started {
		return
	}
	supervisor.RegisterDryRunNode(ctx)
	n.stream = n.client.Completions.NewStreaming(ctx, n.params)
	n.started = true
}

func (n *StreamNode) next() bool {
	if !n.stream.Next() {
		n.completed = true
		return false
	}
	return true
}

func (n *StreamNode) token() (token string) {
	evt := n.stream.Current()
	if len(evt.Choices) > 0 {
		n.generatedTokens++
		n.response.WriteString(evt.Choices[0].Text)
		return evt.Choices[0].Text
	}
	return ""
}

func (n *StreamNode) Get(ctx context.Context) (*StreamNodeResult, error) {
	n.once.Do(func() {
		n.start(ctx)
		for n.next() {
			n.response.WriteString(n.token())
		}
		n.err = n.stream.Err()
	})

	if n.err != nil {
		return nil, n.err
	}
	return &StreamNodeResult{
		params: n.params,
		result: n.response.String(),
	}, nil
}
