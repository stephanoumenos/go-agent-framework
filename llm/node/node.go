package node

import "go-cot/llm"

func New[Req, Resp any](ctx llm.Context, _type Type[Req, Resp], req Req) Node[Resp] {
	return nil
	// return definer.define(ctx, req)
}

type TypeDefinition[Req, Resp any] func(llm.Context, Req) Definition[Req, Resp]

type Type[Req, Resp any] func(Req) TypeDefinition[Req, Resp]

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type Definer[Req, Resp any] interface {
	Define(llm.Context, Req) Node[Resp]
	Marshal(Req) ([]byte, error)
	Unmarshal([]byte) (*Req, error)
}

type Definition[Req, Resp any] struct {
	node Definer[Req, Resp]
}

func (n Definition[Req, Resp]) define(ctx llm.Context, req Req) Node[Resp] {
	return n.node.Define(ctx, req)
}

func (n Definition[Req, Resp]) marshal(req Req) ([]byte, error) {
	return n.node.Marshal(req)
}

func (n Definition[Req, Resp]) unmarshal(b []byte) (*Req, error) {
	return n.node.Unmarshal(b)
}

func DefineType[Req, Resp any](fn func(Req) Definer[Req, Resp]) TypeDefinition[Req, Resp] {
	return func(_ llm.Context, req Req) Definition[Req, Resp] {
		return Definition[Req, Resp]{fn(req)}
	}
}

// Node is a scheduled node in the graph.
// Call Get to get the result of the node.
type Node[Resp any] interface {
	Get(llm.Context) (Resp, error)
	Marshal(Resp) ([]byte, error)
	Unmarshal([]byte) (*Resp, error)
}
