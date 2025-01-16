package ivy

var (
	_ NodeResolver[any] = (*mapper[any, any])(nil)
)

const nodeTypeID NodeTypeID = "mapper"

type mapper[In, Out any] struct {
	mapper func(In) (Out, error)
	input  In
}

func (j *mapper[In, Out]) Get(NodeContext) (Out, error) {
	return j.mapper(j.input)
}

type mapperDefinition[In, Out any] struct {
	fun   func(In) (Out, error)
	input In
}

func mapperNodeType[In, Out any](mapper func(In) (Out, error)) NodeType[In, Out] {
	return DefineNodeType(nodeTypeID, func(req In) Definer[In, Out] {
		return &mapperDefinition[In, Out]{fun: mapper, input: req}
	})
}

func (j *mapperDefinition[In, Out]) Define() NodeResolver[Out] {
	return &mapper[In, Out]{mapper: j.fun, input: j.input}
}
