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

type HandlerFunc[In any] func(WorkflowContext, In) (WorkflowOutput, error)

type WorkflowOutput NodeType[any, io.ReadCloser]

var (
	mu       sync.RWMutex
	handlers = make(map[string]any)
)

func Workflow[In any](route string, startNodeType NodeType[io.ReadCloser, In], handler HandlerFunc[In]) {
	mu.Lock()
	handlers[route] = handler
	mu.Unlock()
}

func StaticNode[In, Out any](ctx WorkflowContext, _type NodeType[In, Out], req In) Output[In, Out] {
	return nil
}

type NodeContext struct {
}

func Node[In, Out any](ctx WorkflowContext, _type NodeType[In, Out], fun func(NodeContext) (In, error)) Output[In, Out] {
	return nil
}

type NodeType[In, Out any] func(WorkflowContext, In) Definition[In, Out]

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type Definer[In, Out any] interface {
	Define(WorkflowContext, In) NodeResolver[Out]
}

type Definition[In, Out any] struct {
	node Definer[In, Out]
}

func (n Definition[In, Out]) define(ctx WorkflowContext, req In) NodeResolver[Out] {
	return n.node.Define(ctx, req)
}

func DefineNodeType[In, Out any](fun func(In) Definer[In, Out]) NodeType[In, Out] {
	return func(_ WorkflowContext, req In) Definition[In, Out] {
		return Definition[In, Out]{fun(req)}
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
