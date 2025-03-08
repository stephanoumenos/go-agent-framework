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

func (i *into[Out]) Out(Context) (Out, error) {
	return i.out, nil
}

func Into[Out any](out Out) Outputer[Out] {
	return &into[Out]{out}
}

type NodeInitializer interface {
	ID() NodeTypeID
}

type definition[In, Out any] struct {
	id          NodeID
	initializer NodeInitializer
	resolver    NodeResolver[In, Out]
	once        *sync.Once
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
	return n
}

var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

func (d *definition[In, Out]) heart() {}

type NodeID string
type NodeTypeID string

func DefineNode[In, Out any](ctx Context, nodeID NodeID, resolver NodeResolver[In, Out]) NodeDefinition[In, Out] {
	return &definition[In, Out]{id: nodeID, resolver: resolver, once: &sync.Once{}}
}

type NodeResolver[In, Out any] interface {
	Init() NodeInitializer
	Get(context.Context, In) (Out, error)
}

type InOut[In, Out any] struct {
	In  In
	Out Out
}

type InputerGetter[In any] interface {
	heart()
}

type Inputer[In any] interface {
	In(InputerGetter[In]) (In, error)
}

type OutputerGetter[Out any] interface {
	heart()
}

type Outputer[Out any] interface {
	Out(OutputerGetter[Out]) (Out, error)
}

type NoderGetter[In, Out any] interface {
	heart()
	InputerGetter[In]
	OutputerGetter[Out]
}

type Noder[In, Out any] interface {
	Inputer[In]
	Outputer[Out]
	InOut(NoderGetter[In, Out]) (InOut[In, Out], error)
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

func Transform[In, Out any](Outputer[In], func(In) (Out, error)) Outputer[Out] {
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
func Transform[In, Out, TOut any](nodeID NodeID, node Output[In, Out], fun func(Out) (TOut, error)) Output[Out, TOut] {
	return mapperNodeType(nodeID, func(in Out) (TOut, error) {
		return fun(in)
	}).FanIn(func(nc Context) (Out, error) {
		out, err := node.Get(nc)
		return out.Output, err
	})
}
*/

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
