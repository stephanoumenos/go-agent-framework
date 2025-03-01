package heart

// type middleware[In, Out, NewIn, NewOut any] func(Output[In, Out]) Output[NewIn, NewOut]

/*
const middlewareNodeTypeID NodeTypeID = "middleware"

type middlewareInitializer struct{}

func (m *middlewareInitializer) ID() NodeTypeID {
	return middlewareNodeTypeID
}

type middleWare[In, Out, NewOut any] struct{ *middlewareInitializer }

func (m *middleWare[In, Out, NewOut]) Init() NodeInitializer {
	m.middlewareInitializer = &middlewareInitializer{}
	return m.middlewareInitializer
}

func (m *middleWare[In, Out, NewOut]) Get(ctx NodeContext, in In) (NewOut, error) {
	var n NewOut
	return n, nil
}
*/

type middlewareDefinition[In, Out, NewOut any] struct {
	middleware func(in In, next NodeDefinition[In, Out]) Output[In, NewOut]
	next       NodeDefinition[In, Out]
}

func (m *middlewareDefinition[In, Out, NewOut]) heart() {}
func (m *middlewareDefinition[In, Out, NewOut]) Input(in In) Output[In, NewOut] {
	return m.middleware(in, m.next)
}
func (m *middlewareDefinition[In, Out, NewOut]) FanIn(func(NodeContext) (In, error)) Output[In, NewOut] {
	return nil
}

func DefineMiddleware[In, Out, NewOut any](
	middleware func(in In, next NodeDefinition[In, Out]) Output[In, NewOut],
	next NodeDefinition[In, Out],
) NodeDefinition[In, NewOut] {
	return &middlewareDefinition[In, Out, NewOut]{middleware: middleware, next: next}
}
