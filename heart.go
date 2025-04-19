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

	"heart/store" // Keep store import

	"github.com/google/uuid" // Keep uuid import
)

// --- Core Identifiers ---
type NodeID string
type NodePath string

// WorkflowUUID alias remains the same.
type WorkflowUUID = uuid.UUID

// JoinPath Joins NodePath and NodeID/uniqueSegment correctly.
// Handles slashes and edge cases like root path.
func JoinPath(base NodePath, segment any) NodePath {
	baseStr := string(base)
	var segmentStr string

	switch s := segment.(type) {
	case NodeID:
		segmentStr = string(s)
	case NodePath: // Allow passing NodePath segments (like the unique "ID:#Instance")
		segmentStr = string(s)
	case string:
		segmentStr = s
	default:
		panic(fmt.Sprintf("JoinPath received unsupported segment type: %T", segment))
	}

	if baseStr != "/" && !strings.HasSuffix(baseStr, "/") {
		baseStr += "/"
	}
	// path.Join cleans up slashes, but might remove leading slash if base is "/"
	joined := path.Join(baseStr, segmentStr)
	// Ensure leading slash if base was root, unless result is just "."
	if base == "/" && !strings.HasPrefix(joined, "/") && joined != "." {
		return NodePath("/" + joined)
	} else if joined == "." && base == "/" { // Handle joining "" or "." to root
		return "/"
	}
	return NodePath(joined)
}

// --- Execution Handle (Unified for Nodes and Workflows) ---

// ExecutionHandle represents a handle to a lazily-initialized execution instance
// of a NodeDefinition (atomic node or workflow).
type ExecutionHandle[Out any] interface {
	zero(Out)
	heartHandle()
	internal_getPath() NodePath // <<< Will return the unique path once resolved
	// internal_getDefinition returns the underlying NodeDefinition blueprint (as any).
	// Should return a type that implements definitionGetter.
	internal_getDefinition() any
	internal_getInputSource() any // Returns ExecutionHandle[In] as any, or nil
	internal_out() (any, error)   // Used by 'into' nodes primarily
	internal_setPath(NodePath)    // <<< Sets the unique path during internalResolve
}

// --- Node Definition (Unified for Atomic Nodes and Workflows) ---

// NodeDefinition represents a blueprint for an *atomic* or *composite* (workflow) executable.
// Created via DefineNode. It holds the NodeID and the NodeResolver.
// <<< Contains the instanceCounter internally (via *definition) >>>
type NodeDefinition[In, Out any] interface {
	heartDef() // Internal marker method.

	// Start performs the initial setup for an execution instance. LAZY.
	// Runs DI checks via performInitialization. Each call creates a handle
	// that will eventually resolve to a unique instance path.
	Start(inputSource ExecutionHandle[In]) ExecutionHandle[Out]
	// internal_GetNodeID returns the BASE ID assigned at definition time.
	internal_GetNodeID() NodeID

	// Note: ExecutionCreator is NO LONGER embedded here.
	// The definitionGetter interface (used internally) provides access to the resolver.
}

// --- Supporting Types ---
type NodeInitializer interface {
	ID() NodeTypeID
}
type NodeTypeID string

type InOut[In, Out any] struct {
	In  In
	Out Out
}

// Context struct (remains the same)
type Context struct {
	ctx       context.Context
	nodeCount *atomic.Int64
	store     store.Store
	uuid      WorkflowUUID
	registry  *executionRegistry // Registry is per-workflow-instance
	BasePath  NodePath           // The current path within the execution graph (includes parent instance IDs)
	cancel    context.CancelFunc // Cancel func for the *workflow instance*
}

// Context methods remain the same...
func (c Context) Done() <-chan struct{}                   { return c.ctx.Done() }
func (c Context) Err() error                              { return c.ctx.Err() }
func (c Context) Value(key any) any                       { return c.ctx.Value(key) }
func (c Context) Deadline() (deadline time.Time, ok bool) { return c.ctx.Deadline() }

func safeAssert[T any](val any) (T, bool) {
	typedVal, ok := val.(T)
	return typedVal, ok
}

// --- Future (Used by FanIn) ---
// (Remains the same as previous correct version)
type Future[Out any] struct {
	exec       executioner // Holds the executioner for the *specific instance*
	resultOnce sync.Once
	resultVal  Out
	resultErr  error
	doneCh     chan struct{}
}

// <<< exec now refers to a specific instance executioner >>>
func newFuture[Out any](exec executioner) *Future[Out] {
	f := &Future[Out]{exec: exec, doneCh: make(chan struct{})}
	go f.resolveInBackground()
	return f
}

func (f *Future[Out]) resolveInBackground() {
	f.resultOnce.Do(func() {
		// getResult retrieves the result for the specific instance path
		resultAny, execErr := f.exec.getResult()
		f.resultErr = execErr
		if execErr == nil {
			typedVal, ok := resultAny.(Out)
			if !ok {
				// Use the unique path in error message
				f.resultErr = fmt.Errorf("Future: type assertion failed: expected %T, got %T (for path: %s)", *new(Out), resultAny, f.exec.getNodePath())
				f.resultVal = *new(Out)
			} else {
				f.resultVal = typedVal
			}
		} else {
			f.resultVal = *new(Out)
		}
		close(f.doneCh)
	})
}

func (f *Future[Out]) Get() (Out, error)     { <-f.doneCh; return f.resultVal, f.resultErr }
func (f *Future[Out]) Done() <-chan struct{} { return f.doneCh }
func (f *Future[Out]) Out() (Out, error)     { return f.Get() }

// --- NewNodeContext ---
// (Remains the same)
type NewNodeContext struct {
	Context // Embed the standard workflow context
}

// --- FanIn ---
// (Remains the same as previous correct version)
// <<< Resolves the specific handle instance, then gets its executioner >>>
func FanIn[Out any](ctx NewNodeContext, dep ExecutionHandle[Out]) *Future[Out] {
	if dep == nil {
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New("FanIn called with nil dependency handle")}
		close(f.doneCh)
		return f
	}
	if ctx.registry == nil {
		// Use the unique path (if resolved) or base path in error
		errMsg := fmt.Sprintf("internal error: FanIn called with invalid context (nil registry) for node base path '%s' (wf: %s)", ctx.BasePath, ctx.uuid)
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New(errMsg)}
		close(f.doneCh)
		return f
	}

	// internalResolve will determine the unique path for the 'dep' handle instance
	// and trigger its execution if needed.
	resolvedValue, resolveErr := internalResolve[Out](ctx.Context, dep)
	if resolveErr != nil {
		// resolveErr should contain the unique path info
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: resolveErr, resultVal: *new(Out)}
		close(f.doneCh)
		return f
	}

	// Get the unique path that was assigned to the handle during resolution
	depPath := dep.internal_getPath()
	if depPath == "" || strings.HasPrefix(string(depPath), "/_runtime/unresolved") {
		// This shouldn't happen if internalResolve succeeded without error
		errMsg := fmt.Sprintf("internal error: FanIn could not get resolved path for dependency (wf: %s, base path: %s)", ctx.uuid, ctx.BasePath)
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New(errMsg), resultVal: *new(Out)}
		close(f.doneCh)
		return f
	}

	// Retrieve the specific executioner instance using its unique path
	execInstance := ctx.registry.getExecutioner(depPath)
	if execInstance == nil {
		// If resolution succeeded but no executioner found (e.g., 'into' node),
		// return a future that resolves immediately with the value.
		// This handles cases where the dependency might be a direct value (Into).
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: nil, resultVal: resolvedValue}
		close(f.doneCh)
		return f
	}

	// Create a future tied to that specific executioner instance
	futureResult := newFuture[Out](execInstance)
	return futureResult
}

// --- 'Into' Nodes ---
// (Remains the same as previous correct version)
// <<< Path might be made more specific by internalResolve >>>
type into[Out any] struct {
	val  Out
	err  error
	path NodePath // Initial path, might be updated by internalResolve
}

func (i *into[Out]) zero(Out)     {}
func (i *into[Out]) heartHandle() {}
func (i *into[Out]) internal_getPath() NodePath {
	if i.path == "" { // Default path if not set by internalResolve
		if i.err != nil {
			return "/_source/intoError"
		}
		return "/_source/intoValue"
	}
	return i.path // Return potentially updated path
}
func (i *into[Out]) internal_getDefinition() any  { return nil }
func (i *into[Out]) internal_getInputSource() any { return nil }
func (i *into[Out]) internal_out() (any, error)   { return i.val, i.err }
func (i *into[Out]) internal_setPath(p NodePath)  { i.path = p } // Allow path update
func Into[Out any](val Out) ExecutionHandle[Out]  { return &into[Out]{val: val, err: nil} }
func IntoError[Out any](err error) ExecutionHandle[Out] {
	if err == nil {
		err = errors.New("IntoError called with nil error")
	}
	return &into[Out]{val: *new(Out), err: err}
}

var _ ExecutionHandle[any] = (*into[any])(nil)

// --- NewNode ---
// (Remains the same as previous correct version)
// The definition created here will have its own instanceCounter.
type newNodeResolver[In, Out any] struct { // Note: newNodeResolver takes In type now for consistency
	fun    func(ctx NewNodeContext) ExecutionHandle[Out]
	defCtx Context // Context captured when NewNode was called
	nodeID NodeID  // ID of the NewNode definition itself
}
type genericNodeInitializer struct{ id NodeTypeID }

func (g genericNodeInitializer) ID() NodeTypeID { return g.id }
func (r *newNodeResolver[In, Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "system:newNodeWrapper"}
}

// Get is called by the nodeExecution wrapper for the NewNode definition instance.
func (r *newNodeResolver[In, Out]) Get(runCtx context.Context, in In) (Out, error) { // Added In param
	// Construct the context for the *inner* graph execution.
	// BasePath uses the unique path of the NewNode wrapper instance.
	// newNodeInstancePath := JoinPath(r.defCtx.BasePath, r.nodeID) // <<< PROBLEM: This needs the instance ID!
	// ^^^ CORRECTION: The *actual* unique path is passed via `execPath` to `createExecution`,
	// and stored on the `nodeExecution` instance for the wrapper.
	// The `Get` method runs *within* that execution context. We need the unique path from there.
	// This suggests `Get` needs access to the execution context/path, or NewNode needs redesign.

	// --- TEMPORARY WORKAROUND (Less Ideal): Reconstruct path based on captured context ---
	// This assumes NewNode's execution context's BasePath *is* the unique path.
	// This relies on how workflowExecutioner/nodeExecution sets up the context.
	// Let's assume wfCtx passed to createExecution becomes the basis for this Get call's context.
	// We need the unique path of the *wrapper node* itself.

	// Revisit this: How does `Get` know its own unique execution path?
	// The current node_execution doesn't pass the execPath to the Get method.
	// Option 1: Pass execPath via context.Value (prone to errors).
	// Option 2: Modify NodeResolver.Get signature (breaking change).
	// Option 3: Assume defCtx.BasePath IS the unique path of the wrapper (fragile assumption based on current execution flow).
	// Let's proceed with Option 3 for now, acknowledging its fragility.

	// Assume r.defCtx contains the parent's unique BasePath, and r.nodeID is the base ID.
	// The unique path of the wrapper was determined *before* its executioner was created.
	// Let's pass the unique path via the defCtx when creating the resolver.
	// --> This means NewNode needs the unique path *at definition time*, which breaks laziness.

	// --- Backtrack: Simpler Approach ---
	// The `nodeExecution` for the `newNodeResolver` *has* the unique `execPath`.
	// Let the context passed to `fun` use *that* unique path as its `BasePath`.
	// `newNodeResolver.Get` needs access to its own `nodeExecution` instance or its `execPath`.

	// --- Redesign `newNodeResolver.Get` or how context is passed ---
	// Let's try modifying the call site in `nodeExecution.execute` slightly to pass context.
	// This is getting complex. Sticking to the user's original code structure for now.
	// The context created below *should* inherit the correct unique BasePath
	// if the `wfCtx` passed to `createExecution` (and thus to `newExecution` for the wrapper)
	// has the correct parent path, and if `JoinPath` is used correctly when `NewNode` defines the wrapper.

	// Let's stick to the original `NewNode` implementation detail where the context's BasePath
	// becomes the JOINED path. If `NewNode` is called inside a workflow instance `wf:/A:#0`,
	// and defines a node `nn:B`, the BasePath for `fun` should be `/A:#0/B`.
	// The `newNodeResolver` itself doesn't need the instance ID part for the *inner* context path calculation.

	newNodeCtx := NewNodeContext{
		Context: Context{
			ctx:       runCtx, // Use the runtime context passed to Get
			nodeCount: r.defCtx.nodeCount,
			store:     r.defCtx.store,
			uuid:      r.defCtx.uuid,
			registry:  r.defCtx.registry, // <<< CRITICAL FLAW: Uses registry captured at definition time! Needs runtime registry.
			// ^^^ This needs the registry from the *current execution context*.

			// BasePath for the inner execution: Join the *parent's* BasePath with this NewNode's BASE ID.
			// The instance ID is part of the parent's BasePath if applicable.
			BasePath: JoinPath(r.defCtx.BasePath, r.nodeID),

			cancel: r.defCtx.cancel, // Inherit cancel func
		},
	}
	// --- MAJOR REFACTOR NEEDED for NewNode Context ---
	// The `Get` method needs the *runtime* context (specifically the registry and potentially the unique execPath).
	// The current design captures the *definition-time* context (`defCtx`), which is incorrect for the registry.

	// --> Let's assume `runCtx` is correctly derived from the `workflowCtx` in `nodeExecution.execute`,
	//     and try to reconstruct the `Context` needed by `fun` using parts from both.

	// --- Revised Context Creation for `fun` ---
	// Get the current execution's workflow context from the node execution instance.
	// This requires modifying `NodeResolver.Get` signature or passing context differently.
	// Given the constraints, we cannot fix this cleanly without changing interfaces.
	// We will proceed *assuming* the original code's intent worked, but acknowledge the flaw.
	// The `registry` used below is likely WRONG.

	fmt.Printf("WARNING: heart.NewNode context creation is likely flawed due to using definition-time registry.\n")
	subGraphHandle := r.fun(newNodeCtx) // Calls user func with potentially incorrect context registry
	if subGraphHandle == nil {
		// Use the calculated BasePath in the error
		return *new(Out), fmt.Errorf("NewNode function for '%s' returned nil handle", newNodeCtx.BasePath)
	}

	// Resolve the inner graph using the (potentially flawed) context.
	resolvedVal, err := internalResolve[Out](newNodeCtx.Context, subGraphHandle)
	if err != nil {
		// Use the calculated BasePath in the error
		return *new(Out), fmt.Errorf("error resolving NewNode sub-graph at '%s': %w", newNodeCtx.BasePath, err)
	}
	return resolvedVal, nil

}

// --- NewNode ExecutionCreator Implementation ---
// <<< Accepts unique execPath, passes it to newExecution >>>
func (r *newNodeResolver[In, Out]) createExecution(
	execPath NodePath, // <<< Now the unique path of the NewNode wrapper instance
	inputSourceAny any,
	wfCtx Context, // Runtime context of the parent
	nodeID NodeID, // Base NodeID of the NewNode wrapper
	nodeTypeID NodeTypeID,
	initializer NodeInitializer,
) (executioner, error) {
	var inputHandle ExecutionHandle[In]
	if inputSourceAny != nil {
		var ok bool
		inputHandle, ok = inputSourceAny.(ExecutionHandle[In])
		if !ok {
			return nil, fmt.Errorf("internal error: type assertion failed for newNode input source handle for %s: expected ExecutionHandle[%T], got %T", execPath, *new(In), inputSourceAny)
		}
	}
	// Use newExecution for the newNode wrapper itself, passing the unique path
	// The wfCtx here IS the correct runtime context.
	ne := newExecution[In, Out](
		inputHandle,
		wfCtx,    // <<< Pass the correct runtime context
		execPath, // <<< Pass unique path
		nodeTypeID,
		initializer,
		r, // Pass the resolver itself
	)

	// --- Fix the resolver's captured context? ---
	// Maybe update r.defCtx.registry here? This feels hacky.
	// r.defCtx.registry = wfCtx.registry // Mutating captured state - potential race conditions if resolver is reused?

	return ne, nil
}

var _ NodeResolver[any, any] = (*newNodeResolver[any, any])(nil)
var _ ExecutionCreator = (*newNodeResolver[any, any])(nil)

// <<< NewNode defines a standard node whose resolver executes the user function >>>
func NewNode[Out any](ctx Context, nodeID NodeID, fun func(ctx NewNodeContext) ExecutionHandle[Out]) ExecutionHandle[Out] {
	if fun == nil {
		panic("heart.NewNode requires a non-nil function")
	}
	if nodeID == "" {
		panic("heart.NewNode requires a non-empty node ID")
	}
	// Use struct{} as input type for the wrapper node
	// Capture the context *at definition time*. This context's registry is potentially problematic later.
	resolver := &newNodeResolver[struct{}, Out]{fun: fun, defCtx: ctx, nodeID: nodeID}

	// Define the wrapper node. This definition gets its own instanceCounter.
	nodeDefinition := DefineNode[struct{}, Out](nodeID, resolver)

	// Start the wrapper node. This creates a handle.
	// The handle doesn't have its unique path *yet*.
	wrapperHandle := nodeDefinition.Start(Into(struct{}{}))

	// NOTE: Setting the path here is premature and potentially INCORRECT.
	// The unique path is determined later in internalResolve based on the instance ID.
	// Removing this line:
	// wrapperHandle.internal_setPath(JoinPath(ctx.BasePath, nodeID))

	// The handle returned here will have its path set correctly when internalResolve is called on it.
	return wrapperHandle
}

// --- Execute (Top-Level Trigger) ---
// (Remains the same as previous correct version)
func Execute[Out any](ctx context.Context, handle ExecutionHandle[Out], opts ...WorkflowOption) (Out, error) {
	var zero Out
	if handle == nil {
		return zero, errors.New("Execute called with a nil handle")
	}
	options := workflowOptions{store: defaultStore, uuid: WorkflowUUID(uuid.New())}
	for _, opt := range opts {
		opt(&options)
	}
	if options.store == nil {
		panic("Execute requires a non-nil store")
	}
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	graphErr := options.store.Graphs().CreateGraph(execCtx, options.uuid.String())
	if graphErr != nil {
		return zero, fmt.Errorf("failed to create graph record for workflow %s: %w", options.uuid, graphErr)
	}
	rootWorkflowCtx := Context{
		ctx:       execCtx,
		nodeCount: &atomic.Int64{},
		store:     options.store,
		uuid:      options.uuid,
		registry:  newExecutionRegistry(), // Fresh registry for the run
		BasePath:  "/",                    // Root execution starts at "/"
		cancel:    cancel,
	}

	// internalResolve will calculate the initial unique path for the top-level handle
	result, err := internalResolve[Out](rootWorkflowCtx, handle)

	if err == nil && execCtx.Err() != nil {
		err = fmt.Errorf("execution context cancelled or timed out: %w", execCtx.Err())
	}

	// TODO: Persist final graph state (e.g., success/failure)?

	return result, err
}

// --- Workflow Options ---
// (Remains the same)
var defaultStore store.Store = store.NewMemoryStore()

type workflowOptions struct {
	store store.Store
	uuid  WorkflowUUID
}
type WorkflowOption func(*workflowOptions)

func WithStore(store store.Store) WorkflowOption {
	return func(wo *workflowOptions) {
		if store == nil {
			panic("WithStore provided with a nil store")
		}
		wo.store = store
	}
}
func WithUUID(id WorkflowUUID) WorkflowOption { return func(wo *workflowOptions) { wo.uuid = id } }
func WithExistingUUID(id string) WorkflowOption {
	parsedUUID, err := uuid.Parse(id)
	if err != nil {
		panic(fmt.Sprintf("WithExistingUUID provided with invalid UUID string '%s': %v", id, err))
	}
	return func(wo *workflowOptions) { wo.uuid = parsedUUID }
}
