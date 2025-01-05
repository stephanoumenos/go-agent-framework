package llm

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

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type NodeDefiner[Req, Resp any] interface {
	Define(LLMContext, Req) Node[Resp]
	Marshal(Req) ([]byte, error)
	Unmarshal([]byte) (*Req, error)
}

type NodeDefinition[Req, Resp any] struct {
	node NodeDefiner[Req, Resp]
}

func (n NodeDefinition[Req, Resp]) define(ctx LLMContext, req Req) Node[Resp] {
	return n.node.Define(ctx, req)
}

func (n NodeDefinition[Req, Resp]) marshal(req Req) ([]byte, error) {
	return n.node.Marshal(req)
}

func (n NodeDefinition[Req, Resp]) unmarshal(b []byte) (*Req, error) {
	return n.node.Unmarshal(b)
}

func DefineNode[Req, Resp any](fn func(Req) NodeDefiner[Req, Resp]) func(LLMContext, Req) NodeDefinition[Req, Resp] {
	return func(ctx LLMContext, req Req) NodeDefinition[Req, Resp] {
		return NodeDefinition[Req, Resp]{fn(req)}
	}
}

// Node is a scheduled node in the graph.
// Call Get to get the result of the node.
type Node[Resp any] interface {
	Get(LLMContext) (Resp, error)
	Marshal(Resp) ([]byte, error)
	Unmarshal([]byte) (*Resp, error)
}

type LLMContext context.Context

type NodeDefinerFn[Req, Resp any] func(Req) func(LLMContext, Req) NodeDefinition[Req, Resp]

func Define[Req, Resp any](ctx LLMContext, definer NodeDefinerFn[Req, Resp], req Req) Node[Resp] {
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
