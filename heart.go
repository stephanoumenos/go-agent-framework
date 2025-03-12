package heart

import (
	"context"
	"sync"
)

type NodeDefinition[In, Out any] interface {
	heart()
	Bind(Outputer[In]) Noder[In, Out]
}

type into[Out any] struct{ out Out }

func (i *into[Out]) Out(OutputerGetter) (Out, error) {
	return i.out, nil
}

func Into[Out any](out Out) Outputer[Out] {
	return &into[Out]{out: out}
}

type NodeInitializer interface {
	ID() NodeTypeID
}

type definition[In, Out any] struct {
	id          NodeID
	initializer NodeInitializer
	resolver    NodeResolver[In, Out]
	once        *sync.Once
	ctx         Context
	idx         int64
}

func (d *definition[In, Out]) init() error {
	d.initializer = d.resolver.Init()
	return dependencyInject(d.initializer, d.initializer.ID())
}

func (d *definition[In, Out]) Bind(in Outputer[In]) Noder[In, Out] {
	n := &node[In, Out]{
		d:     d,
		in:    in,
		inOut: InOut[In, Out]{},
	}
	go func() {
		n.get(&getter{_child: &d.id})
	}()
	return n
}

var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

func (d *definition[In, Out]) heart() {}

type NodeID string
type NodeTypeID string

func DefineNode[In, Out any](ctx Context, nodeID NodeID, resolver NodeResolver[In, Out]) NodeDefinition[In, Out] {
	return &definition[In, Out]{id: nodeID, resolver: resolver, ctx: ctx, once: &sync.Once{}}
}

type NodeResolver[In, Out any] interface {
	Init() NodeInitializer
	Get(context.Context, In) (Out, error)
}

type InOut[In, Out any] struct {
	In  In
	Out Out
}

type InputerGetter interface {
	child() *NodeID
	heart()
}

type Inputer[In any] interface {
	In(InputerGetter) (In, error)
}

type OutputerGetter interface {
	child() *NodeID
	heart()
}

type Outputer[Out any] interface {
	Out(OutputerGetter) (Out, error)
}

type NoderGetter interface {
	heart()
	child() *NodeID
	InputerGetter // TODO: Maybe always have an input and an output for simplicity?
	OutputerGetter
}

type Noder[In, Out any] interface {
	Inputer[In]
	Outputer[Out]
	InOut(NoderGetter) (InOut[In, Out], error)
}

func FanIn[Out any](func(Context) (Out, error)) Outputer[Out] {
	// TODO: implement
	return nil
}

func Map[SIn ~[]In, In any, SOut ~[]Out, Out any](s Outputer[SIn], fun func(In) Outputer[Out]) Outputer[SOut] {
	return nil
}

// TODO: Change to error outputer
func ForEach[SIn ~[]In, In any](s Outputer[SIn], fun func(In) Outputer[struct{}]) Outputer[struct{}] {
	return nil
}

func Filter[SIn ~[]In, In any](s Outputer[SIn], fun func(In) Outputer[bool]) Outputer[SIn] {
	return nil
}

type transform[In, Out any] struct {
	in  Outputer[In]
	fun func(In) (Out, error)
}

func (t *transform[In, Out]) Out(nc OutputerGetter) (Out, error) {
	var out Out
	in, err := t.in.Out(nc)
	if err != nil {
		return out, err
	}
	return t.fun(in)
}

func Transform[In, Out any](in Outputer[In], fun func(In) (Out, error)) Outputer[Out] {
	return &transform[In, Out]{in: in, fun: fun}
}

func Root[Out any](nodeID NodeID, fun func(context.Context) (Out, error)) Outputer[Out] {
	return nil
}

func Node[In, Out any](nodeID NodeID, in Outputer[In], fun func(context.Context, In) (Out, error)) Noder[In, Out] {
	return nil
}

type connector[Out any] struct{}

func (c *connector[Out]) Connect(Outputer[Out]) {}

func UseConnector[Out any]() *connector[Out] { return nil }

type Maybe[T any] struct {
	value    T
	hasValue bool
}

func None[T any]() Maybe[T] {
	return Maybe[T]{hasValue: false}
}

func Some[T any](value T) Maybe[T] {
	return Maybe[T]{value: value, hasValue: true}
}

func MatchMaybe[In, Out any](
	in Outputer[Maybe[In]],
	some func(Context, In) Outputer[Out],
	none func(Context) Outputer[Out],
) Outputer[Out] {
	return nil
}

/*

/* Ideas
type Condition any

func If[Out any](condition Condition, do func(WorkflowContext)) Output[Condition, Out]    {}
func While[Out any](condition Condition, do func(WorkflowContext)) Output[Condition, Out] {}


func FanOut[In, Out any](node Output[In, Out], fun func(Context, In)) []Output[In, Out] {
	return nil
}

func ChainOfThought[In, Out, ROut any](n Output[In, Out], rewardModel NodeType[Out, ROut]) Output[In, Out] {
	return nil
}
*/
