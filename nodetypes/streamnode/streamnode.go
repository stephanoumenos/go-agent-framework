package streamnode

import (
	"context"
	"encoding/json"
	"ivy"
	"strings"
	"sync"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/ssestream"
)

var (
	_ ivy.NodeResolver[StreamNodeResult] = (*StreamNode)(nil)
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

var _ ivy.Definer[openai.CompletionNewParams, StreamNodeResult] = (*StreamNodeDefinition)(nil)

func (s *StreamNodeDefinition) Define(ivy.WorkflowContext, openai.CompletionNewParams) ivy.NodeResolver[StreamNodeResult] {
	return &StreamNode{}
}

func NodeType() ivy.NodeType[openai.CompletionNewParams, StreamNodeResult] {
	return ivy.DefineNodeType(func(req openai.CompletionNewParams) ivy.Definer[openai.CompletionNewParams, StreamNodeResult] {
		return &StreamNodeDefinition{}
	})
}

type StreamNodeResult struct {
	Params openai.CompletionNewParams
	Result string
}

func newStreamNode(ctx context.Context, client *openai.Client, params openai.CompletionNewParams) *StreamNode {
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

func (n *StreamNode) start(ctx ivy.NodeContext) {
	if n.started {
		return
	}
	// ivy.RegisterDryRunNode(ctx)
	n.stream = n.client.Completions.NewStreaming(context.Background(), n.params)
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

func (n *StreamNode) Get(ctx ivy.NodeContext) (StreamNodeResult, error) {
	n.once.Do(func() {
		n.start(ctx)
		for n.next() {
			n.response.WriteString(n.token())
		}
		n.err = n.stream.Err()
	})

	if n.err != nil {
		return StreamNodeResult{}, n.err
	}
	return StreamNodeResult{
		Params: n.params,
		Result: n.response.String(),
	}, nil
}

func (n *StreamNode) Marshal(resp StreamNodeResult) ([]byte, error) {
	return json.Marshal(resp)
}

func (n *StreamNode) Unmarshal(data []byte) (*StreamNodeResult, error) {
	var resp StreamNodeResult
	err := json.Unmarshal(data, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
