// ./heart.go
package heart

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"heart/store"

	"github.com/google/uuid"
)

// --- Core Identifiers ---

// NodeID represents the user-defined identifier for a node *blueprint*
// (NodeDefinition) within its definition scope (e.g., top-level or inside a workflow).
// It forms part of the unique NodePath during execution.
type NodeID string

// NodePath represents the unique, slash-separated path to a specific node
// *instance* within a workflow execution graph. It includes the base NodeIDs
// and instance counters (e.g., "/workflowA:#0/nodeB:#1/subNodeC:#0").
type NodePath string

// WorkflowUUID is an alias for uuid.UUID, representing the unique identifier
// for a specific workflow execution run.
type WorkflowUUID = uuid.UUID

// JoinPath combines a base NodePath with a segment (NodeID, NodePath, or string)
// to create a new NodePath. It handles path separators correctly.
// For example, JoinPath("/wf", NodeID("node")) yields "/wf/node".
// JoinPath("/", NodePath("node:#0")) yields "/node:#0".
func JoinPath(base NodePath, segment any) NodePath {
	baseStr := string(base)
	var segmentStr string

	switch s := segment.(type) {
	case NodeID:
		segmentStr = string(s)
	case NodePath: // Allow joining path segments directly (e.g., "nodeA:#0").
		segmentStr = string(s)
	case string:
		segmentStr = s
	default:
		// Panic because this indicates a programming error within the framework.
		panic(fmt.Sprintf("JoinPath received unsupported segment type: %T", segment))
	}

	// Ensure base path ends with a slash if it's not the root.
	if baseStr != "/" && !strings.HasSuffix(baseStr, "/") {
		baseStr += "/"
	}
	// Use path.Join for cleaning, but it might remove the leading slash if base is "/".
	joined := path.Join(baseStr, segmentStr)
	// Restore leading slash if necessary.
	if base == "/" && !strings.HasPrefix(joined, "/") && joined != "." {
		return NodePath("/" + joined)
	} else if joined == "." && base == "/" { // Handle joining empty/dot segment to root.
		return "/"
	}
	return NodePath(joined)
}

// --- Execution Handle (Unified for Nodes and Workflows) ---

// ExecutionHandle represents a reference to a potentially uninitialized and
// unevaluated instance of a node or workflow defined by a NodeDefinition.
// It acts as a lazy pointer to the eventual result. Handles are connected
// together to form the execution graph.
//
// The internal methods are used by the framework's execution logic (like
// internalResolve and the executionRegistry) and should not be called directly
// by user code.
type ExecutionHandle[Out any] interface {
	// zero is a marker method used for type inference with generics.
	zero(Out)
	// heartHandle is an internal marker method for identifying heart handles.
	heartHandle()
	// internal_getPath returns the unique NodePath assigned to this specific
	// execution instance once it has been resolved by the framework. Before
	// resolution, it may return a temporary or unresolved path.
	internal_getPath() NodePath
	// internal_getDefinition returns the underlying NodeDefinition blueprint
	// (as any) that this handle corresponds to. The returned value should
	// implement the internal definitionGetter interface. Returns nil for
	// handles created via Into() or IntoError().
	internal_getDefinition() any
	// internal_getInputSource returns the ExecutionHandle that provides the input
	// for this handle's execution, returned as 'any'. Returns nil if the node
	// takes no input or is a source node (like Into).
	internal_getInputSource() any
	// internal_out is used primarily by 'into' nodes (created via Into/IntoError)
	// to provide their direct value or error. Standard nodes/workflows return an error.
	internal_out() (any, error)
	// internal_setPath is called by the framework (specifically internalResolve)
	// to assign the final, unique NodePath to this handle instance during execution.
	internal_setPath(NodePath)
}

// --- Node Definition (Unified for Atomic Nodes and Workflows) ---

// NodeDefinition represents a reusable blueprint for an executable unit within
// the Heart framework. This unit can be an *atomic* node (defined via DefineNode
// with a user-provided NodeResolver) or a *composite* workflow (defined via
// WorkflowFromFunc or NewNode).
//
// NodeDefinitions are typically created once during setup and then used via their
// Start() method within workflow handlers or NewNode functions to create specific
// ExecutionHandles representing instances in the execution graph.
//
// The internal methods are used by the framework and should not be called directly.
type NodeDefinition[In, Out any] interface {
	// Start creates a new ExecutionHandle representing a potential instance of this
	// node definition. It takes a handle (`inputSource`) providing the input value.
	// Start is LAZY; it does not trigger execution immediately. It performs
	// initialization checks (like dependency injection) and returns a handle
	// that can be passed to other nodes or resolved later. Each call to Start
	// conceptually represents a new potential instance in the graph.
	Start(inputSource ExecutionHandle[In]) ExecutionHandle[Out]
	// internal_GetNodeID returns the base NodeID assigned to this definition
	// when it was created (e.g., via DefineNode or WorkflowFromFunc). This is
	// distinct from the full NodePath of an execution instance.
	internal_GetNodeID() NodeID
}

// --- Supporting Types ---

// NodeInitializer is an interface implemented by types returned from a
// NodeResolver's Init() method. It's used during the initialization phase
// primarily for dependency injection.
type NodeInitializer interface {
	// ID returns the NodeTypeID associated with the node resolver. This ID is used
	// by the dependency injection system to find the correct dependency instance.
	ID() NodeTypeID
}

// NodeTypeID is a string identifier used to associate node types with their
// specific dependencies during dependency injection.
type NodeTypeID string

// InOut is a simple generic struct holding an input and output value.
// Currently unused but kept for potential future use cases.
type InOut[In, Out any] struct {
	In  In
	Out Out
}

// Context carries execution-scoped information throughout a workflow run.
// It provides access to the underlying Go context (for cancellation),
// the storage interface, the workflow's unique ID, the execution registry
// for the current scope, the current execution path, and the workflow's
// cancellation function. It's passed to WorkflowHandlerFunc and NewNode functions.
type Context struct {
	// ctx is the standard Go context, used for deadlines and cancellation signals.
	ctx context.Context
	// nodeCount is an atomic counter used for generating unique IDs within the run.
	nodeCount *atomic.Int64 // Kept for potential future use, currently pathing relies on instance counters.
	// store provides access to the persistence layer (e.g., MemoryStore, FileStore).
	store store.Store
	// uuid is the unique identifier for this specific workflow execution run.
	uuid WorkflowUUID
	// registry manages executioner instances for the current scope (workflow or NewNode).
	registry *executionRegistry
	// BasePath is the NodePath prefix for nodes defined within this context.
	// It includes the unique paths of parent workflows/nodes.
	BasePath NodePath
	// cancel is the function to call to cancel this workflow instance's context.
	cancel context.CancelFunc
}

// Done mirrors the context.Context Done method.
func (c Context) Done() <-chan struct{} { return c.ctx.Done() }

// Err mirrors the context.Context Err method.
func (c Context) Err() error { return c.ctx.Err() }

// Value mirrors the context.Context Value method.
func (c Context) Value(key any) any { return c.ctx.Value(key) }

// Deadline mirrors the context.Context Deadline method.
func (c Context) Deadline() (deadline time.Time, ok bool) { return c.ctx.Deadline() }

// safeAssert provides a type-safe assertion without causing a panic on failure.
func safeAssert[T any](val any) (T, bool) {
	typedVal, ok := val.(T)
	return typedVal, ok
}

// --- Future (Used by FanIn) ---

// Future represents the result of an asynchronous operation within a NewNode
// function, specifically used by FanIn. It allows waiting for a dependency
// node's execution to complete and retrieving its result or error.
type Future[Out any] struct {
	// exec holds the specific executioner instance this future is waiting for.
	exec       executioner
	resultOnce sync.Once     // Ensures the background resolution runs only once.
	resultVal  Out           // Stores the resolved value.
	resultErr  error         // Stores the resolved error.
	doneCh     chan struct{} // Closed when the result is available.
}

// newFuture creates a new Future associated with a specific executioner instance.
// It starts a background goroutine to resolve the executioner's result.
func newFuture[Out any](exec executioner) *Future[Out] {
	f := &Future[Out]{exec: exec, doneCh: make(chan struct{})}
	go f.resolveInBackground()
	return f
}

// resolveInBackground waits for the associated executioner to complete via getResult,
// performs type assertion, stores the result/error, and closes the done channel.
func (f *Future[Out]) resolveInBackground() {
	f.resultOnce.Do(func() {
		// Retrieve the result from the specific executioner instance.
		resultAny, execErr := f.exec.getResult()
		f.resultErr = execErr
		if execErr == nil {
			// Perform type assertion if execution succeeded.
			typedVal, ok := resultAny.(Out)
			if !ok {
				// Capture type assertion errors.
				f.resultErr = fmt.Errorf("Future: type assertion failed: expected %T, got %T (for path: %s)", *new(Out), resultAny, f.exec.getNodePath())
				f.resultVal = *new(Out) // Ensure zero value on error.
			} else {
				f.resultVal = typedVal
			}
		} else {
			f.resultVal = *new(Out) // Ensure zero value on error.
		}
		close(f.doneCh) // Signal completion.
	})
}

// Get blocks until the Future's result is available and returns the value and error.
func (f *Future[Out]) Get() (Out, error) { <-f.doneCh; return f.resultVal, f.resultErr }

// Done returns a channel that is closed when the Future's result is ready.
func (f *Future[Out]) Done() <-chan struct{} { return f.doneCh }

// Out is an alias for Get().
func (f *Future[Out]) Out() (Out, error) { return f.Get() }

// --- NewNodeContext ---

// NewNodeContext is the context provided to the function passed to NewNode.
// It embeds the standard Context, allowing access to the workflow's execution
// state and enabling the definition of further nodes within the NewNode scope.
type NewNodeContext struct {
	Context // Embed the standard workflow context.
}

// --- FanIn ---

// FanIn is used within a NewNode function to wait for a dependency represented by
// an ExecutionHandle. It triggers the resolution of the dependency handle (if not
// already started) and returns a Future that will yield the dependency's result
// or error once available.
//
// FanIn ensures that dependencies defined within a NewNode are resolved using the
// correct execution context and registry associated with that NewNode instance.
func FanIn[Out any](ctx NewNodeContext, dep ExecutionHandle[Out]) *Future[Out] {
	if dep == nil {
		// Return an immediately resolved future with an error if the handle is nil.
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New("FanIn called with nil dependency handle")}
		close(f.doneCh)
		return f
	}
	if ctx.registry == nil {
		// This indicates an internal setup error.
		errMsg := fmt.Sprintf("internal error: FanIn called with invalid context (nil registry) for node base path '%s' (wf: %s)", ctx.BasePath, ctx.uuid)
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New(errMsg)}
		close(f.doneCh)
		return f
	}

	// Trigger resolution of the dependency handle using its defining context (ctx.Context).
	// internalResolve calculates the unique path, finds/creates the executioner,
	// and triggers execution if necessary. It returns the final value/error.
	resolvedValue, resolveErr := internalResolve[Out](ctx.Context, dep)
	if resolveErr != nil {
		// If resolution itself failed, return a future resolved with that error.
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: resolveErr, resultVal: *new(Out)}
		close(f.doneCh)
		return f
	}

	// Get the unique path assigned to the handle during resolution.
	depPath := dep.internal_getPath()
	if depPath == "" || strings.HasPrefix(string(depPath), "/_runtime/unresolved") {
		// Should not happen if internalResolve succeeded without error.
		errMsg := fmt.Sprintf("internal error: FanIn could not get resolved path for dependency (wf: %s, base path: %s)", ctx.uuid, ctx.BasePath)
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New(errMsg), resultVal: *new(Out)}
		close(f.doneCh)
		return f
	}

	// Retrieve the specific executioner instance using its unique path from the registry.
	execInstance := ctx.registry.getExecutioner(depPath)
	if execInstance == nil {
		// Handle edge cases like 'Into' nodes which resolve directly without a standard executioner.
		// Return a future immediately resolved with the value obtained from internalResolve.
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: nil, resultVal: resolvedValue}
		close(f.doneCh)
		return f
	}

	// Create and return a future tied to the specific executioner instance.
	futureResult := newFuture[Out](execInstance)
	return futureResult
}

// --- 'Into' Nodes ---

// into is the internal implementation of ExecutionHandle used for handles created
// via Into() and IntoError(). It holds a direct value or error.
type into[Out any] struct {
	val  Out
	err  error
	path NodePath // Path assigned by internalResolve if used within a context.
}

// zero implements ExecutionHandle.
func (i *into[Out]) zero(Out) {}

// heartHandle implements ExecutionHandle.
func (i *into[Out]) heartHandle() {}

// internal_getPath implements ExecutionHandle. Returns a default path or one set by internalResolve.
func (i *into[Out]) internal_getPath() NodePath {
	if i.path == "" {
		if i.err != nil {
			return "/_source/intoError"
		}
		return "/_source/intoValue"
	}
	return i.path
}

// internal_getDefinition implements ExecutionHandle. Returns nil as 'into' nodes have no definition.
func (i *into[Out]) internal_getDefinition() any { return nil }

// internal_getInputSource implements ExecutionHandle. Returns nil as 'into' nodes have no input source.
func (i *into[Out]) internal_getInputSource() any { return nil }

// internal_out implements ExecutionHandle. Returns the stored value and error.
func (i *into[Out]) internal_out() (any, error) { return i.val, i.err }

// internal_setPath implements ExecutionHandle. Allows the framework to assign a more specific path.
func (i *into[Out]) internal_setPath(p NodePath) { i.path = p }

// Into creates an ExecutionHandle that immediately resolves to the provided value.
// Useful for injecting static data or results from outside the Heart framework
// into the execution graph.
func Into[Out any](val Out) ExecutionHandle[Out] { return &into[Out]{val: val, err: nil} }

// IntoError creates an ExecutionHandle that immediately resolves to the provided error.
// Useful for injecting pre-existing errors or terminating a graph branch.
func IntoError[Out any](err error) ExecutionHandle[Out] {
	if err == nil {
		// Ensure a non-nil error is always provided.
		err = errors.New("IntoError called with nil error")
	}
	return &into[Out]{val: *new(Out), err: err}
}

// Compile-time check for ExecutionHandle implementation.
var _ ExecutionHandle[any] = (*into[any])(nil)

// --- NewNode ---

// newNodeResolver is the internal NodeResolver implementation for nodes created
// with NewNode. It wraps the user-provided function.
type newNodeResolver[In, Out any] struct { // Takes In type for resolver consistency.
	fun    func(ctx NewNodeContext) ExecutionHandle[Out]
	nodeID NodeID // Base NodeID of the NewNode definition.
}

// genericNodeInitializer is a simple NodeInitializer used for internal node types
// like workflows and NewNode wrappers that don't require specific dependency injection.
type genericNodeInitializer struct{ id NodeTypeID }

// ID implements NodeInitializer.
func (g genericNodeInitializer) ID() NodeTypeID { return g.id }

// Init implements NodeResolver for newNodeResolver.
func (r *newNodeResolver[In, Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "system:newNodeWrapper"}
}

// Get implements NodeResolver for newNodeResolver. This method is executed when
// the NewNode *wrapper* node runs. It sets up the NewNodeContext, calls the
// user's function to define the subgraph, and then resolves the subgraph's result.
func (r *newNodeResolver[In, Out]) Get(runCtx context.Context, in In) (Out, error) { // Added In param.
	// Retrieve the runtime heart Context and unique execution path, which were added
	// to the Go context (`runCtx`) by the nodeExecution wrapper before calling Get.
	runtimeWfCtx, okWfCtx := runCtx.Value(heartContextKey{}).(Context)
	runtimeExecPath, okExecPath := runCtx.Value(execPathKey{}).(NodePath)

	if !okWfCtx || !okExecPath {
		// This signifies an internal framework error in context propagation.
		return *new(Out), fmt.Errorf("internal error: NewNode (%s) couldn't retrieve runtime context/path via context.Value. WfCtxOK: %v, ExecPathOK: %v", r.nodeID, okWfCtx, okExecPath)
	}

	// Construct the NewNodeContext for the user function.
	// Crucially, it uses a *new* executionRegistry scoped to this NewNode instance,
	// and its BasePath is the unique path of the NewNode wrapper itself.
	newNodeCtx := NewNodeContext{
		Context: Context{
			ctx:       runCtx,                 // Pass down the Go context.
			nodeCount: runtimeWfCtx.nodeCount, // Use runtime atomic counter.
			store:     runtimeWfCtx.store,     // Use runtime store instance.
			uuid:      runtimeWfCtx.uuid,      // Use runtime workflow UUID.
			registry:  runtimeWfCtx.registry,  // <<< Use the runtime registry for this scope!
			cancel:    runtimeWfCtx.cancel,    // Use runtime cancel function.
			BasePath:  runtimeExecPath,        // Base path for nodes defined inside is the wrapper's unique path.
		},
	}

	// --- Execute the User Function ---
	// This call defines the subgraph by returning a handle to its final node.
	subGraphHandle := r.fun(newNodeCtx)
	if subGraphHandle == nil {
		// The user function must return a valid handle.
		return *new(Out), fmt.Errorf("NewNode function for '%s' returned nil handle", newNodeCtx.BasePath)
	}

	// --- Resolve the Subgraph ---
	// Use internalResolve with the NewNodeContext to execute the subgraph.
	// This ensures nodes within the subgraph are resolved relative to the correct
	// BasePath and use the correct registry.
	resolvedVal, err := internalResolve(newNodeCtx.Context, subGraphHandle)
	if err != nil {
		// Wrap subgraph errors with the NewNode wrapper's path for context.
		return *new(Out), fmt.Errorf("error resolving NewNode sub-graph at '%s': %w", newNodeCtx.BasePath, err)
	}
	return resolvedVal, nil
}

// createExecution implements the ExecutionCreator interface for newNodeResolver.
// It creates the standard nodeExecution instance that will wrap the call to the
// newNodeResolver's Get method.
func (r *newNodeResolver[In, Out]) createExecution(
	execPath NodePath, // The unique path assigned to this NewNode wrapper instance.
	inputSourceAny any,
	wfCtx Context, // Runtime context of the parent scope.
	nodeID NodeID,
	nodeTypeID NodeTypeID,
	initializer NodeInitializer,
) (executioner, error) {
	// Assert the input handle type.
	var inputHandle ExecutionHandle[In]
	if inputSourceAny != nil {
		var ok bool
		inputHandle, ok = inputSourceAny.(ExecutionHandle[In])
		if !ok {
			return nil, fmt.Errorf("internal error: type assertion failed for newNode input source handle for %s: expected ExecutionHandle[%T], got %T", execPath, *new(In), inputSourceAny)
		}
	}
	// Create a standard node executioner using the newNodeResolver itself as the resolver.
	// Pass the unique execution path.
	ne := newExecution(
		inputHandle,
		wfCtx,    // Pass the correct runtime context.
		execPath, // Pass the unique path for this wrapper instance.
		nodeTypeID,
		initializer,
		r, // The newNodeResolver is the resolver for the wrapper execution.
	)
	return ne, nil
}

// Compile-time checks for newNodeResolver interfaces.
var (
	_ NodeResolver[any, any] = (*newNodeResolver[any, any])(nil)
	_ ExecutionCreator       = (*newNodeResolver[any, any])(nil)
)

// NewNode creates an ExecutionHandle for a dynamically defined subgraph.
// The provided function `fun` is executed lazily when the handle returned by
// NewNode is resolved (e.g., via FanIn or Execute). The function receives a
// NewNodeContext, which allows defining nodes scoped within the NewNode instance.
// The function must return an ExecutionHandle representing the final output of the subgraph.
//
// `ctx`: The Context from the scope where NewNode is called (e.g., a workflow handler).
// `nodeID`: A NodeID unique within the calling scope for this NewNode instance.
// `fun`: The function that defines the subgraph.
func NewNode[Out any](ctx Context, nodeID NodeID, fun func(ctx NewNodeContext) ExecutionHandle[Out]) ExecutionHandle[Out] {
	if fun == nil {
		panic("heart.NewNode requires a non-nil function")
	}
	if nodeID == "" {
		panic("heart.NewNode requires a non-empty node ID")
	}
	// Create the internal resolver. Use struct{} as the placeholder input type for the wrapper node.
	resolver := &newNodeResolver[struct{}, Out]{fun: fun, nodeID: nodeID}

	// Define the wrapper node blueprint using the standard DefineNode.
	// This definition gets its own instance counter.
	nodeDefinition := DefineNode(nodeID, resolver)

	// Start the wrapper node, creating its handle. Input is a dummy struct{}.
	// The handle doesn't have its final unique path assigned yet.
	wrapperHandle := nodeDefinition.Start(Into(struct{}{}))

	// Return the handle to the wrapper. Its path will be set, and its 'Get' method
	// (which executes 'fun' and resolves the subgraph) will be called when this
	// handle is resolved by the framework.
	return wrapperHandle
}

// --- Execute (Top-Level Trigger) ---

// Execute is the primary entry point for running a Heart workflow or node graph.
// It takes a Go context for cancellation, a handle to the final node/workflow
// of the graph, and optional WorkflowOptions (like WithStore, WithUUID).
//
// It initializes the execution context, including a unique workflow run UUID
// and an execution registry, and then triggers the lazy resolution of the
// provided handle using internalResolve. It waits for the entire graph connected
// to the handle to complete and returns the final output value or any error
// encountered during execution.
func Execute[Out any](ctx context.Context, handle ExecutionHandle[Out], opts ...WorkflowOption) (Out, error) {
	var zero Out // Zero value for error returns.
	if handle == nil {
		return zero, errors.New("Execute called with a nil handle")
	}

	// Apply workflow options, setting defaults for store and UUID.
	options := workflowOptions{store: defaultStore, uuid: WorkflowUUID(uuid.New())}
	for _, opt := range opts {
		opt(&options)
	}
	if options.store == nil {
		panic("Execute requires a non-nil store (default or provided via WithStore)")
	}

	// Create the cancellable Go context for this specific execution run.
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel() // Ensure cancellation propagates if Execute returns early.

	// Create the graph record in the store.
	graphErr := options.store.Graphs().CreateGraph(execCtx, options.uuid.String())
	if graphErr != nil {
		// If using WithExistingUUID, this might indicate the UUID is already in use.
		return zero, fmt.Errorf("failed to create graph record for workflow %s: %w", options.uuid, graphErr)
	}

	// Initialize the root execution Context.
	rootWorkflowCtx := Context{
		ctx:       execCtx,
		nodeCount: &atomic.Int64{}, // Shared counter (currently unused by path generation).
		store:     options.store,
		uuid:      options.uuid,
		registry:  newExecutionRegistry(), // A fresh registry for this run.
		BasePath:  "/",                    // Root execution starts at path "/".
		cancel:    cancel,                 // Pass the cancel function.
	}

	// Start the recursive resolution process from the root handle.
	result, err := internalResolve(rootWorkflowCtx, handle)

	// Check if the execution context was cancelled or timed out, even if no specific
	// node error occurred during resolution.
	if err == nil && execCtx.Err() != nil {
		err = fmt.Errorf("execution context cancelled or timed out: %w", execCtx.Err())
	}

	// TODO: Persist final graph status (e.g., success/failure) in the store?

	return result, err
}

// --- Internal context keys ---

// heartContextKey is used as a key for context.WithValue to pass the runtime heart.Context.
type heartContextKey struct{}

// execPathKey is used as a key for context.WithValue to pass the unique runtime NodePath.
type execPathKey struct{}

// --- Workflow Options ---

// defaultStore is the store used if WithStore is not provided to Execute.
var defaultStore store.Store = store.NewMemoryStore()

// workflowOptions holds configuration settings for a workflow execution run.
type workflowOptions struct {
	store store.Store
	uuid  WorkflowUUID
}

// WorkflowOption defines the signature for functions that modify workflowOptions.
type WorkflowOption func(*workflowOptions)

// WithStore provides a specific storage implementation (e.g., FileStore)
// to be used for the workflow run, overriding the default MemoryStore.
func WithStore(store store.Store) WorkflowOption {
	return func(wo *workflowOptions) {
		if store == nil {
			panic("WithStore provided with a nil store")
		}
		wo.store = store
	}
}

// WithUUID provides a specific UUID to be used for the workflow run identifier.
// If not provided, a new UUID is generated automatically.
func WithUUID(id WorkflowUUID) WorkflowOption { return func(wo *workflowOptions) { wo.uuid = id } }

// WithExistingUUID provides a specific UUID (as a string) to be used for the
// workflow run identifier. It panics if the string is not a valid UUID format.
// Useful for resuming or identifying specific runs. Ensure the UUID is unique
// or the storage layer can handle potential collisions if resuming.
func WithExistingUUID(id string) WorkflowOption {
	parsedUUID, err := uuid.Parse(id)
	if err != nil {
		panic(fmt.Sprintf("WithExistingUUID provided with invalid UUID string '%s': %v", id, err))
	}
	return func(wo *workflowOptions) { wo.uuid = parsedUUID }
}
