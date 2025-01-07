package streamnode

import (
	"context"
	"encoding/json"
	"golem/golem"
	"golem/golem/node"
	"strings"
	"sync"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/ssestream"
)

var _ node.Execution[StreamNodeResult] = (*StreamNode)(nil)

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

var _ node.Definer[openai.CompletionNewParams, StreamNodeResult] = (*StreamNodeDefinition)(nil)

func (s *StreamNodeDefinition) Define(golem.Context, openai.CompletionNewParams) node.Execution[StreamNodeResult] {
	return &StreamNode{}
}

func (s *StreamNodeDefinition) Marshal(req openai.CompletionNewParams) ([]byte, error) {
	return json.Marshal(req)
}

func (s *StreamNodeDefinition) Unmarshal(data []byte) (*openai.CompletionNewParams, error) {
	var req *openai.CompletionNewParams
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return req, nil
}

func NewStreamNodeDefinition(ctx golem.Context, params openai.CompletionNewParams) StreamNodeDefinition {
	panic("implement me")
}

func NewStreamNode2(openai.CompletionNewParams) node.TypeDefinition[openai.CompletionNewParams, StreamNodeResult] {
	return node.DefineType(func(req openai.CompletionNewParams) node.Definer[openai.CompletionNewParams, StreamNodeResult] {
		return &StreamNodeDefinition{}
	})
}

var streamNodeType node.Type[openai.CompletionNewParams, StreamNodeResult] = NewStreamNode2

func Type() node.Type[openai.CompletionNewParams, StreamNodeResult] {
	return streamNodeType
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
	// golem.RegisterDryRunNode(ctx)
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

func (n *StreamNode) Get(ctx golem.Context) (StreamNodeResult, error) {
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
		params: n.params,
		result: n.response.String(),
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
