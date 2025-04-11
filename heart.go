// ./heart.go
package heart

import (
	"context"
	"fmt"
	"path"
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

// Inputer defines the interface for retrieving the input of a node.
type Inputer[In any] interface {
	// In returns the resolved input of the node, blocking if necessary.
	In() (In, error)
}

// Output defines the interface for retrieving the output of a node or operation.
// It focuses on accessing the final value and checking for completion.
type Output[Out any] interface {
	// Out returns the output of the node, blocking if necessary.
	Out() (Out, error)
	// Done returns a channel that is closed when the node's execution is complete (successfully or with an error).
	Done() <-chan struct{}
}

// Node defines the interface for a complete processing node instance within the graph.
// It allows inspection of both input and output.
type Node[In, Out any] interface {
	Inputer[In] // Embeds In()
	Output[Out] // Embeds Out() and Done()
	// InOut returns both input and output, blocking if necessary.
	InOut() (InOut[In, Out], error)
}

// NodeDefinition represents the static definition of a node's structure and behavior.
// It's used to create runtime node instances via Bind.
type NodeDefinition[In, Out any] interface {
	heart() // Internal marker method
	// Bind creates a runtime node instance (Node) by connecting it to an input source (Output).
	// Binding triggers the eager execution of the node.
	Bind(Output[In]) Node[In, Out]
}

// into represents an immediately resolved value or error, acting as an Output source.
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

// Into wraps a concrete value, making it an immediately resolved Output.
func Into[Out any](out Out) Output[Out] {
	return &into[Out]{out: out}
}

// IntoError wraps an error, making it an immediately resolved Output that returns the error.
func IntoError[Out any](err error) Output[Out] {
	return &into[Out]{err: err}
}

// Ensure into implements Node (specifically Output part is most relevant for source nodes)
var _ Node[struct{}, any] = (*into[any])(nil)

// NodeInitializer provides identification for a node type, primarily for dependency injection.
type NodeInitializer interface {
	ID() NodeTypeID // Returns the type identifier (e.g., "openai:chat")
}

// definition holds the static configuration of a node definition.
type definition[In, Out any] struct {
	path        NodePath   // Unique hierarchical path for this node instance
	nodeTypeID  NodeTypeID // Store the node type ID separately
	initializer NodeInitializer
	resolver    NodeResolver[In, Out]
	ctx         Context // The context used to define this node
}

// init initializes the resolver and performs dependency injection.
func (d *definition[In, Out]) init() error {
	if d.resolver == nil {
		return fmt.Errorf("internal error: node resolver is nil for node path %s", d.path)
	}
	d.initializer = d.resolver.Init()
	if d.initializer == nil {
		return fmt.Errorf("NodeResolver.Init() returned nil for node path %s", d.path)
	}

	d.nodeTypeID = d.initializer.ID()
	if d.nodeTypeID == "" {
		return fmt.Errorf("NodeInitializer.ID() returned empty NodeTypeID for node path %s", d.path)
	}

	return dependencyInject(d.initializer, d.nodeTypeID) // Inject based on TYPE id
}

// Bind creates a runtime node instance (Node) and immediately starts its execution in a goroutine.
func (d *definition[In, Out]) Bind(in Output[In]) Node[In, Out] {
	n := &node[In, Out]{
		d:      d,
		in:     in,
		inOut:  InOut[In, Out]{},
		doneCh: make(chan struct{}), // Initialize the done channel
	}

	// Eager execution: Start the node's get() method in a goroutine.
	go n.get()

	return n // Return the Node instance immediately.
}

// Internal marker method for NodeDefinition interface satisfaction.
func (d *definition[In, Out]) heart() {}

var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

// NodeTypeID represents the *type* of a node (e.g., "openai:createChatCompletion", "system:transform").
// Used for dependency injection and potentially metrics/logging.
type NodeTypeID string

// DefineNode creates a node definition.
// It calculates the unique NodePath based on the context's BasePath and the user-provided nodeID.
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
		// nodeTypeID and initializer are set during d.init()
	}
}

// NodeResolver defines the interface for the core logic of a node.
type NodeResolver[In, Out any] interface {
	// Init provides the NodeInitializer, typically for dependency injection setup.
	Init() NodeInitializer
	// Get contains the primary execution logic, transforming input to output.
	Get(context.Context, In) (Out, error)
}

// InOut holds both the input and output values of a node.
type InOut[In, Out any] struct {
	In  In
	Out Out
}

// --- NewNode Implementation ---

// newNodeResolver is a resolver for nodes created using NewNode.
// It wraps a user function that returns an Output, allowing dynamic sub-graph construction.
type newNodeResolver[Out any] struct {
	fun    func(ctx Context) Output[Out] // Takes heart Context, returns Output
	defCtx Context                       // The context captured when NewNode was defined
}

// genericNodeInitializer - Reusable initializer providing a NodeTypeID
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

// Get executes the wrapped function `fun` to get an Output, then calls Out() on it.
// The input `_ struct{}` is ignored.
func (r *newNodeResolver[Out]) Get(stdCtx context.Context, _ struct{}) (Out, error) {
	// Create the derived heart.Context for the function call.
	// The BasePath for this context is the path of the NewNode itself,
	// which is correctly set in r.defCtx.BasePath.
	nodeCtx := r.defCtx // Start with a copy of the definition context
	// Update the standard context within the heart.Context to the one provided at runtime.
	nodeCtx.ctx = stdCtx

	// Call the user's function to get the Output for the actual result.
	// This function might define and bind a sub-graph using nodeCtx.
	subOutputer := r.fun(nodeCtx) // Now returns Output[Out]

	// Check if the user function returned a nil Output, which is invalid.
	if subOutputer == nil {
		var zero Out
		return zero, fmt.Errorf("internal execution error: NewNode function returned a nil Output")
	}

	// Block and wait for the result from the Output returned by the user function.
	// This retrieves the actual value (or error) from the sub-graph/computation defined within 'fun'.
	return subOutputer.Out()
}

// Ensure newNodeResolver implements NodeResolver
var _ NodeResolver[struct{}, any] = (*newNodeResolver[any])(nil)

// NewNode creates an Output that wraps an arbitrary Go function (`fun`).
// The function `fun` is responsible for defining the logic (potentially a sub-graph)
// and returning an Output representing the final result of that logic.
// Execution is eager: `NewNode` defines and binds the wrapper node immediately.
// The provided `ctx` is used to define the wrapper node itself (determining its path).
// The `fun` receives a derived `Context` with the correct `BasePath`, allowing it to
// correctly define sub-nodes relative to the NewNode wrapper.
func NewNode[Out any](
	ctx Context, // Context for defining this NewNode wrapper
	nodeID NodeID, // ID for this NewNode wrapper relative to ctx.BasePath
	fun func(ctx Context) Output[Out], // The function that returns the final Output
) Output[Out] { // Returns an Output representing the execution of the wrapper

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
	// gets the returned Output, and calls .Out() on it to get the final result.
	noder := nodeDefinition.Bind(Into(struct{}{}))

	// Return the Output representing the wrapper node immediately.
	// Calling .Out() on this Output will block until the Output returned by 'fun' resolves.
	return noder
}

// --- If Implementation ---
// NOTE: Needs significant update later for path and context handling within branches.
// This implementation is broken. Use NewNode with standard Go if/else logic instead.

type _if[Out any] struct {
	ctx        Context // The context used to *define* the If node
	_condition Output[bool]
	ifTrue     func(Context) Output[Out] // Function expects a derived context, returns Output
	_else      func(Context) Output[Out] // Function expects a derived context, returns Output
	defPath    NodePath                  // Store the definition path to derive branch paths
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

	var result Output[Out]
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
		if i.ifTrue == nil {
			return *new(Out), fmt.Errorf("internal execution error: If node (true branch) has nil function")
		}
		result = i.ifTrue(branchCtx)
	} else {
		branchCtx = falseCtx // Use derived context (incorrectly derived)
		if i._else == nil {
			var zero Out
			// Represent the "no-op" else branch as an immediately resolved Output
			result = Into(zero)
		} else {
			result = i._else(branchCtx)
		}
	}

	// Ensure the chosen branch function actually returned an Output
	if result == nil {
		branch := "false"
		if condition {
			branch = "true"
		}
		return *new(Out), fmt.Errorf("internal execution error: If node (%s branch) function returned a nil Output", branch)
	}

	// Calling Out() here will trigger the execution of the selected branch's Output
	// This part is conceptually okay, but getting the correct `result` Output
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
	condition Output[bool],
	ifTrue func(Context) Output[Out], // Must not be nil
	_else func(Context) Output[Out], // Can be nil (results in zero value for Out)
) Output[Out] { // Returns the Output handle
	// Define the If node itself.
	ifPath := JoinPath(ctx.BasePath, nodeID)
	fmt.Printf("WARNING: Defining heart.If node '%s'. This construct is currently broken; use heart.NewNode with Go's if/else instead.\n", nodeID)

	if ifTrue == nil {
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
