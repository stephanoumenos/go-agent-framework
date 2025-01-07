package golem

import (
	"context"
	"sync"
)

// Context serves two purposes:
// 1. Prevent users from calling functions outside llm.Suppervise.
// 2. Lets us store variables with the state of the graph.
type Context context.Context

type HandlerFunc func(Context) error

var (
	mu       sync.RWMutex
	handlers = make(map[string]HandlerFunc)
)

func HandleFunc(route string, handler HandlerFunc) {
	mu.Lock()
	handlers[route] = handler
	mu.Unlock()
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
