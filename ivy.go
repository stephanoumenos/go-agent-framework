package ivy

import (
	"bytes"
	"encoding/json"
	"io"
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

type HandlerFunc[In, In2Out, Out any] func(WorkflowContext, In) Output[In2Out, Out]
type resolveWorkflow[In, Out any] func(In) (Out, error)

type WorkflowInput[Req any] NodeType[io.ReadCloser, Req]

func NewWorkflowFactory[In, In2Out, Out any](handler HandlerFunc[In, In2Out, Out]) WorkflowFactory[In, Out] {
	return WorkflowFactory[In, Out]{
		resolver: func(in In) (Out, error) {
			var nCtx NodeContext
			ctx := WorkflowContext{NodeContext: nCtx}
			resultNode := handler(ctx, in)
			result, err := resultNode.Get(nCtx)
			return result.Output, err
		},
	}
}

type NodeContext struct {
	m map[nodeKey]nodeState
}

type nodeKey struct {
	nodeID   NodeID
	nodeType NodeTypeID
}

type nodeStatus string

const (
	nodeStatusPending nodeStatus = "pending"
	nodeStatusRunning nodeStatus = "running"
	nodeStatusDone    nodeStatus = "done"
)

type nodeState []struct{}

func (nc *NodeContext) defineNode(n *nodeKey) {
}

type NodeType[In, Out any] interface {
	FanIn(func(NodeContext) (In, error)) Output[In, Out]
	Input(In) Output[In, Out]
}

type nodeType[In, Out any] func(WorkflowContext, In) definition[In, Out]

// Node implementations must implement this interface to be used in the supervisor.
// N.B.: it's unexported to prevent users from implementing it directly.
type Definer[In, Out any] interface {
	Define() NodeResolver[Out]
}

type definition[In, Out any] struct {
	id     NodeID
	fun    func(In) Definer[In, Out]
	typeID NodeTypeID
}

type outputFromFanin[In, Out any] struct {
	d   *definition[In, Out]
	fun func(NodeContext) (In, error)
}

func (o *outputFromFanin[In, Out]) Get(nc NodeContext) (Result[In, Out], error) {
	var (
		r   Result[In, Out]
		err error
	)
	r.Input, err = o.fun(nc)
	r.Output, err = o.d.define(nc, r.Input).Get(nc)
	return r, err
}

type outputFromInput[In, Out any] struct {
	d  *definition[In, Out]
	in In
}

func (o *outputFromInput[In, Out]) Get(nc NodeContext) (Result[In, Out], error) {
	r := Result[In, Out]{Input: o.in}
	var err error
	r.Output, err = o.d.define(nc, o.in).Get(nc)
	return r, err
}

func (d *definition[In, Out]) FanIn(fun func(NodeContext) (In, error)) Output[In, Out] {
	return &outputFromFanin[In, Out]{
		d:   d,
		fun: fun,
	}
}

func (d *definition[In, Out]) Input(in In) Output[In, Out] {
	return &outputFromInput[In, Out]{
		d:  d,
		in: in,
	}
}

type nodeResolver[Out any] struct {
	ctx                  NodeContext
	nodeTypeNodeResolver NodeResolver[Out]
}

func (n *nodeResolver[Out]) Get(nc NodeContext) (Out, error) {
	return n.nodeTypeNodeResolver.Get(nc)
}

func (n definition[In, Out]) define(ctx NodeContext, req In) NodeResolver[Out] {
	ctx.defineNode(&nodeKey{
		nodeID:   n.id,
		nodeType: n.typeID,
	})
	return &nodeResolver[Out]{nodeTypeNodeResolver: n.fun(req).Define(), ctx: ctx}
}

type NodeID string
type NodeTypeID string

func DefineNodeType[In, Out any](nodeID NodeID, nodeTypeID NodeTypeID, fun func(In) Definer[In, Out]) NodeType[In, Out] {
	return &definition[In, Out]{id: nodeID, fun: fun, typeID: nodeTypeID}
}

type NodeResolver[Out any] interface {
	Get(NodeContext) (Out, error)
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

func Request[In any](fun func(io.ReadCloser) (In, error)) NodeType[io.ReadCloser, In] {
	return mapperNodeType("request", fun)
}

func RequestFromJSON[In any]() NodeType[io.ReadCloser, In] {
	return mapperNodeType("request", func(req io.ReadCloser) (In, error) {
		var in In
		if err := json.NewDecoder(req).Decode(&in); err != nil {
			return in, err
		}
		return in, nil
	})
}

func Response[In, Out any](finalNode Output[In, Out], fun func(Out) (io.ReadCloser, error)) WorkflowOutput[Out] {
	return Transform("response", finalNode, fun)
}

func ResponseToJSON[In, Out any](finalNode Output[In, Out]) WorkflowOutput[Out] {
	return mapperNodeType("response", func(out Out) (io.ReadCloser, error) {
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(out); err != nil {
			return nil, err
		}
		return io.NopCloser(buf), nil
	}).FanIn(func(nc NodeContext) (Out, error) {
		response, err := finalNode.Get(nc)
		return response.Output, err
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
