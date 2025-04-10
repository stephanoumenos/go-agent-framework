package heart

import "context"

// MiddlewareExecutor defines an interface for node resolvers that represent middleware.
type MiddlewareExecutor[In, Out any] interface {
	ExecuteMiddleware(ctx context.Context, in In) (Out, error)
}
