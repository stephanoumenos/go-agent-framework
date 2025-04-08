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

func (i *into[Out]) Out(ResolverContext) (Out, error) {
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

type Inputer[In any] interface {
	In(ResolverContext) (In, error)
}

type Outputer[Out any] interface {
	Out(ResolverContext) (Out, error)
}

type ResolverContext interface {
	heart()
}

type Noder[In, Out any] interface {
	Inputer[In]
	Outputer[Out]
	InOut(ResolverContext) (InOut[In, Out], error)
}

type fanInResolver[Out any] struct {
	fun func(ResolverContext) (Out, error)
}

type fanInInitializer struct{}

func (f fanInInitializer) ID() NodeTypeID {
	return "fanIn"
}

func (f *fanInResolver[Out]) Init() NodeInitializer {
	return fanInInitializer{}
}

func (f *fanInResolver[Out]) Get(context.Context, struct{}) (Out, error) {
	return f.fun(getter{})
}

func FanIn[Out any](ctx Context, nodeID NodeID, fun func(ResolverContext) (Out, error)) Outputer[Out] {
	return DefineNode(ctx, nodeID, &fanInResolver[Out]{fun: fun}).Bind(Into(struct{}{}))
}

type transform[In, Out any] struct {
	ctx Context
	in  Outputer[In]
	fun func(context.Context, In) (Out, error)
}

func (t *transform[In, Out]) Out(nc ResolverContext) (Out, error) {
	var out Out
	in, err := t.in.Out(nc)
	if err != nil {
		return out, err
	}
	return t.fun(t.ctx.ctx, in)
}

func Transform[In, Out any](ctx Context, nodeID NodeID, in Outputer[In], fun func(ctx context.Context, in In) (Out, error)) Outputer[Out] {
	// TODO: Define node here
	return &transform[In, Out]{ctx: ctx, in: in, fun: fun}
}

type connector[Out any] struct{}

func (c *connector[Out]) Connect(Outputer[Out]) {}

func UseConnector[Out any]() *connector[Out] { return nil }

type _if[Out any] struct {
	ctx        Context
	_condition Outputer[bool]
	ifTrue     func(Context) Outputer[Out]
	_else      func(Context) Outputer[Out]
}

type genericNodeInitializer struct {
	_id NodeTypeID
}

func (g genericNodeInitializer) ID() NodeTypeID {
	return g._id
}

func (i *_if[Out]) Init() NodeInitializer {
	return genericNodeInitializer{NodeTypeID("if")}
}

func (i *_if[Out]) Get(ctx context.Context, condition bool) (Out, error) {
	if condition {
		return i.ifTrue(i.ctx).Out(&getter{})
	} else {
		return i._else(i.ctx).Out(&getter{})
	}
}

func If[Out any](
	ctx Context,
	nodeID NodeID,
	condition Outputer[bool],
	ifTrue func(Context) Outputer[Out],
	_else func(Context) Outputer[Out],
) Outputer[Out] {
	// TODO: Define node here
	__if := &_if[Out]{ctx: ctx, _condition: condition, ifTrue: ifTrue, _else: _else}
	return DefineNode(ctx, nodeID, __if).Bind(condition)
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
