package heart

import (
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

// WorkflowContext serves two purposes:
// 1. Prevent users from calling functions outside llm.Suppervise.
// 2. Lets us store variables with the state of the graph.
type WorkflowContext struct {
	NodeContext NodeContext
}

type NodeContext struct {
	nodeCount *atomic.Int64
	m         map[nodeKey]nodeState
}

type nodeKey struct {
	nodeID   NodeID
	nodeType NodeTypeID
}

type nodeStatus string

type HandlerFunc[In, In2Out, Out any] func(WorkflowContext, In) Output[In2Out, Out]
type resolveWorkflow[In, Out any] func(In) (Out, error)

type WorkflowInput[Req any] NodeBuilder[io.ReadCloser, Req]

func NewWorkflowFactory[In, In2Out, Out any](handler HandlerFunc[In, In2Out, Out]) WorkflowFactory[In, Out] {
	return WorkflowFactory[In, Out]{
		resolver: func(in In) (Out, error) {
			nCtx := NodeContext{
				nodeCount: &atomic.Int64{},
			}
			ctx := WorkflowContext{NodeContext: nCtx}
			resultNode := handler(ctx, in)
			result, err := resultNode.Get(nCtx)
			return result.Output, err
		},
	}
}

const (
	nodeStatusPending nodeStatus = "pending"
	nodeStatusRunning nodeStatus = "running"
	nodeStatusDone    nodeStatus = "done"
)

type nodeState []struct{}

func (nc *NodeContext) defineNode(n *nodeKey) {
}

type NodeBuilder[In, Out any] interface {
	heart()
	FanIn(func(NodeContext) (In, error)) Output[In, Out]
	Input(In) Output[In, Out]
}

type nodeType[In, Out any] func(WorkflowContext, In) definition[In, Out]

type NodeInitializer interface {
	ID() NodeTypeID
}

type definition[In, Out any] struct {
	id       NodeID
	resolver NodeResolver[In, Out]
	once     *sync.Once
	idx      int64
}

type outputFromFanIn[In, Out any] struct {
	d   *definition[In, Out]
	fun func(NodeContext) (In, error)
	Result[In, Out]
	err error
}

func (o *outputFromFanIn[In, Out]) Get(nc NodeContext) (Result[In, Out], error) {
	o.d.once.Do(func() {
		o.Result.Input, o.err = o.fun(nc)
		if o.err != nil {
			return
		}
		o.Result.Output, o.err = o.d.resolver.Get(nc, o.Result.Input)
	})
	return o.Result, o.err
}

type outputFromInput[In, Out any] struct {
	d *definition[In, Out]
	Result[In, Out]
	err error
}

func (o *outputFromInput[In, Out]) Get(nc NodeContext) (Result[In, Out], error) {
	o.d.once.Do(func() {
		o.Result.Output, o.err = o.d.resolver.Get(nc, o.Result.Input)
	})
	return o.Result, o.err
}

func (d *definition[In, Out]) FanIn(fun func(NodeContext) (In, error)) Output[In, Out] {
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

var _ NodeBuilder[any, any] = (*definition[any, any])(nil)

func (d *definition[In, Out]) heart() {}

type NodeID string
type NodeTypeID string

func DefineNodeBuilder[In, Out any](ctx WorkflowContext, nodeID NodeID, resolver NodeResolver[In, Out]) NodeBuilder[In, Out] {
	idx := ctx.NodeContext.nodeCount.Add(1)
	return &definition[In, Out]{id: nodeID, resolver: resolver, once: &sync.Once{}, idx: idx}
}

type NodeResolver[In, Out any] interface {
	Init() NodeInitializer
	Get(NodeContext, In) (Out, error)
}

type Result[In, Out any] struct {
	Input  In
	Output Out
}

type Output[In, Out any] interface {
	Get(NodeContext) (Result[In, Out], error)
}

func Transform[In, Out, TOut any](nodeID NodeID, node Output[In, Out], fun func(Out) (TOut, error)) Output[Out, TOut] {
	return mapperNodeType(nodeID, func(in Out) (TOut, error) {
		return fun(in)
	}).FanIn(func(nc NodeContext) (Out, error) {
		out, err := node.Get(nc)
		return out.Output, err
	})
}

/* Ideas
type Condition any

func If[Out any](condition Condition, do func(WorkflowContext)) Output[Condition, Out]    {}
func While[Out any](condition Condition, do func(WorkflowContext)) Output[Condition, Out] {}


func FanOut[In, Out any](node Output[In, Out], fun func(NodeContext, In)) []Output[In, Out] {
	return nil
}

func ChainOfThought[In, Out, ROut any](n Output[In, Out], rewardModel NodeType[Out, ROut]) Output[In, Out] {
	return nil
}
*/
