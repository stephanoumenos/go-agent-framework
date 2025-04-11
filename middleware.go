// ./middleware.go
package heart

import "context"

// MiddlewareExecutor defines an interface for node resolvers that represent middleware.
// Middleware often needs to interact with the context and input before potentially
// modifying the request and calling the next step (implicitly via its internal logic).
type MiddlewareExecutor[In, Out any] interface {
	// ExecuteMiddleware contains the primary logic of the middleware.
	// It receives the standard context and the input value.
	ExecuteMiddleware(ctx context.Context, in In) (Out, error)
}
