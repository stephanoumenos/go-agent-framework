// ./heart.go
package heart

import (
	"context"
	"fmt"
	"path"
	// Make sure store is imported if needed for enhancements later
	// Make sure uuid is imported
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

	// Pass the *initializer* (which might be the resolver itself or a dedicated struct)
	// to dependencyInject, along with its NodeTypeID.
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

// If creates a conditional execution branch based on an Output[bool].
// It defines a wrapper node using NewNode. The function within NewNode waits
// for the 'condition' to resolve, then executes either the 'ifTrue' or '_else'
// function, passing a derived context with an appropriate BasePath
// (e.g., ".../nodeID/ifTrue" or ".../nodeID/ifFalse").
//
// The Output returned by the chosen branch function becomes the final Output of If.
// Nodes defined within the executed branch will be persisted under the derived path.
//
// Args:
//
//	ctx: The context used to define the If wrapper node. Its BasePath determines the parent path.
//	nodeID: The local ID for this If construct, relative to ctx.BasePath. The wrapper node path will be JoinPath(ctx.BasePath, nodeID).
//	condition: An Output[bool] whose result determines which branch executes.
//	ifTrue: A function executed if the condition is true. Receives a derived Context for the true branch. Must not be nil.
//	_else: A function executed if the condition is false. Receives a derived Context for the false branch. Can be nil (results in an Output resolving to the zero value of Out).
//
// Returns:
//
//	An Output[Out] representing the result of the executed branch.
func If[Out any](
	ctx Context,
	nodeID NodeID,
	condition Output[bool],
	ifTrue func(ctx Context) Output[Out],
	_else func(ctx Context) Output[Out],
) Output[Out] {
	// --- Pre-checks ---
	if ifTrue == nil {
		err := fmt.Errorf("programming error: heart.If node '%s' at path '%s' called with a nil 'ifTrue' function", nodeID, ctx.BasePath)
		// Return an immediately failing Output
		return IntoError[Out](err)
	}
	if condition == nil {
		err := fmt.Errorf("programming error: heart.If node '%s' at path '%s' called with a nil 'condition' Output", nodeID, ctx.BasePath)
		return IntoError[Out](err)
	}
	// TODO: Add validation for nodeID if needed (e.g., non-empty, valid characters)

	// Use NewNode to wrap the conditional logic.
	// The wrapper node itself doesn't take direct input via Bind (hence struct{}),
	// its logic depends on resolving the 'condition' Output internally.
	return NewNode[Out](
		ctx, // Use the original context provided to If to define the wrapper node.
		// This sets the parent path correctly.
		nodeID, // Use the user-provided ID for the wrapper node itself.
		// --- This inner function contains the core conditional logic ---
		func(wrapperCtx Context) Output[Out] {
			// wrapperCtx is the context for the *execution* of the wrapper node.
			// Its BasePath is correctly set to the full path of the If wrapper
			// (e.g., /parent/path/nodeID).

			// 1. Block and wait for the condition to resolve.
			condValue, err := condition.Out() // This blocks until the condition node completes
			if err != nil {
				// If condition evaluation itself failed, the If construct fails.
				// Wrap the error for clarity.
				return IntoError[Out](fmt.Errorf("failed to evaluate condition for If node '%s' at path '%s': %w", nodeID, wrapperCtx.BasePath, err))
			}

			// TODO (Optional Enhancement): Persist the condition result (condValue)
			// to the wrapper node's state in the store for better traceability.
			// This might involve adding fields to `nodeState` or a specific store method.
			// For now, we just use the value in memory.

			var chosenBranchOutput Output[Out]

			if condValue {
				// --- True Branch ---
				// Derive context specifically for the true branch.
				trueBranchCtx := wrapperCtx                                      // Create a copy to modify BasePath
				trueBranchCtx.BasePath = JoinPath(wrapperCtx.BasePath, "ifTrue") // e.g., /parent/path/nodeID/ifTrue

				// Execute the user's true branch function, passing the derived context.
				chosenBranchOutput = ifTrue(trueBranchCtx)

			} else {
				// --- False Branch ---
				// Derive context specifically for the false branch.
				falseBranchCtx := wrapperCtx                                       // Create a copy to modify BasePath
				falseBranchCtx.BasePath = JoinPath(wrapperCtx.BasePath, "ifFalse") // e.g., /parent/path/nodeID/ifFalse

				if _else == nil {
					// No else function provided. Resolve immediately with the zero value for Out.
					var zero Out
					chosenBranchOutput = Into(zero) // Use heart.Into for immediate resolution
				} else {
					// Execute the user's else branch function, passing the derived context.
					chosenBranchOutput = _else(falseBranchCtx)
				}
			}

			// Validate that the chosen branch function returned a non-nil Output.
			if chosenBranchOutput == nil {
				branchName := "ifFalse"
				if condValue {
					branchName = "ifTrue"
				}
				// This indicates a programming error in the user's branch function.
				return IntoError[Out](fmt.Errorf("If node '%s' at path '%s': '%s' branch function returned a nil Output", nodeID, wrapperCtx.BasePath, branchName))
			}

			// Return the Output handle obtained from the executed branch.
			// The result of the If construct will be the result of this chosen Output.
			return chosenBranchOutput
		}, // --- End of NewNode inner function ---
	)
}
