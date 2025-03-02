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

type HandlerFunc[In, In2Out, Out any] func(In) Output[In2Out, Out]
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
			result, err := resultNode.Get(nCtx)
			return result.Output, err
		},
	}
}

type NodeDefinition[In, Out any] interface {
	heart()
	Input(In) Output[In, Out]
	FanIn(func(Context) (In, error)) Output[In, Out]
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

func (d *definition[In, Out]) init() {
	d.initializer = d.resolver.Init()
	dependencyInject(d.initializer, d.initializer.ID())
}

type outputFromFanIn[In, Out any] struct {
	d   *definition[In, Out]
	fun func(Context) (In, error)
	Result[In, Out]
	err error
}

func (o *outputFromFanIn[In, Out]) Get(nc Context) (Result[In, Out], error) {
	o.d.once.Do(func() {
		o.d.init()
		o.Result.Input, o.err = o.fun(nc)
		if o.err != nil {
			return
		}
		o.Result.Output, o.err = o.d.resolver.Get(nc.ctx, o.Result.Input)
	})
	return o.Result, o.err
}

type outputFromInput[In, Out any] struct {
	d *definition[In, Out]
	Result[In, Out]
	err error
}

func (o *outputFromInput[In, Out]) Get(nc Context) (Result[In, Out], error) {
	o.d.once.Do(func() {
		o.d.init()
		o.Result.Output, o.err = o.d.resolver.Get(nc.ctx, o.Result.Input)
	})
	return o.Result, o.err
}

func (d *definition[In, Out]) FanIn(fun func(Context) (In, error)) Output[In, Out] {
	return &outputFromFanIn[In, Out]{
		d:   d,
		fun: fun,
	}
}

func (d *definition[In, Out]) Input(in In) Output[In, Out] {
	return &outputFromInput[In, Out]{
		d:      d,
		Result: Result[In, Out]{Input: in},
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

type Result[In, Out any] struct {
	Input  In
	Output Out
}

type Output[In, Out any] interface {
	Get(Context) (Result[In, Out], error)
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
