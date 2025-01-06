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

// Context serves two purposes:
// 1. Prevent users from calling functions outside llm.Suppervise.
// 2. Lets us store variables with the state of the graph.
type Context context.Context

type GraphID string

type graph struct {
	id  GraphID
	run func(Context) error
}

func NewGraph(id GraphID, run func(Context) error) *graph {
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
