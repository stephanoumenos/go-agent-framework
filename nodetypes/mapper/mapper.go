package mapper

import (
	"ivy"
)

var (
	_ ivy.NodeResolver[any] = (*Mapper[any, any])(nil)
)

type Mapper[In, Out any] struct {
	fun   func(In) (Out, error)
	input In
}

func (j *Mapper[In, Out]) Get(ivy.NodeContext) (Out, error) {
	return j.fun(j.input)
}

type MapperDefinition[In, Out any] struct {
	fun   func(In) (Out, error)
	input In
}

func NodeType[In, Out any](fun func(In) (Out, error)) ivy.NodeType[In, Out] {
	return ivy.DefineNodeType(func(req In) ivy.Definer[In, Out] {
		return &MapperDefinition[In, Out]{fun: fun, input: req}
	})
}

func (j *MapperDefinition[In, Out]) Define(ctx ivy.WorkflowContext, input In) ivy.NodeResolver[Out] {
	return &Mapper[In, Out]{fun: j.fun, input: input}
}
