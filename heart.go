package heart

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
)

type WorkflowFactory[In, Out any] struct {
	resolver resolveWorkflow[In, Out]
}

func (w WorkflowFactory[In, Out]) New(in In) (Out, error) {
	return w.resolver(in)
}

type Context struct {
	nodeCount *atomic.Int64
	ctx       context.Context
}

type HandlerFunc[In, In2Out, Out any] func(In) Noder[In2Out, Out]
type resolveWorkflow[In, Out any] func(In) (Out, error)

type WorkflowInput[Req any] NodeDefinition[io.ReadCloser, Req]

func NewWorkflowFactory[In, In2Out, Out any](handler HandlerFunc[In, In2Out, Out]) WorkflowFactory[In, Out] {
	return WorkflowFactory[In, Out]{
		resolver: func(in In) (Out, error) {
			nCtx := Context{
				nodeCount: &atomic.Int64{},
				ctx:       context.Background(),
			}
			resultNode := handler(in)
			out, err := resultNode.Out(nCtx)
			return out, err
		},
	}
}

type NodeDefinition[In, Out any] interface {
	heart()
	Input(Outputer[In]) Noder[In, Out]
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

type node[In, Out any] struct {
	d     *definition[In, Out]
	in    Outputer[In]
	inOut InOut[In, Out]
	err   error
}

func (o *node[In, Out]) get(nc Context) {
	o.d.once.Do(func() {
		o.err = o.d.init()
		if o.err != nil {
			return
		}
		o.inOut.In, o.err = o.in.Out(nc)
		if o.err != nil {
			return
		}
		o.inOut.Out, o.err = o.d.resolver.Get(nc.ctx, o.inOut.In)
	})
}

func (o *node[In, Out]) In(nc Context) (In, error) {
	o.get(nc)
	return o.inOut.In, o.err
}

func (o *node[In, Out]) Out(nc Context) (Out, error) {
	o.get(nc)
	return o.inOut.Out, o.err
}

func (o *node[In, Out]) InOut(nc Context) (InOut[In, Out], error) {
	o.get(nc)
	return o.inOut, o.err
}

func (d *definition[In, Out]) Input(in Outputer[In]) Noder[In, Out] {
	return &node[In, Out]{
		d:     d,
		in:    in,
		inOut: InOut[In, Out]{},
	}
}

var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

func (d *definition[In, Out]) heart() {}

type NodeID string
type NodeTypeID string

func DefineNode[In, Out any](nodeID NodeID, resolver NodeResolver[In, Out]) NodeDefinition[In, Out] {
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

type Inputer[In any] interface {
	In(Context) (In, error)
}

type Outputer[Out any] interface {
	Out(Context) (Out, error)
}

type Noder[In, Out any] interface {
	Inputer[In]
	Outputer[Out]
	InOut(Context) (InOut[In, Out], error)
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
