package requestmapper

import (
	"golem/golem"
	"io"
)

var (
	_ golem.Execution[any] = (*RequestMapper[any])(nil)
)

type RequestMapper[Req any] struct {
	fun func(io.ReadCloser) (Req, error)
	req io.ReadCloser
}

func (j *RequestMapper[Resp]) Get(golem.NodeContext) (Resp, error) {
	return j.fun(j.req)
}

type RequestMapperDefinition[Req any] struct {
	fun func(io.ReadCloser) (Req, error)
	req io.ReadCloser
}

func NodeType[Req any](fun func(io.ReadCloser) (Req, error)) golem.NodeType[io.ReadCloser, Req] {
	return golem.DefineNodeType(func(req io.ReadCloser) golem.Definer[io.ReadCloser, Req] {
		return &RequestMapperDefinition[Req]{fun: fun, req: req}
	})
}

func (j *RequestMapperDefinition[Resp]) Define(ctx golem.WorkflowContext, req io.ReadCloser) golem.Execution[Resp] {
	return &RequestMapper[Resp]{fun: j.fun, req: req}
}
