package ivy

import (
	"io"
	"sync"
)

// WorkflowContext serves two purposes:
// 1. Prevent users from calling functions outside llm.Suppervise.
// 2. Lets us store variables with the state of the graph.
type WorkflowContext struct {
}

type HandlerFunc[Req any] func(WorkflowContext, Req) error

var (
	mu       sync.RWMutex
	handlers = make(map[string]any)
)

func HandleFunc[Req any](route string, startNodeType NodeType[io.ReadCloser, Req], handler HandlerFunc[Req]) {
	mu.Lock()
	handlers[route] = handler
	mu.Unlock()
}

func NewNode[Req, Resp any](ctx WorkflowContext, _type NodeType[Req, Resp], req Req) Execution[Resp] {
	return nil
}

type NodeContext struct {
}

func NewAgenticNode[Req, Resp any](ctx WorkflowContext, _type NodeType[Req, Resp], fun func(NodeContext) (Req, error)) Execution[Resp] {
	return nil
}

type NodeType[Req, Resp any] func(WorkflowContext, Req) Definition[Req, Resp]

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type Definer[Req, Resp any] interface {
	Define(WorkflowContext, Req) Execution[Resp]
}

type Definition[Req, Resp any] struct {
	node Definer[Req, Resp]
}

func (n Definition[Req, Resp]) define(ctx WorkflowContext, req Req) Execution[Resp] {
	return n.node.Define(ctx, req)
}

func DefineNodeType[Req, Resp any](fun func(Req) Definer[Req, Resp]) NodeType[Req, Resp] {
	return func(_ WorkflowContext, req Req) Definition[Req, Resp] {
		return Definition[Req, Resp]{fun(req)}
	}
}

// Execution is a scheduled node in the graph.
// Call Get to get the result of the node.
type Execution[Resp any] interface {
	Get(NodeContext) (Resp, error)
}
