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

func Supervise(ctx context.Context, run func(context.Context)) error {
	dryRun(ctx, run)
	return nil
}

/*
Idea...
type NodeDefinition struct{}
type NodeExecution struct{}
*/

func dryRun(ctx context.Context, run func(context.Context)) {
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
	Supervise(ctx, func(ctx context.Context) {
	})
}
