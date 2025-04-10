package heart

import (
	"context"
	"fmt"
	"path"
	"sync"
)

// NodeID represents a segment name within a NodePath. It's the local identifier
// provided when defining a node within a specific context (like a workflow handler or FanIn/If branch).
type NodeID string

// NodePath represents the unique hierarchical identifier for a node instance within a workflow execution.
// It always starts with "/" and uses "/" as a separator.
// Example: "/", "/openai-call", "/fanin-step/optimist-branch/llm-call"
type NodePath string

// JoinPath combines a base path and a node ID segment to form a new NodePath.
// It ensures the path uses "/" as a separator and handles potential double slashes correctly.
func JoinPath(base NodePath, id NodeID) NodePath {
	// Use path.Join as it handles cleaning (like removing //) and uses '/'
	// It also preserves the leading '/' if the base path starts with one.
	return NodePath(path.Join(string(base), string(id)))
}

type NodeDefinition[In, Out any] interface {
	heart()
	Bind(Outputer[In]) Noder[In, Out]
	// TODO: Add a method to get the NodePath? (Maybe later)
	// Path() NodePath
}

type into[Out any] struct {
	out Out
	err error
}

func (i *into[Out]) Out(ResolverContext) (Out, error) {
	return i.out, i.err
}

func Into[Out any](out Out) Outputer[Out] {
	return &into[Out]{out: out}
}

func IntoError[Out any](err error) Outputer[Out] {
	return &into[Out]{err: err}
}

type NodeInitializer interface {
	ID() NodeTypeID // Keep this as NodeTypeID (type of node), not instance ID
}

type definition[In, Out any] struct {
	// id NodeID // Replaced by path
	path        NodePath   // Unique hierarchical path for this node instance
	nodeTypeID  NodeTypeID // Added: Store the node type ID separately
	initializer NodeInitializer
	resolver    NodeResolver[In, Out]
	once        *sync.Once
	ctx         Context // The context used to define this node
	// TODO: Store NodeOptions here later
}

func (d *definition[In, Out]) init() error {
	// Initialize resolver and get the initializer instance
	d.initializer = d.resolver.Init()
	if d.initializer == nil {
		// Attempt to derive NodeTypeId from the resolver itself if Init returns nil
		// This might be fragile, consider requiring Init() to always return non-nil
		// or find another way to get NodeTypeID reliably.
		// For now, let's assume Init always works or add better error handling.
		// panic(fmt.Sprintf("NodeResolver.Init() returned nil for node path %s", d.path)) // Option: Panic
		return fmt.Errorf("NodeResolver.Init() returned nil for node path %s", d.path) // Option: Error
	}

	// Store the NodeTypeID from the initializer
	d.nodeTypeID = d.initializer.ID()

	// Perform dependency injection using the NodeTypeID
	return dependencyInject(d.initializer, d.nodeTypeID) // Inject based on TYPE id
}

func (d *definition[In, Out]) Bind(in Outputer[In]) Noder[In, Out] {
	n := &node[In, Out]{
		d:     d,
		in:    in,
		inOut: InOut[In, Out]{},
	}
	// Schedule the node's execution when Bind is called
	// NOTE: The actual execution logic in node.go will need updates later
	// to use d.path for persistence.
	go func() {
		n.get(&getter{}) // Placeholder context
	}()
	return n
}

var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

func (d *definition[In, Out]) heart() {}

// NodeTypeID represents the *type* of a node (e.g., "openai:chat", "transform").
// Used for dependency injection and potentially metrics/logging.
type NodeTypeID string

// DefineNode creates a node definition.
// It now calculates the unique NodePath based on the context's BasePath and the user-provided nodeID.
func DefineNode[In, Out any](
	ctx Context, // Context now contains BasePath
	nodeID NodeID, // User-provided local ID for this node segment
	resolver NodeResolver[In, Out],
	// TODO: Add options ...NodeOption later
) NodeDefinition[In, Out] {

	// Construct the full hierarchical path for this node instance.
	fullPath := JoinPath(ctx.BasePath, nodeID) // Use the new JoinPath function

	return &definition[In, Out]{
		path:     fullPath, // Store the full path
		resolver: resolver,
		ctx:      ctx, // Store context used for definition (contains base path etc.)
		once:     &sync.Once{},
		// nodeTypeID and initializer are set during d.init()
	}
}

type NodeResolver[In, Out any] interface {
	Init() NodeInitializer // Must return the initializer which provides the NodeTypeID
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
	// TODO: Add methods if needed for context passing (e.g., NodePath(), ParentPath()?)
}

// Implement ResolverContext for the internal getter struct used in Workflow.New
type getter struct {
	// Potential future fields: path NodePath?
}

func (g getter) heart() {}

type Noder[In, Out any] interface {
	Inputer[In]
	Outputer[Out]
	InOut(ResolverContext) (InOut[In, Out], error)
}

// --- FanIn Implementation ---
// NOTE: FanIn, Transform, If etc. will need significant updates in later steps
// to correctly manage context (BasePath) and paths for their internal nodes.
// These implementations are temporarily broken by the path changes.

type fanInResolver[Out any] struct {
	fun func(ResolverContext) Outputer[Out]
}

type fanInInitializer struct{}

func (f fanInInitializer) ID() NodeTypeID {
	return "fanIn" // Type ID
}

func (f *fanInResolver[Out]) Init() NodeInitializer {
	return fanInInitializer{}
}

func (f *fanInResolver[Out]) Get(ctx context.Context, _ struct{}) (Out, error) {
	// Placeholder context
	return f.fun(getter{}).Out(getter{})
}

// FanIn - Needs update later for path handling
func FanIn[Out any](ctx Context, nodeID NodeID, fun func(ResolverContext) Outputer[Out]) Outputer[Out] {
	resolver := &fanInResolver[Out]{fun: fun}
	// This DefineNode call now correctly generates a path like /base/nodeID
	// But the function `fun` likely defines more nodes internally, and *their*
	// context/paths aren't handled yet.
	return DefineNode(ctx, nodeID, resolver).Bind(Into(struct{}{}))
}

// --- Transform Implementation ---
// NOTE: Temporarily broken by path changes. Needs update later.

type transformResolver[In, Out any] struct {
	fun func(context.Context, In) (Out, error)
}

// genericNodeInitializer - Reusable initializer providing a NodeTypeID
type genericNodeInitializer struct {
	id NodeTypeID
}

func (g genericNodeInitializer) ID() NodeTypeID {
	return g.id
}

func (tr *transformResolver[In, Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "transform"} // Type ID
}

func (tr *transformResolver[In, Out]) Get(ctx context.Context, in In) (Out, error) {
	return tr.fun(ctx, in)
}

// Transform - Needs update later for path handling
func Transform[In, Out any](ctx Context, nodeID NodeID, in Outputer[In], fun func(ctx context.Context, in In) (Out, error)) Outputer[Out] {
	resolver := &transformResolver[In, Out]{
		fun: fun,
	}
	// DefineNode generates correct path for the transform node itself.
	nodeDefinition := DefineNode(ctx, nodeID, resolver)
	return nodeDefinition.Bind(in)
}

// --- Connector (Placeholder) ---

type connector[Out any] struct{}

func (c *connector[Out]) Connect(Outputer[Out]) {}

func UseConnector[Out any]() *connector[Out] { return nil } // Placeholder

// --- If Implementation ---
// NOTE: Temporarily broken by path changes. Needs update later.

type _if[Out any] struct {
	ctx        Context // Store the context for creating sub-nodes
	_condition Outputer[bool]
	ifTrue     func(Context) Outputer[Out]
	_else      func(Context) Outputer[Out]
}

func (i *_if[Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "if"} // Type ID
}

func (i *_if[Out]) Get(ctx context.Context, condition bool) (Out, error) {
	var result Outputer[Out]
	// PROBLEM: The context passed to ifTrue/_else needs the *correct BasePath*
	// which should be the path of the If node itself (e.g., /base/if-node-id).
	// The current i.ctx is the *parent's* context. This needs fixing later.
	currentBasePath := i.ctx.BasePath // Incorrect - Just an example of the issue

	// Create derived contexts (conceptually - needs proper implementation)
	trueCtx := i.ctx                                         // TODO: Fix this - should derive from If node's path
	trueCtx.BasePath = JoinPath(currentBasePath, "ifTrue")   // Example derivation
	falseCtx := i.ctx                                        // TODO: Fix this
	falseCtx.BasePath = JoinPath(currentBasePath, "ifFalse") // Example derivation

	if condition {
		result = i.ifTrue(trueCtx) // Pass potentially derived context
	} else {
		if i._else == nil {
			var zero Out
			return zero, nil
		}
		result = i._else(falseCtx) // Pass potentially derived context
	}
	// Placeholder context
	return result.Out(&getter{})
}

// If - Needs update later for path and context handling within branches.
func If[Out any](
	ctx Context,
	nodeID NodeID,
	condition Outputer[bool],
	ifTrue func(Context) Outputer[Out],
	_else func(Context) Outputer[Out],
) Outputer[Out] {
	// This context (ctx) is the parent context.
	ifResolver := &_if[Out]{
		ctx:        ctx, // Stores parent context - needs care when used in Get
		_condition: condition,
		ifTrue:     ifTrue,
		_else:      _else,
	}
	// DefineNode generates correct path for the If node itself.
	nodeDefinition := DefineNode(ctx, nodeID, ifResolver)
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
