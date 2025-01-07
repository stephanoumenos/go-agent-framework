package node

import (
	"context"
	"golem/golem"
)

func New[Req, Resp any](ctx golem.Context, _type Type[Req, Resp], req Req) Execution[Resp] {
	return nil
	// return definer.define(ctx, req)
}

type Context context.Context

func NewDynamic[Req, Resp any](ctx golem.Context, _type Type[Req, Resp], fun func(Context) (Req, error)) Execution[Resp] {
	return nil
}

type TypeDefinition[Req, Resp any] func(golem.Context, Req) Definition[Req, Resp]

type Type[Req, Resp any] func(Req) TypeDefinition[Req, Resp]

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type Definer[Req, Resp any] interface {
	Define(golem.Context, Req) Execution[Resp]
	Marshal(Req) ([]byte, error)
	Unmarshal([]byte) (*Req, error)
}

type Definition[Req, Resp any] struct {
	node Definer[Req, Resp]
}

func (n Definition[Req, Resp]) define(ctx golem.Context, req Req) Execution[Resp] {
	return n.node.Define(ctx, req)
}

func (n Definition[Req, Resp]) marshal(req Req) ([]byte, error) {
	return n.node.Marshal(req)
}

func (n Definition[Req, Resp]) unmarshal(b []byte) (*Req, error) {
	return n.node.Unmarshal(b)
}

func DefineType[Req, Resp any](fun func(Req) Definer[Req, Resp]) TypeDefinition[Req, Resp] {
	return func(_ golem.Context, req Req) Definition[Req, Resp] {
		return Definition[Req, Resp]{fun(req)}
	}
}

// Execution is a scheduled node in the graph.
// Call Get to get the result of the node.
type Execution[Resp any] interface {
	Get(golem.Context) (Resp, error)
	Marshal(Resp) ([]byte, error)
	Unmarshal([]byte) (*Resp, error)
}
