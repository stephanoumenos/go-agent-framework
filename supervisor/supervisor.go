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
	Define(LLMContext, Req) Node[Resp]
	Marshal(Req) ([]byte, error)
	Unmarshal([]byte) (*Req, error)
}

// Node is a scheduled node in the graph.
// Call Get to get the result of the node.
type Node[Resp any] interface {
	Get(LLMContext) (Resp, error)
	Marshal(Resp) ([]byte, error)
	Unmarshal([]byte) (*Resp, error)
}

type LLMContext context.Context

func Define[Req, Resp any](ctx LLMContext, definer NodeDefiner[Req, Resp], req Req) Node[Resp] {
	return definer.Define(ctx, req)
}

func Supervise(ctx context.Context, run func(LLMContext) error) error {
	dryRun(ctx, run)
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

func userCode() {
	ctx := context.Background()
	Supervise(ctx, func(ctx LLMContext) error {
		return nil
	})
}
