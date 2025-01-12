package mapper

import (
	"ivy"
)

var (
	_ ivy.NodeResolver[any] = (*mapper[any, any])(nil)
)

const nodeTypeID ivy.NodeTypeID = "mapper"

type mapper[In, Out any] struct {
	mapper func(In) (Out, error)
	input  In
}

func (j *mapper[In, Out]) Get(ivy.NodeContext) (Out, error) {
	return j.mapper(j.input)
}

type mapperDefinition[In, Out any] struct {
	fun   func(In) (Out, error)
	input In
}

func NodeType[In, Out any](mapper func(In) (Out, error)) ivy.NodeType[In, Out] {
	return ivy.DefineNodeType(nodeTypeID, func(req In) ivy.Definer[In, Out] {
		return &mapperDefinition[In, Out]{fun: mapper, input: req}
	})
}

func (j *mapperDefinition[In, Out]) Define() ivy.NodeResolver[Out] {
	return &mapper[In, Out]{mapper: j.fun, input: j.input}
}
