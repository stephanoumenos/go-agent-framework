package supervisor

import (
	"context"
	"fmt"
)

type dryRunContextKey struct{}

var dryRunCtxKey = &dryRunContextKey{}

type dryRunNodes struct{}

type dryRunContextValue struct {
	nodes []*dryRunNodes
}

func newDryRunContextValue() *dryRunContextValue {
	return &dryRunContextValue{}
}

type NodeDefiner[Req, Resp any] interface {
	// Define ...
	define(LLMContext, Req) Node[Resp]
	marshal(Req) ([]byte, error)
	unmarshal([]byte) (*Req, error)
}

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type nodeDefiner[Req, Resp any] interface {
	Define(LLMContext, Req) Node[Resp]
	Marshal(Req) ([]byte, error)
	Unmarshal([]byte) (*Req, error)
}

type nodeDefinition[Req, Resp any] struct {
	defineFn    func(LLMContext, Req) Node[Resp]
	marshalFn   func(Req) ([]byte, error)
	unmarshalFn func([]byte) (*Req, error)
}

func (n nodeDefinition[Req, Resp]) define(ctx LLMContext, req Req) Node[Resp] {
	return n.defineFn(ctx, req)
}

func (n nodeDefinition[Req, Resp]) marshal(req Req) ([]byte, error) {
	return n.marshalFn(req)
}

func (n nodeDefinition[Req, Resp]) unmarshal(b []byte) (*Req, error) {
	return n.unmarshalFn(b)
}

func DefineNodeDefiner[Req, Resp any](fn func(LLMContext, Req) nodeDefiner[Req, Resp]) func(LLMContext, Req) NodeDefiner[Req, Resp] {
	return func(ctx LLMContext, req Req) NodeDefiner[Req, Resp] {
		n := fn(ctx, req)
		return nodeDefinition[Req, Resp]{
			defineFn:    n.Define,
			marshalFn:   n.Marshal,
			unmarshalFn: n.Unmarshal,
		}
	}
}

// Node is a scheduled node in the graph.
// Call Get to get the result of the node.
type Node[Resp any] interface {
	Get(LLMContext) (Resp, error)
	marshal(Resp) ([]byte, error)
	unmarshal([]byte) (*Resp, error)
}

type LLMContext context.Context

func Define[Req, Resp any](ctx LLMContext, definer func(Req) NodeDefiner[Req, Resp]) Node[Resp] {
	return nil
	// return definer.define(ctx, req)
}

type GraphID string

type graph struct {
	id  GraphID
	run func(LLMContext) error
}

func NewGraph(id GraphID, run func(LLMContext) error) *graph {
	return &graph{id, run}
}

func Supervise(ctx context.Context, graphs ...*graph) error {
	graphIDs := make(map[GraphID]struct{}, len(graphs))
	for _, graph := range graphs {
		if _, ok := graphIDs[graph.id]; ok {
			return fmt.Errorf("duplicate graph id: %s", graph.id)
		}
		graphIDs[graph.id] = struct{}{}
	}

	// TODO: add dry run
	// dryRun(ctx, run)

	return nil
}

func dryRun(ctx context.Context, run func(LLMContext) error) {
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
