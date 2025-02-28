package heart

type middleware[In, Out, NewOut any] func(Output[In, Out]) Output[In, NewOut]

func DefineMiddleware[In, Out, NewOut any](in Output[In, Out]) Output[In, NewOut] {
	return nil
}
