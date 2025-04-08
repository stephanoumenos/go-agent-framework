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
	// Schedule the node's execution when Bind is called
	go func() {
		n.get(&getter{})
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
	// TODO: Add methods if needed for context passing, e.g., child node ID
}

// Implement ResolverContext for the internal getter struct used in Workflow.New
// (Assuming getter is defined elsewhere or implicitly used)
// type getter struct { /* ... */ }
// func (g getter) heart() {}

type Noder[In, Out any] interface {
	Inputer[In]
	Outputer[Out]
	InOut(ResolverContext) (InOut[In, Out], error)
}

// --- FanIn Implementation ---

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

func (f *fanInResolver[Out]) Get(ctx context.Context, _ struct{}) (Out, error) {
	// FanIn's logic depends on the ResolverContext, not the standard context.Context or input
	// We might need a way to pass the correct ResolverContext here.
	// Using a placeholder getter for now.
	return f.fun(getter{}) // Potential issue: Where does getter come from?
}

func FanIn[Out any](ctx Context, nodeID NodeID, fun func(ResolverContext) (Out, error)) Outputer[Out] {
	resolver := &fanInResolver[Out]{fun: fun}
	// FanIn doesn't conceptually have an input other than the context, so we bind struct{}{}
	return DefineNode(ctx, nodeID, resolver).Bind(Into(struct{}{}))
}

// --- Transform Implementation ---

// transformResolver implements NodeResolver for the Transform function.
type transformResolver[In, Out any] struct {
	fun func(context.Context, In) (Out, error)
	// No need to store ctx here, Get receives its own context.
}

// Init returns a generic initializer for the transform node.
func (tr *transformResolver[In, Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "transform"} // Using generic initializer
}

// Get executes the transformation function provided to Transform.
func (tr *transformResolver[In, Out]) Get(ctx context.Context, in In) (Out, error) {
	// Call the user-provided transformation function
	return tr.fun(ctx, in)
}

// Transform defines a node that applies a function to the output of another node.
// It uses DefineNode internally to create a standard heart node.
func Transform[In, Out any](ctx Context, nodeID NodeID, in Outputer[In], fun func(ctx context.Context, in In) (Out, error)) Outputer[Out] {
	// Create the specific resolver for this transformation
	resolver := &transformResolver[In, Out]{
		fun: fun,
	}

	// Define the node using the standard mechanism
	nodeDefinition := DefineNode(ctx, nodeID, resolver)

	// Bind the input Outputer to the newly defined node
	// The result is a Noder, which also implements Outputer[Out]
	return nodeDefinition.Bind(in)
}

// --- Connector (Placeholder) ---

type connector[Out any] struct{}

func (c *connector[Out]) Connect(Outputer[Out]) {}

func UseConnector[Out any]() *connector[Out] { return nil } // Placeholder

// --- If Implementation ---

type _if[Out any] struct {
	ctx        Context
	_condition Outputer[bool] // Keep the condition Outputer to bind later
	ifTrue     func(Context) Outputer[Out]
	_else      func(Context) Outputer[Out]
}

// genericNodeInitializer can be reused for If, Transform, etc.
type genericNodeInitializer struct {
	id NodeTypeID
}

func (g genericNodeInitializer) ID() NodeTypeID {
	return g.id
}

func (i *_if[Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "if"}
}

// Get for _if resolves the condition and executes the corresponding branch.
func (i *_if[Out]) Get(ctx context.Context, condition bool) (Out, error) {
	// The 'condition' input to Get is the *resolved* boolean value
	var result Outputer[Out]
	if condition {
		result = i.ifTrue(i.ctx)
	} else {
		// Handle the case where _else might be nil (optional else)
		if i._else == nil {
			var zero Out
			// Or return a specific error? Depends on desired semantics.
			return zero, nil // Return zero value if no else branch
		}
		result = i._else(i.ctx)
	}
	// We need the correct ResolverContext to call Out on the chosen branch.
	// Using placeholder getter for now.
	return result.Out(&getter{}) // Potential issue: Where does getter come from?
}

// If defines a conditional node. It uses DefineNode internally.
func If[Out any](
	ctx Context,
	nodeID NodeID,
	condition Outputer[bool],
	ifTrue func(Context) Outputer[Out],
	_else func(Context) Outputer[Out], // Can be nil for if without else
) Outputer[Out] {
	// Create the resolver instance for the If logic
	ifResolver := &_if[Out]{
		ctx:        ctx,
		_condition: condition, // Store the Outputer[bool]
		ifTrue:     ifTrue,
		_else:      _else,
	}

	// Define the node. The input type is bool (the resolved condition).
	nodeDefinition := DefineNode(ctx, nodeID, ifResolver)

	// Bind the condition Outputer[bool] as the input to this If node.
	return nodeDefinition.Bind(condition)
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
