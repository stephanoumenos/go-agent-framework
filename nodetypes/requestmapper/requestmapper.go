package requestmapper

import (
	"io"
	"ivy"
)

var (
	_ ivy.Execution[any] = (*RequestMapper[any])(nil)
)

type RequestMapper[Req any] struct {
	fun func(io.ReadCloser) (Req, error)
	req io.ReadCloser
}

func (j *RequestMapper[Resp]) Get(ivy.NodeContext) (Resp, error) {
	return j.fun(j.req)
}

type RequestMapperDefinition[Req any] struct {
	fun func(io.ReadCloser) (Req, error)
	req io.ReadCloser
}

func NodeType[Req any](fun func(io.ReadCloser) (Req, error)) ivy.NodeType[io.ReadCloser, Req] {
	return ivy.DefineNodeType(func(req io.ReadCloser) ivy.Definer[io.ReadCloser, Req] {
		return &RequestMapperDefinition[Req]{fun: fun, req: req}
	})
}

func (j *RequestMapperDefinition[Resp]) Define(ctx ivy.WorkflowContext, req io.ReadCloser) ivy.Execution[Resp] {
	return &RequestMapper[Resp]{fun: j.fun, req: req}
}
