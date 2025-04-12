// ./heart.go
package heart

import (
	"context"
	"fmt"
	"path"
	"strings" // Needed for JoinPath and FanIn
	// Assuming store interfaces/errors are defined here
)

// NodeID, NodePath, JoinPath remain the same...
type NodeID string
type NodePath string

func JoinPath(base NodePath, id NodeID) NodePath { /* ... */
	joined := path.Join(string(base), string(id))
	// Handle joining with root "/" correctly
	if base == "/" && id != "" && !strings.HasPrefix(joined, "/") {
		return NodePath("/" + joined)
	} else if base != "/" && strings.HasPrefix(joined, "/") {
		// path.Join might strip leading slashes if base is not "/"
		// This logic might need refinement depending on desired path behavior
	}
	return NodePath(joined)
}

// Output interface remains the same...
type Output[Out any] interface {
	Out() (Out, error)
	Done() <-chan struct{}
	// Maybe add GetUUID() here if needed universally? Currently only on WorkflowOutput
	// GetUUID() WorkflowUUID
}

// NewNodeContext is a specialized context provided to functions within heart.NewNode.
// It allows controlled, eager resolution of dependencies within that specific node's scope
// using helper functions like heart.FanIn.
type NewNodeContext struct {
	Context // Embed the standard workflow context
}

// resolve (unexported) triggers the internal resolution mechanism for a dependency node blueprint.
// This is the core mechanism used by FanIn helpers.
// It uses the embedded Context's registry and internalResolve logic.
// It now correctly returns the specific Out type or an error.
func resolve[Out any](c NewNodeContext, nodeBlueprint any) (Out, error) {
	// Access the embedded Context's internalResolve method
	if c.registry == nil { // Check embedded registry
		var zero Out
		return zero, fmt.Errorf("internal error: NewNodeContext used without a valid execution registry (path: %s, wf: %s)", c.BasePath, c.uuid)
	}
	// internalResolve handles triggering/awaiting the lazy execution and returns the specific Out type.
	return internalResolve[Out](c.Context, nodeBlueprint)
}

// FanIn waits for a single dependency node to complete within a NewNode function
// and returns its typed result or an error.
// It provides a type-safe way to access dependency results eagerly inside NewNode.
func FanIn[Out any](ctx NewNodeContext, dep OutputBlueprint[Out]) (Out, error) {
	// Call the unexported resolve method on the NewNodeContext.
	// resolve is already typed with [Out], so it returns the correct type or error directly.
	resultTyped, err := resolve[Out](ctx, dep) // Returns Out, error
	if err != nil {
		var zero Out
		// Enhance error message with path if possible (dep should have internal_getPath)
		depPath := "unknown_dependency_path"
		if pather, ok := dep.(interface{ internal_getPath() NodePath }); ok {
			depPath = string(pather.internal_getPath())
		}
		return zero, fmt.Errorf("FanIn failed for dependency '%s' in node '%s' (wf: %s): %w", depPath, ctx.BasePath, ctx.uuid, err)
	}

	// No further type assertion needed here, as resolve[Out] already returned the specific type.
	return resultTyped, nil
}

// OutputBlueprint represents a blueprint handle for a computation
// that results in a value of type Out (or an error).
type OutputBlueprint[Out any] interface {
	// These methods allow the framework to interact with the blueprint
	// without needing to know the specific In/Out types at compile time everywhere.

	// This is just a dummy method that returns a zero Out to make the compiler happy.
	// If we remove this the compiler fails to infer Out when passing the interface around.
	// This is fine meme.
	zero(Out)

	// internal_getPath returns the unique path identifier for this node instance
	// within the workflow graph.
	internal_getPath() NodePath

	// internal_getDefinition returns the underlying static definition (*definition[In, Out])
	// type-erased as 'any'. The registry uses this to access definition details.
	internal_getDefinition() any

	// internal_getInputSource returns the blueprint handle for the node providing
	// the input for this node (Node[In]), type-erased as 'any'.
	// Returns nil if the node has no specific input source (e.g., an 'Into' node).
	internal_getInputSource() any
}

// Node represents a bound, reusable blueprint for a computation step.
// It's essentially a handle that points to a definition and its input source.
// It does not hold execution state itself.
type Node[Out any] interface {
	heart() // Internal marker method to identify heart node types

	OutputBlueprint[Out]
}

// into represents an immediately resolved value or error, acting as a source blueprint.
// It satisfies the Node interface for consistency.
type into[Out any] struct {
	out Out
	err error
}

// heart is the marker method implementation for *into.
func (i *into[Out]) heart() {}

func (i *into[Out]) zero(Out) {}

// internal_getPath returns a static path for 'into' nodes.
func (i *into[Out]) internal_getPath() NodePath { return "/_source/into" } // Path could be made more unique if needed

// internal_getDefinition returns nil as 'into' nodes don't have a separate definition struct.
func (i *into[Out]) internal_getDefinition() any { return nil }

// internal_getInputSource returns nil as 'into' nodes are source nodes.
func (i *into[Out]) internal_getInputSource() any { return nil }

// internal_out provides direct access to the pre-resolved value/error.
// Used by Context.internalResolve to handle these efficiently.
func (i *into[Out]) internal_out() (any, error) { return i.out, i.err }

// Into wraps a concrete value into a Node blueprint handle.
// The input type is struct{} as it takes no dynamic input.
func Into[Out any](out Out) Node[Out] {
	return &into[Out]{out: out}
}

// IntoError wraps an error into a Node blueprint handle.
// The input type is struct{} as it takes no dynamic input.
func IntoError[Out any](err error) Node[Out] {
	return &into[Out]{err: err}
}

// Ensure *into implements the Node interface (using struct{} for In).
var _ Node[any] = (*into[any])(nil)

// NodeInitializer defines the interface for initializing node-type-specific state.
// This is typically implemented by the NodeResolver or a struct returned by its Init method.
// It provides the NodeTypeID used for dependency injection mapping.
type NodeInitializer interface {
	// ID returns a unique identifier for the *type* of the node resolver/logic.
	// This is used to look up dependencies registered for this node type.
	ID() NodeTypeID
}

// definition holds the static configuration of a node definition blueprint.
// It's created by DefineNode and contains the resolver and definition context.
type definition[In, Out any] struct {
	path        NodePath              // Full path of the node instance
	nodeTypeID  NodeTypeID            // ID for the type of node (set during init)
	initializer NodeInitializer       // Initialized instance (often the resolver itself) (set during init)
	resolver    NodeResolver[In, Out] // The user-provided resolver logic
	defCtx      Context               // Context at the time of definition
	initErr     error                 // Stores error from init() (dependency injection, etc.)
}

// init initializes the resolver (calling its Init method) and performs dependency injection.
// It's called internally by Bind and stores any error encountered in d.initErr.
// Returns the error encountered during initialization.
func (d *definition[In, Out]) init() error {
	if d.resolver == nil {
		return fmt.Errorf("internal error: node resolver is nil for node path %s (wf: %s)", d.path, d.defCtx.uuid)
	}

	// Call the resolver's Init() method to get the NodeInitializer instance.
	// This instance might be the resolver itself or a separate struct.
	initer := d.resolver.Init()
	if initer == nil {
		return fmt.Errorf("NodeResolver.Init() returned nil for node path %s (wf: %s)", d.path, d.defCtx.uuid)
	}
	d.initializer = initer // Store the initializer

	// Get the NodeTypeID from the initializer. This ID is crucial for dependency injection.
	typeID := d.initializer.ID()
	if typeID == "" {
		return fmt.Errorf("NodeInitializer.ID() returned empty NodeTypeID for node path %s (wf: %s)", d.path, d.defCtx.uuid)
	}
	d.nodeTypeID = typeID // Store the type ID

	// Perform dependency injection using the initializer instance and its NodeTypeID.
	// dependencyInject uses reflection to find and call a `DependencyInject(DepType)` method
	// on the initializer if it exists and a dependency is registered for the typeID.
	diErr := dependencyInject(d.initializer, d.nodeTypeID)
	if diErr != nil {
		return fmt.Errorf("dependency injection failed for node path %s (type %s, wf: %s): %w", d.path, d.nodeTypeID, d.defCtx.uuid, diErr)
	}

	// Initialization successful
	return nil
}

// Bind creates a runtime node blueprint handle (Node).
// It calls init() to perform setup (like DI) and stores any setup error in initErr.
// It does NOT return an error itself; setup errors are handled during execution.
// The inputSource parameter is the blueprint handle for the node providing input.
func (d *definition[In, Out]) Bind(inputSource OutputBlueprint[In]) Node[Out] {
	// Run initialization (calls resolver.Init(), performs DI).
	// The result (error or nil) is stored in d.initErr.
	d.initErr = d.init()
	if d.initErr != nil {
		// Log the initialization error clearly here?
		fmt.Printf("WARN: Initialization failed during Bind for node %s (wf: %s): %v. Execution will fail.\n", d.path, d.defCtx.uuid, d.initErr)
	}

	// Create the internal node struct which acts as the blueprint handle.
	// This handle links the definition (d) with its specific input source.
	n := &node[In, Out]{
		d:           d,           // Link to the definition containing resolver, initErr etc.
		inputSource: inputSource, // Link to the blueprint of the node providing input
	}

	return n // Return the Node blueprint handle immediately (lazy execution).
}

// createExecution is called by the registry to instantiate the stateful nodeExecution.
// It performs type assertions internally to ensure the inputSource matches expectations.
// It implements the internal 'executionCreator' interface.
func (d *definition[In, Out]) createExecution(inputSourceAny any, wfCtx Context) (executioner, error) {
	// Type assert the inputSourceAny (which comes from the Node blueprint handle)
	// back to the specific Node[In] type expected by this definition.
	var inputSource Node[In]   // Changed to Node[In]
	if inputSourceAny != nil { // Handle nil input source (e.g., for Into nodes used as input)
		var ok bool
		inputSource, ok = inputSourceAny.(Node[In]) // Assert to Node[In]
		if !ok {
			var expectedInputType In // Get type for error message
			// This indicates an internal inconsistency - the bound inputSource blueprint handle
			// doesn't match the expected input type 'In' of the definition's resolver.
			return nil, fmt.Errorf(
				"internal error: type assertion failed for input source blueprint in createExecution for node %s (wf: %s). Expected blueprint compatible with Node[%T], got %T",
				d.path, wfCtx.uuid, expectedInputType, inputSourceAny,
			)
		}
	} // If inputSourceAny is nil, inputSource remains nil. newExecution handles this.

	// Create the stateful execution instance.
	// newExecution handles the case where inputSource is nil.
	ne := newExecution[In, Out](d, inputSource, wfCtx)

	// Return the concrete *nodeExecution[In, Out] instance, cast to the executioner interface.
	return ne, nil
}

// heartDef is an internal marker method for NodeDefinition interface.
func (d *definition[In, Out]) heartDef() {}

// NodeDefinition represents the static definition of a node's structure and behavior.
// It's created by DefineNode and used to Bind specific instances.
type NodeDefinition[In, Out any] interface {
	heartDef() // Internal marker method

	// Bind creates a runtime node blueprint handle (Node) by associating this
	// definition with a specific input source blueprint.
	// Initialization (like DI) happens during Bind, but errors are deferred
	// until the node is actually executed (lazily).
	Bind(inputSource OutputBlueprint[In]) Node[Out]
}

// Ensure *definition implements the NodeDefinition interface.
var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

// NodeTypeID is a string identifier for a *type* of node (e.g., "api-client", "image-resizer").
// It's used for registering and injecting dependencies common to all nodes of that type.
type NodeTypeID string

// DefineNode creates a static NodeDefinition blueprint.
// It captures the resolver logic and the context at the point of definition.
func DefineNode[In, Out any](
	ctx Context, // The workflow context at the point of definition
	nodeID NodeID, // A unique ID for this node *instance* within its parent context
	resolver NodeResolver[In, Out], // The logic implementation for the node
) NodeDefinition[In, Out] {
	fullPath := JoinPath(ctx.BasePath, nodeID)

	// Basic validation
	if nodeID == "" {
		panic(fmt.Sprintf("DefineNode requires a non-empty node ID (path: %s, wf: %s)", ctx.BasePath, ctx.uuid))
	}
	if resolver == nil {
		panic(fmt.Sprintf("DefineNode requires a non-nil resolver (path: %s, id: %s, wf: %s)", ctx.BasePath, nodeID, ctx.uuid))
	}
	// TODO: Check if nodeID contains invalid characters (like '/')?

	// Create the definition struct.
	// Initializer, NodeTypeID, and initErr are set later during Bind -> init().
	return &definition[In, Out]{
		path:     fullPath,
		resolver: resolver,
		defCtx:   ctx, // Capture context (BasePath, registry, store, etc.)
	}
}

// NodeResolver defines the interface for the core logic of a node.
type NodeResolver[In, Out any] interface {
	// Init is called once during the Bind phase (via definition.init).
	// It should return a NodeInitializer instance (often the resolver itself).
	// The NodeInitializer provides the NodeTypeID used for dependency injection.
	// Init should perform any one-time setup for the node *type*.
	Init() NodeInitializer

	// Get is called during lazy execution when the node's result is needed.
	// It receives the resolved input value and the standard Go context.
	// It performs the primary computation for the node instance.
	Get(ctx context.Context, in In) (Out, error)
}

// InOut is a simple struct often used for convenience in resolvers or state.
type InOut[In, Out any] struct {
	In  In
	Out Out
}

// -----------------------------------------------------------------------------
// NewNode Implementation (ADJUSTED for NewNodeContext)
// -----------------------------------------------------------------------------

// newNodeResolver wraps the user's function for NewNode.
type newNodeResolver[Out any] struct {
	// The user's function now accepts NewNodeContext
	fun    func(ctx NewNodeContext) Node[Out] // <<< CHANGED
	defCtx Context                            // The original definition context
}

// genericNodeInitializer is used by newNodeResolver.
type genericNodeInitializer struct {
	id NodeTypeID
}

func (g genericNodeInitializer) ID() NodeTypeID { return g.id }

// Init returns a simple initializer for NewNode wrappers.
func (r *newNodeResolver[Out]) Init() NodeInitializer {
	// Provide a distinct type ID for nodes created via NewNode
	// This allows potential dependency injection specific to NewNode wrappers if needed.
	return genericNodeInitializer{id: "system:newNodeWrapper"}
}

// Get executes the user's function passed to NewNode.
func (r *newNodeResolver[Out]) Get(stdCtx context.Context, _ struct{}) (Out, error) { // Input is always struct{}
	// 1. Create the NewNodeContext from the captured definition context.
	newNodeCtx := NewNodeContext{Context: r.defCtx}

	// 2. CRITICAL: Update the embedded standard context.Context field within NewNodeContext
	// to use the *runtime* context (stdCtx) passed into Get. This ensures that
	// cancellations, deadlines, and value propagation work correctly for operations
	// initiated *within* the user's function (e.g., store calls, nested DefineNode/Bind, FanIn).
	newNodeCtx.ctx = stdCtx

	// 3. Call the user's function, passing the specialized NewNodeContext.
	// The user's function defines the sub-graph or logic for this node and returns
	// the blueprint handle for the final node in that sub-graph.
	subGraphNodeBlueprint := r.fun(newNodeCtx) // <<< Pass the specialized context

	// 4. Validate the returned blueprint handle from the user's function.
	if subGraphNodeBlueprint == nil {
		var zero Out
		return zero, fmt.Errorf("internal execution error: NewNode function for '%s' (wf: %s) returned a nil Node blueprint handle", newNodeCtx.BasePath, newNodeCtx.uuid)
	}

	// 5. Resolve the sub-graph defined by the user's function.
	// Use the newNodeCtx's internalResolve method, which correctly uses the registry
	// and the updated runtime context (newNodeCtx.ctx).
	// internalResolve is now correctly typed with [Out].
	resolvedVal, err := internalResolve[Out](newNodeCtx.Context, subGraphNodeBlueprint)
	if err != nil {
		var zero Out
		// Wrap error for clarity
		return zero, fmt.Errorf("error resolving NewNode sub-graph at '%s' (wf: %s): %w", newNodeCtx.BasePath, newNodeCtx.uuid, err)
	}

	// 6. NO TYPE ASSERTION NEEDED HERE. internalResolve[Out] already returned the correct type.

	// 7. Return the successfully resolved and typed result.
	return resolvedVal, nil
}

// Ensure newNodeResolver still implements the correct NodeResolver interface.
var _ NodeResolver[struct{}, any] = (*newNodeResolver[any])(nil)

// NewNode creates a Node blueprint handle that wraps an arbitrary Go function (`fun`).
// The provided function `fun` receives a heart.NewNodeContext, allowing it to define
// sub-graphs (by calling DefineNode/Bind) or use heart.FanIn helpers to eagerly resolve
// dependencies within its scope before returning the final Node blueprint for its result.
func NewNode[Out any](
	ctx Context, // Definition context (captures BasePath, etc.)
	nodeID NodeID, // Unique ID for this NewNode instance within its parent
	// The user's function: Takes NewNodeContext, returns the final Node blueprint for this step.
	fun func(ctx NewNodeContext) Node[Out], // <<< CHANGED signature
) Node[Out] { // Input type is always struct{} for the wrapper node

	// --- Input validation ---
	if fun == nil {
		panic(fmt.Sprintf("heart.NewNode requires a non-nil function (node ID: %s, path: %s, wf: %s)", nodeID, ctx.BasePath, ctx.uuid))
	}
	if nodeID == "" {
		panic(fmt.Sprintf("heart.NewNode requires a non-empty node ID (path: %s, wf: %s)", ctx.BasePath, ctx.uuid))
	}
	// --- End Input validation ---

	// 1. Create the resolver, capturing the user's function and the definition context.
	resolver := &newNodeResolver[Out]{
		fun:    fun, // Store the user's function
		defCtx: ctx, // Capture the context at definition time
	}

	// 2. Define the wrapper node using the standard DefineNode mechanism.
	// The input type is struct{}, output type is Out.
	nodeDefinition := DefineNode[struct{}, Out](ctx, nodeID, resolver)

	// 3. Bind the wrapper node definition.
	// The input source is an immediately resolved empty struct.
	// Initialization errors (DI, resolver init, etc.) are stored in the definition via init().
	wrapperNodeBlueprint := nodeDefinition.Bind(Into(struct{}{}))

	// 4. Return the blueprint handle for the wrapper node. Execution is lazy.
	return wrapperNodeBlueprint
}

// -----------------------------------------------------------------------------
// If Implementation (Uses NewNode internally, no changes needed here)
// -----------------------------------------------------------------------------
func If[Out any]( // Removed NodeIn as it's implicitly struct{} via condition
	ctx Context,
	nodeID NodeID,
	condition Node[bool], // Condition is a Node blueprint handle
	ifTrue func(ctx Context) Node[Out], // Returns Node blueprint
	_else func(ctx Context) Node[Out], // Returns Node blueprint
) Node[Out] { // Output type is 'any' as input could be anything via condition
	// --- Pre-checks ---
	if ifTrue == nil {
		err := fmt.Errorf("programming error: heart.If node '%s' at path '%s' called with a nil 'ifTrue' function (wf: %s)", nodeID, ctx.BasePath, ctx.uuid)
		return IntoError[Out](err) // Return immediately failing blueprint
	}
	if condition == nil {
		err := fmt.Errorf("programming error: heart.If node '%s' at path '%s' called with a nil 'condition' Node blueprint handle (wf: %s)", nodeID, ctx.BasePath, ctx.uuid)
		return IntoError[Out](err)
	}

	// Use NewNode to wrap the conditional logic.
	// NewNode handles the context management (passing NewNodeContext) and resolution.
	return NewNode[Out]( // Returns Node[Out], assignable to Node[Out]
		ctx,
		nodeID,
		// --- This inner function contains the core conditional logic ---
		// It receives the specialized NewNodeContext from the NewNode wrapper.
		func(wrapperCtx NewNodeContext) Node[Out] { // Returns the chosen branch blueprint
			// 1. Eagerly resolve the condition using FanIn (which uses wrapperCtx.resolve).
			condValue, err := FanIn[bool](wrapperCtx, condition) // FanIn handles resolution & type assertion
			if err != nil {
				// Wrap the error from FanIn
				err = fmt.Errorf("failed to evaluate condition for If node '%s' at path '%s' (wf: %s): %w", nodeID, wrapperCtx.BasePath, wrapperCtx.uuid, err)
				return IntoError[Out](err) // Return blueprint representing the error
			}

			// 2. Choose the branch based on the condition result.
			var chosenBranchNode Node[Out] // This will hold the *blueprint* handle
			var branchCtx Context          // Use standard Context for branch functions

			if condValue {
				// Create context for the 'true' branch
				branchCtx = wrapperCtx.Context // Get embedded standard context
				branchCtx.BasePath = JoinPath(wrapperCtx.BasePath, "ifTrue")
				// Execute the user's 'ifTrue' function to get the blueprint
				chosenBranchNode = ifTrue(branchCtx)
			} else {
				// Create context for the 'false' branch
				branchCtx = wrapperCtx.Context // Get embedded standard context
				branchCtx.BasePath = JoinPath(wrapperCtx.BasePath, "ifFalse")
				// Check if an 'else' branch exists
				if _else == nil {
					// Default branch: return a blueprint with the zero value of Out
					var zero Out
					chosenBranchNode = Into(zero) // Returns Node[Out]
				} else {
					// Execute the user's 'else' function to get the blueprint
					chosenBranchNode = _else(branchCtx)
				}
			}

			// 3. Validate the blueprint returned by the chosen branch function.
			if chosenBranchNode == nil {
				branchName := "ifFalse"
				if condValue {
					branchName = "ifTrue"
				}
				err = fmt.Errorf("If node '%s' at path '%s' (wf: %s): '%s' branch function returned a nil Node blueprint handle", nodeID, wrapperCtx.BasePath, wrapperCtx.uuid, branchName)
				return IntoError[Out](err)
			}

			// 4. Return the chosen branch's blueprint handle.
			// The NewNode wrapper's Get method will then resolve this returned blueprint.
			return chosenBranchNode
		}, // --- End of NewNode inner function ---
	)
}

// safeAssert is a helper function for performing type assertions.
// It improves readability compared to the two-value assignment form.
// No longer needed by FanIn, but potentially useful elsewhere. Kept for now.
func safeAssert[T any](val any) (T, bool) {
	typedVal, ok := val.(T)
	return typedVal, ok
}
