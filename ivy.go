package ivy

import (
	"fmt"
	"io"
	"sync"
)

// WorkflowContext serves two purposes:
// 1. Prevent users from calling functions outside llm.Suppervise.
// 2. Lets us store variables with the state of the graph.
type WorkflowContext interface {
}

type HandlerFunc[In, OutputReq any] func(WorkflowContext, In) (WorkflowOutput[OutputReq], error)

type WorkflowOutput[Req any] Output[Req, io.ReadCloser]

var (
	mu       sync.RWMutex
	handlers = make(map[string]any)
)

type userHandler struct {
	startNodeType any
	handler       any
}

func Workflow[In, OutputReq any](route string, startNodeType NodeType[io.ReadCloser, In], handler HandlerFunc[In, OutputReq]) {
	mu.Lock()
	var resolve resolveWorkflow = func(req io.ReadCloser) (io.ReadCloser, error) {
		mapped, err := startNodeType(nil, req).definer.Define().Get(NodeContext{})
		if err != nil {
			return nil, err
		}
		out, err := handler(nil, mapped)
		if err != nil {
			return nil, err
		}
		result, err := out.Get(NodeContext{})
		if err != nil {
			return nil, err
		}
		return result.Output, nil
	}
	handlers[route] = resolve
	mu.Unlock()
}

type resolveWorkflow func(io.ReadCloser) (io.ReadCloser, error)

func RunWorkflow(route string, req io.ReadCloser) (io.ReadCloser, error) {
	mu.RLock()
	routeHandler, ok := handlers[route]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("handler not found")
	}
	handler, ok := routeHandler.(resolveWorkflow)
	if !ok {
		return nil, fmt.Errorf("error getting handler")
	}
	return handler(req)
}

type staticNode[In, Out any] struct {
	result   Result[In, Out]
	resolver NodeResolver[Out]
	err      error
	once     sync.Once
}

func (n *staticNode[In, Out]) Get(ctx NodeContext) (Result[In, Out], error) {
	n.once.Do(func() {
		n.result.Output, n.err = n.resolver.Get(ctx)
	})
	return n.result, n.err
}

var (
	_ Output[any, any] = (*staticNode[any, any])(nil)
)

func StaticNode[In, Out any](ctx WorkflowContext, _type NodeType[In, Out], in In) Output[In, Out] {
	return &staticNode[In, Out]{
		result: Result[In, Out]{
			Input: in,
		},
		resolver: _type(ctx, in).definer.Define(),
	}
}

type NodeContext struct {
}

type node[In, Out any] struct {
	result   Result[In, Out]
	resolver func(NodeContext)
	err      error
	once     sync.Once
}

func (n *node[In, Out]) Get(ctx NodeContext) (Result[In, Out], error) {
	n.once.Do(func() {
		n.resolver(ctx)
	})
	return n.result, n.err
}

func Node[In, Out any](ctx WorkflowContext, _type NodeType[In, Out], fun func(NodeContext) (In, error)) Output[In, Out] {
	var n node[In, Out]
	n.resolver = func(nc NodeContext) {
		n.result.Input, n.err = fun(nc)
		if n.err != nil {
			return
		}
		n.result.Output, n.err = _type(ctx, n.result.Input).definer.Define().Get(nc)
	}
	return &n
}

type NodeType[In, Out any] func(WorkflowContext, In) Definition[In, Out]

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type Definer[In, Out any] interface {
	Define() NodeResolver[Out]
}

type Definition[In, Out any] struct {
	definer Definer[In, Out]
	typeID  NodeTypeID
}

func (n Definition[In, Out]) define(ctx WorkflowContext, req In) NodeResolver[Out] {
	return n.definer.Define()
}

type NodeTypeID string

func DefineNodeType[In, Out any](id NodeTypeID, fun func(In) Definer[In, Out]) NodeType[In, Out] {
	return func(_ WorkflowContext, req In) Definition[In, Out] {
		return Definition[In, Out]{definer: fun(req), typeID: id}
	}
}

type NodeResolver[Out any] interface {
	Get(NodeContext) (Out, error)
}

type Result[In, Out any] struct {
	Input  In
	Output Out
}

type Output[In, Out any] interface {
	Get(NodeContext) (Result[In, Out], error)
}
