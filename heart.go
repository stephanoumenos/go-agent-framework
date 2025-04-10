// ./heart.go
package heart

import (
	"context"
	"fmt"
	"path"
	// Keep sync for node's execOnce
	// Keep time for node state
	// io needed for WorkflowInput type alias, keep it
	// "github.com/google/uuid" needed for WorkflowUUID, keep it
	// "heart/store" needed for node persistence, keep it
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
	Bind(Outputer[In]) NodeResult[In, Out]
	// TODO: Add a method to get the NodePath? (Maybe later)
	// Path() NodePath
}

type into[Out any] struct {
	out Out
	err error
}

// Done returns a channel that is always closed, suitable for immediately resolved inputs.
func (i *into[Out]) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// Out returns the pre-defined value or error immediately.
func (i *into[Out]) Out() (Out, error) {
	return i.out, i.err
}

// In always returns a zero value and nil error for `into`, as it represents a source.
func (i *into[Out]) In() (struct{}, error) { // Input type is irrelevant, use struct{}
	var zero struct{}
	return zero, nil
}

// InOut returns the pre-defined value/error and a zero input.
func (i *into[Out]) InOut() (InOut[struct{}, Out], error) { // Input type is irrelevant
	var zero struct{}
	return InOut[struct{}, Out]{In: zero, Out: i.out}, i.err
}

// Into wraps a concrete value, making it an immediately resolved Outputer.
func Into[Out any](out Out) Outputer[Out] {
	return &into[Out]{out: out}
}

// IntoError wraps an error, making it an immediately resolved Outputer that returns the error.
func IntoError[Out any](err error) Outputer[Out] {
	return &into[Out]{err: err}
}

// Ensure into implements Noder (specifically Outputer part is most relevant)
var _ NodeResult[struct{}, any] = (*into[any])(nil)

type NodeInitializer interface {
	ID() NodeTypeID // Keep this as NodeTypeID (type of node), not instance ID
}

type definition[In, Out any] struct {
	// id NodeID // Replaced by path
	path        NodePath   // Unique hierarchical path for this node instance
	nodeTypeID  NodeTypeID // Added: Store the node type ID separately
	initializer NodeInitializer
	resolver    NodeResolver[In, Out]
	// once *sync.Once // Removed: Execution is per-instance now
	ctx Context // The context used to define this node
	// TODO: Store NodeOptions here later
}

func (d *definition[In, Out]) init() error {
	// Initialize resolver and get the initializer instance
	if d.resolver == nil {
		// This can happen if the definition was manually constructed incorrectly
		return fmt.Errorf("internal error: node resolver is nil for node path %s", d.path)
	}
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
	if d.nodeTypeID == "" {
		return fmt.Errorf("NodeInitializer.ID() returned empty NodeTypeID for node path %s", d.path)
	}

	// Perform dependency injection using the NodeTypeID
	// DependencyInject might modify the d.initializer instance.
	return dependencyInject(d.initializer, d.nodeTypeID) // Inject based on TYPE id
}

// Bind creates a runtime node instance (Noder) and immediately starts its execution in a goroutine.
func (d *definition[In, Out]) Bind(in Outputer[In]) NodeResult[In, Out] {
	n := &node[In, Out]{
		d:      d,
		in:     in,
		inOut:  InOut[In, Out]{},
		doneCh: make(chan struct{}), // Initialize the done channel
		// execOnce is zero-value initialized
	}

	// Eager execution: Start the node's get() method in a goroutine.
	go n.get()

	return n // Return the Noder instance immediately.
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
		// once: &sync.Once{}, // Removed
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
	// In returns the resolved input of the node, blocking if necessary.
	In() (In, error)
}

type Outputer[Out any] interface {
	// Out returns the output of the node, blocking if necessary.
	Out() (Out, error)
}

type NodeResult[In, Out any] interface {
	Inputer[In]
	Outputer[Out]
	// InOut returns both input and output, blocking if necessary.
	InOut() (InOut[In, Out], error)
	// Done returns a channel that is closed when the node's execution is complete (successfully or with an error).
	Done() <-chan struct{}
}

// --- NewNode Implementation ---

// newNodeResolver is a resolver for nodes created using NewNode.
// It wraps a user function that returns an Outputer, allowing dynamic sub-graph construction.
type newNodeResolver[Out any] struct {
	fun    func(ctx Context) Outputer[Out] // Takes heart Context, returns Outputer
	defCtx Context                         // The context captured when NewNode was defined
}

// genericNodeInitializer - Reusable initializer providing a NodeTypeID
// Moved definition here as it's used by multiple resolvers.
type genericNodeInitializer struct {
	id NodeTypeID
}

func (g genericNodeInitializer) ID() NodeTypeID {
	return g.id
}

func (r *newNodeResolver[Out]) Init() NodeInitializer {
	// Provide a distinct NodeTypeID for nodes created this way
	return genericNodeInitializer{id: "system:newNode"}
}

// Get executes the wrapped function `fun` to get an Outputer, then calls Out() on it.
// The input `_ struct{}` is ignored.
func (r *newNodeResolver[Out]) Get(stdCtx context.Context, _ struct{}) (Out, error) {
	// Create the derived heart.Context for the function call.
	// The BasePath for this context is the path of the NewNode itself,
	// which is correctly set in r.defCtx.BasePath.
	nodeCtx := r.defCtx // Start with a copy of the definition context
	// Update the standard context within the heart.Context to the one provided at runtime.
	nodeCtx.ctx = stdCtx

	// Call the user's function to get the Outputer for the actual result.
	// This function might define and bind a sub-graph using nodeCtx.
	subOutputer := r.fun(nodeCtx) // Now returns Outputer[Out]

	// Check if the user function returned a nil Outputer, which is invalid.
	if subOutputer == nil {
		var zero Out
		// This indicates a programming error by the user of NewNode.
		// We cannot easily get the NodePath here, so use a generic error.
		// TODO: Consider logging this more visibly.
		return zero, fmt.Errorf("internal execution error: NewNode function returned a nil Outputer")
	}

	// Block and wait for the result from the Outputer returned by the user function.
	// This retrieves the actual value (or error) from the sub-graph/computation defined within 'fun'.
	return subOutputer.Out()
}

// Ensure newNodeResolver implements NodeResolver
var _ NodeResolver[struct{}, any] = (*newNodeResolver[any])(nil)

// NewNode creates a NodeResult that wraps an arbitrary Go function (`fun`).
// The function `fun` is responsible for defining the logic (potentially a sub-graph)
// and returning an Outputer representing the final result of that logic.
// Execution is eager: `NewNode` defines and binds the wrapper node immediately.
// The provided `ctx` is used to define the wrapper node itself (determining its path).
// The `fun` receives a derived `Context` with the correct `BasePath`, allowing it to
// correctly define sub-nodes relative to the NewNode wrapper.
func NewNode[Out any](
	ctx Context, // Context for defining this NewNode wrapper
	nodeID NodeID, // ID for this NewNode wrapper relative to ctx.BasePath
	fun func(ctx Context) Outputer[Out], // The function that returns the final Outputer
) NodeResult[struct{}, Out] { // Returns a Noder representing the execution of the wrapper

	// Create the resolver, capturing the user function and the definition context.
	// The definition context (ctx) provides the BasePath for the context passed to 'fun'.
	resolver := &newNodeResolver[Out]{
		fun:    fun,
		defCtx: ctx,
	}

	// Define the wrapper node using the standard mechanism. This calculates and stores
	// the correct hierarchical path (e.g., /parent/nodeID) on the resulting definition struct.
	// The input type is struct{} as NewNode doesn't take explicit heart input via Bind.
	nodeDefinition := DefineNode[struct{}, Out](ctx, nodeID, resolver)

	// Bind the wrapper node with a dummy input (Into(struct{}{})).
	// This triggers the eager execution of the wrapper node's get() method in a goroutine.
	// The get() method will call the newNodeResolver's Get(), which calls the user's function 'fun',
	// gets the returned Outputer, and calls .Out() on it to get the final result.
	noder := nodeDefinition.Bind(Into(struct{}{}))

	// Return the Noder representing the wrapper node immediately.
	// Calling .Out() on this noder will block until the Outputer returned by 'fun' resolves.
	return noder
}

// --- FanIn Removed ---
// Use NewNode with manual synchronization (calling .Out() on dependencies) instead.
// See examples/fanin/main.go

// --- Transform Implementation ---

type transformResolver[In, Out any] struct {
	fun func(ctx context.Context, in In) (Out, error) // Standard context is ok if no sub-nodes defined
}

func (tr *transformResolver[In, Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "system:transform"} // Type ID
}

func (tr *transformResolver[In, Out]) Get(ctx context.Context, in In) (Out, error) {
	// Simple transformation just calls the user function.
	return tr.fun(ctx, in)
}

// Ensure transformResolver implements NodeResolver
var _ NodeResolver[any, any] = (*transformResolver[any, any])(nil)

// Transform creates a node that applies a simple function to an input.
// The function receives standard context.Context. If the transformation needs to
// define sub-nodes within the heart graph or return a node, use NewNode instead.
func Transform[In, Out any](
	ctx Context, // Heart context for defining the Transform node
	nodeID NodeID, // ID for this transform node
	in Outputer[In], // The input noder
	fun func(ctx context.Context, in In) (Out, error), // The transformation function returning value/error
) Outputer[Out] { // Returns the output noder
	resolver := &transformResolver[In, Out]{
		fun: fun,
	}
	// DefineNode generates correct path for the transform node itself.
	nodeDefinition := DefineNode(ctx, nodeID, resolver)
	// Bind triggers eager execution, passing the input noder 'in'.
	return nodeDefinition.Bind(in)
}

// --- Connector (Placeholder) ---

type connector[Out any] struct{}

func (c *connector[Out]) Connect(Outputer[Out]) {}

func UseConnector[Out any]() *connector[Out] { return nil } // Placeholder

// --- If Implementation ---
// NOTE: Needs significant update later for path and context handling within branches.
// This implementation is broken. Use NewNode with standard Go if/else logic instead.

type _if[Out any] struct {
	ctx        Context // The context used to *define* the If node
	_condition Outputer[bool]
	ifTrue     func(Context) Outputer[Out] // Function expects a derived context, returns Outputer
	_else      func(Context) Outputer[Out] // Function expects a derived context, returns Outputer
	defPath    NodePath                    // Store the definition path to derive branch paths
}

func (i *_if[Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "system:if"} // Type ID
}

// Get for _if is complex because it needs to dynamically execute one branch
// and ensure nodes within that branch get the correct context/path.
// The current eager model makes this tricky to implement correctly within Get.
// This needs a redesign for the eager model.
func (i *_if[Out]) Get(stdCtx context.Context, condition bool) (Out, error) {
	// PROBLEM: This Get logic runs *after* Bind has already returned.
	// We need to evaluate the condition and then *dynamically define and bind*
	// the nodes within the chosen branch *using the correct context*.
	// This requires a different approach than simply calling ifTrue/else here.
	// This implementation is fundamentally incompatible with the eager model without redesign.

	fmt.Printf("WARNING: heart.If is currently broken in the eager execution model and needs redesign. Use heart.NewNode with Go's if/else instead.\n")

	var result Outputer[Out]
	var branchCtx Context

	// --- Incorrect Path Derivation Logic (Illustrative of the need) ---
	// This logic needs to happen dynamically or be structured differently.
	trueCtx := i.ctx
	trueCtx.BasePath = JoinPath(i.defPath, "ifTrue") // Derive path for true branch
	trueCtx.ctx = stdCtx                             // Update standard context

	falseCtx := i.ctx
	falseCtx.BasePath = JoinPath(i.defPath, "ifFalse") // Derive path for false branch
	falseCtx.ctx = stdCtx                              // Update standard context
	// --- End Incorrect Logic ---

	if condition {
		branchCtx = trueCtx // Use derived context (which is incorrectly derived here)
		// Check if function is nil before calling
		if i.ifTrue == nil {
			return *new(Out), fmt.Errorf("internal execution error: If node (true branch) has nil function")
		}
		result = i.ifTrue(branchCtx)
	} else {
		branchCtx = falseCtx // Use derived context (incorrectly derived)
		if i._else == nil {
			var zero Out
			// Represent the "no-op" else branch as an immediately resolved Outputer
			result = Into(zero)
		} else {
			result = i._else(branchCtx)
		}
	}

	// Ensure the chosen branch function actually returned an Outputer
	if result == nil {
		branch := "false"
		if condition {
			branch = "true"
		}
		return *new(Out), fmt.Errorf("internal execution error: If node (%s branch) function returned a nil Outputer", branch)
	}

	// Calling Out() here will trigger the execution of the selected branch's Outputer
	// This part is conceptually okay, but getting the correct `result` Outputer
	// with proper path context requires the redesign mentioned above.
	return result.Out()
}

// Ensure _if implements NodeResolver
var _ NodeResolver[bool, any] = (*_if[any])(nil)

// If - Needs update later for path and context handling within branches. BROKEN.
// Use heart.NewNode with Go's native if/else constructs for conditional logic.
func If[Out any](
	ctx Context,
	nodeID NodeID,
	condition Outputer[bool],
	ifTrue func(Context) Outputer[Out], // Must not be nil
	_else func(Context) Outputer[Out], // Can be nil (results in zero value for Out)
) Outputer[Out] {
	// Define the If node itself.
	ifPath := JoinPath(ctx.BasePath, nodeID)
	fmt.Printf("WARNING: Defining heart.If node '%s'. This construct is currently broken; use heart.NewNode with Go's if/else instead.\n", nodeID)

	if ifTrue == nil {
		// It doesn't make sense to have an If node without a true branch.
		// We could panic or return an error node immediately.
		// Returning an error node is safer.
		err := fmt.Errorf("programming error: heart.If called with nil ifTrue function for node '%s'", nodeID)
		return IntoError[Out](err)
	}

	ifResolver := &_if[Out]{
		ctx:        ctx, // Store definition context
		_condition: condition,
		ifTrue:     ifTrue,
		_else:      _else,
		defPath:    ifPath, // Store the calculated path
	}

	// DefineNode generates correct path for the If node itself.
	nodeDefinition := DefineNode(ctx, nodeID, ifResolver)

	// Bind triggers eager execution of the _if resolver's Get method.
	// The Get method currently doesn't handle branches correctly in the eager model.
	return nodeDefinition.Bind(condition)
}

/* Ideas (Deferred)
type Condition any

// func If[Out any](condition Condition, do func(WorkflowContext)) Output[Condition, Out]    {} // Redundant with above?
func While[Out any](condition Condition, do func(WorkflowContext)) Output[Condition, Out] {}


func FanOut[In, Out any](node Output[In, Out], fun func(Context, In)) []Output[In, Out] {
    return nil
}

func ChainOfThought[In, Out, ROut any](n Output[In, Out], rewardModel NodeType[Out, ROut]) Output[In, Out] {
    return nil
}
*/
