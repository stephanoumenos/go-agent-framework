package middleware

import "heart"

// Dummy struct just to be able to infer the type.
// It doesn't actually do anything.
type StructuredOutputStruct[T any] struct{}

func WithStructuredOutput[In, Out, SOut any](next heart.NodeDefinition[In, Out], structuredOutputStruct StructuredOutputStruct[SOut]) heart.NodeDefinition[In, SOut] {
	return heart.DefineMiddleware[In, Out, SOut](middleware, next)
}

func middleware[In, Out, SOut any](in In, next heart.NodeDefinition[In, Out]) heart.Output[In, SOut] {
	return nil
}
