package supervisor

import "context"

type contextKey struct{}

var ctxKey = &contextKey{}

func Supervise(ctx context.Context, run func(context.Context) ImplicitGraph) {
	dryRun(ctx, run)
}

type NodeDefinition struct{}
type NodeExecution struct{}

type ImplicitGraph struct {
	Definition []NodeDefinition
	Execution  []NodeExecution
}

func dryRun(ctx context.Context, run func(context.Context) ImplicitGraph) {
	ctx = context.WithValue(ctx, ctxKey, true)
	implicitGraph := run(ctx)
}

func IsDryRun(ctx context.Context) bool {
	return ctx.Value(ctxKey).(bool)
}

func userCode() {
	ctx := context.Background()
	Supervise(ctx, func(ctx context.Context) ImplicitGraph {

		return ImplicitGraph{
			Definition: []NodeDefinition{},
			Execution:  []NodeExecution{},
		}
	})
}
