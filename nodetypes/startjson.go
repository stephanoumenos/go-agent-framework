package nodetypes

import (
	"golem/golem"
	"io"
)

type JSONDecoder[Req any] struct {
	fun func(io.ReadCloser) (Req, error)
	req io.ReadCloser
}

func (j *JSONDecoder[Resp]) Get(golem.WorkflowContext) (Resp, error) {
	return j.fun(j.req)
}

type JSONDecoderDefinition[Req any] struct {
	fun func(io.ReadCloser) (Req, error)
}

func NewJSONDecoderFromFunc[Req any](fun func(io.ReadCloser) (Req, error)) golem.TypeDefinition[io.ReadCloser, Req] {
	return golem.DefineType(func(req io.ReadCloser) golem.Definer[io.ReadCloser, Req] {
		return &JSONDecoderDefinition[Req]{fun: fun}
	})
}

func jsonDecoderNodeType[Req any]() golem.Type[io.ReadCloser, Req] {
	return NewJSONDecoderFromFunc[Req]
}

// var jsonDecoderNodeType = golem.Type[io.ReadCloser]

func Type[Req any]() golem.Type[io.ReadCloser, Req] {
	return NewJSONDecoderFromFunc[Req]
}

type JSONDecoderDefiner[Resp any] struct{}

func (j *JSONDecoderDefinition[Resp]) Define(ctx golem.WorkflowContext, req io.ReadCloser) golem.Execution[Resp] {
	return &JSONDecoder[Resp]{fun: j.fun, req: req}
}
