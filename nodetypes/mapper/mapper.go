package mapper

import (
	"ivy"
)

var (
	_ ivy.NodeResolver[any] = (*Mapper[any, any])(nil)
)

const nodeTypeID ivy.NodeTypeID = "mapper"

type Mapper[In, Out any] struct {
	mapper func(In) (Out, error)
	input  In
}

func (j *Mapper[In, Out]) Get(ivy.NodeContext) (Out, error) {
	return j.mapper(j.input)
}

type MapperDefinition[In, Out any] struct {
	fun   func(In) (Out, error)
	input In
}

func NodeType[In, Out any](mapper func(In) (Out, error)) ivy.NodeType[In, Out] {
	return ivy.DefineNodeType(nodeTypeID, func(req In) ivy.Definer[In, Out] {
		return &MapperDefinition[In, Out]{fun: mapper, input: req}
	})
}

func (j *MapperDefinition[In, Out]) Define(ctx ivy.WorkflowContext, input In) ivy.NodeResolver[Out] {
	return &Mapper[In, Out]{mapper: j.fun, input: input}
}
