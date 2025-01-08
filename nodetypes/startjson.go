package nodetypes

import (
	"golem/golem"
	"io"
)

type JSONDecoder[Resp any] struct {
	fun func(io.ReadCloser) (Resp, error)
	req io.ReadCloser
}

func (j *JSONDecoder[Resp]) Get(golem.WorkflowContext) (Resp, error) {
	return j.fun(j.req)
}

type JSONDecoderDefinition[Resp any] struct {
	fun func(io.ReadCloser) (Resp, error)
}

func NewJSONDecoderFromFunc[Resp any](fun func(io.ReadCloser) (Resp, error)) golem.TypeDefinition[io.ReadCloser, Resp] {
	return golem.DefineType(func(req io.ReadCloser) golem.Definer[io.ReadCloser, Resp] {
		return &JSONDecoderDefinition[Resp]{fun: fun}
	})
}

type JSONDecoderDefiner[Resp any] struct{}

func (j *JSONDecoderDefinition[Resp]) Define(ctx golem.WorkflowContext, req io.ReadCloser) golem.Execution[Resp] {
	return &JSONDecoder[Resp]{fun: j.fun, req: req}
}
