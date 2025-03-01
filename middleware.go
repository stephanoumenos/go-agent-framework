package heart

type thinMiddlewareDefinition[In, Out, NewOut any] struct {
	middleware func(NodeContext, In, NodeDefinition[In, Out]) (NewOut, error)
	next       NodeDefinition[In, Out]
}

type thinMiddleware[In, Out any] struct {
	Result[In, Out]
	get func(NodeContext)
	err error
}

func (l *thinMiddleware[In, Out]) Get(nc NodeContext) (Result[In, Out], error) {
	// TODO: only call get only
	// Not too necessary for lightMiddleware but might make a difference for high RPS
	l.get(nc)
	return l.Result, l.err
}

func (m *thinMiddlewareDefinition[In, Out, NewOut]) heart() {}

func (m *thinMiddlewareDefinition[In, Out, NewOut]) Input(in In) Output[In, NewOut] {
	lm := &thinMiddleware[In, NewOut]{Result: Result[In, NewOut]{Input: in}}
	lm.get = func(nc NodeContext) {
		lm.Output, lm.err = m.middleware(nc, lm.Input, m.next)
	}
	return lm
}

func (m *thinMiddlewareDefinition[In, Out, NewOut]) FanIn(fun func(NodeContext) (In, error)) Output[In, NewOut] {
	lm := &thinMiddleware[In, NewOut]{}
	lm.get = func(nc NodeContext) {
		lm.Input, lm.err = fun(nc)
		if lm.err != nil {
			return
		}
		lm.Output, lm.err = m.middleware(nc, lm.Input, m.next)
	}
	return lm
}

func DefineThinMiddleware[In, Out, NewOut any](
	middleware func(ctx NodeContext, in In, next NodeDefinition[In, Out]) (NewOut, error),
	next NodeDefinition[In, Out],
) NodeDefinition[In, NewOut] {
	return &thinMiddlewareDefinition[In, Out, NewOut]{middleware: middleware, next: next}
}
