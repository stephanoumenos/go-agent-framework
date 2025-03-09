package heart

/* Stateful middleware */

/* Thin middleware */

type thinMiddlewareDefinition[In, Out, NewOut any] struct {
	middleware func(NoderGetter, In, NodeDefinition[In, Out]) (NewOut, error)
	next       NodeDefinition[In, Out]
}

type thinMiddleware[In, Out any] struct {
	inOut InOut[In, Out]
	get   func(NoderGetter)
	err   error
}

func (l *thinMiddleware[In, Out]) InOut(nc NoderGetter) (InOut[In, Out], error) {
	// TODO: only call get only
	// Not too necessary for lightMiddleware but might make a difference for high RPS
	l.get(nc)
	return l.inOut, l.err
}

func (l *thinMiddleware[In, Out]) In(nc InputerGetter) (In, error) {
	l.get(nc)
	return l.inOut.In, l.err
}

func (l *thinMiddleware[In, Out]) Out(nc OutputerGetter) (Out, error) {
	l.get(nc)
	return l.inOut.Out, l.err
}

func (m *thinMiddlewareDefinition[In, Out, NewOut]) heart() {}

func (m *thinMiddlewareDefinition[In, Out, NewOut]) Bind(in Outputer[In]) Noder[In, NewOut] {
	lm := &thinMiddleware[In, NewOut]{inOut: InOut[In, NewOut]{}}
	lm.get = func(nc NoderGetter) {
		lm.inOut.In, lm.err = in.Out(nc)
		if lm.err != nil {
			return
		}
		lm.inOut.Out, lm.err = m.middleware(nc, lm.inOut.In, m.next)
	}
	return lm
}

func DefineThinMiddleware[In, Out, NewOut any](
	middleware func(getter NoderGetter, in In, next NodeDefinition[In, Out]) (NewOut, error),
	next NodeDefinition[In, Out],
) NodeDefinition[In, NewOut] {
	return &thinMiddlewareDefinition[In, Out, NewOut]{middleware: middleware, next: next}
}
