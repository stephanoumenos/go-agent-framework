package heart

import "context"

// MiddlewareExecutor defines an interface for node resolvers that represent middleware.
// Middleware often needs the richer ResolverContext during execution to interact
// with other nodes (like the 'next' node it wraps).
type MiddlewareExecutor[In, Out any] interface {
	ExecuteMiddleware(ctx context.Context, rctx ResolverContext, in In) (Out, error)
}
