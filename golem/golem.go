package golem

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

func HandleFunc[Req any](route string, startNodeType Type[io.ReadCloser, Req], handler HandlerFunc[Req]) {
	mu.Lock()
	handlers[route] = handler
	mu.Unlock()
}

func NewNode[Req, Resp any](ctx WorkflowContext, _type Type[Req, Resp], req Req) Execution[Resp] {
	return nil
	// return definer.define(ctx, req)
}

type NodeContext struct {
}

func NewAgenticNode[Req, Resp any](ctx WorkflowContext, _type Type[Req, Resp], fun func(WorkflowContext) (Req, error)) Execution[Resp] {
	return nil
}

/*
func Start[Req any](ctx Context, _type Type[[]byte, Req], fun func(Req) (Resp, error)) Execution[Resp] {
	return nil
}
*/

type TypeDefinition[Req, Resp any] func(WorkflowContext, Req) Definition[Req, Resp]

type StartTypeDefinition[Req any] func(WorkflowContext, io.ReadCloser) Definition[io.ReadCloser, Req]

type Type[Req, Resp any] func(Req) TypeDefinition[Req, Resp]

// type StartType[Req any] func(func(io.ReadCloser) (Req, error)) TypeDefinition[io.ReadCloser, Req]

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

/*
func (n Definition[Req, Resp]) marshal(req Req) ([]byte, error) {
	return n.node.Marshal(req)
}

func (n Definition[Req, Resp]) unmarshal(b []byte) (*Req, error) {
	return n.node.Unmarshal(b)
}
*/

func DefineType[Req, Resp any](fun func(Req) Definer[Req, Resp]) TypeDefinition[Req, Resp] {
	return func(_ WorkflowContext, req Req) Definition[Req, Resp] {
		return Definition[Req, Resp]{fun(req)}
	}
}

func DefineStartType[Req any](fun func(io.ReadCloser) Definer[io.ReadCloser, Req]) StartTypeDefinition[Req] {
	return func(_ WorkflowContext, req io.ReadCloser) Definition[io.ReadCloser, Req] {
		return Definition[io.ReadCloser, Req]{fun(req)}
	}
}

// Execution is a scheduled node in the graph.
// Call Get to get the result of the node.
type Execution[Resp any] interface {
	Get(WorkflowContext) (Resp, error)
}

type Persistable[Resp any] interface {
	Marshal(Resp) ([]byte, error)
	Unmarshal([]byte) (*Resp, error)
}

/*
type dryRunContextKey struct{}

var dryRunCtxKey = &dryRunContextKey{}

type dryRunNodes struct{}

type dryRunContextValue struct {
	nodes []*dryRunNodes
}

func newDryRunContextValue() *dryRunContextValue {
	return &dryRunContextValue{}
}

func dryRun(ctx context.Context, run func(Context) error) {
	ctxValue := newDryRunContextValue()
	ctx = context.WithValue(ctx, dryRunCtxKey, ctxValue)
	run(ctx)
}

func getDryRun(ctx context.Context) (val *dryRunContextValue, ok bool) {
	val, ok = ctx.Value(dryRunCtxKey).(*dryRunContextValue)
	return
}

func RegisterDryRunNode(ctx context.Context) (isDryRun bool) {
	val, ok := getDryRun(ctx)
	if !ok {
		return false
	}
	val.nodes = append(val.nodes, &dryRunNodes{
		// TODO: add node definition
	})
	return true
}

func ErrNoContextValue[T any](key T) error { return fmt.Errorf("no value for key %v+", key) }

func WithSideEffects(ctx context.Context, fun func()) {
	if _, isDryRun := getDryRun(ctx); isDryRun {
		return
	}
	fun()
}

*/
