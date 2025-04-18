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

func JoinPath(base NodePath, id NodeID) NodePath {
	baseStr := string(base)
	if baseStr != "/" && !strings.HasSuffix(baseStr, "/") {
		baseStr += "/"
	}
	joined := path.Join(baseStr, string(id))
	if base == "/" && !strings.HasPrefix(joined, "/") && joined != "." {
		return NodePath("/" + joined)
	} else if joined == "." && base == "/" {
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
	internal_getPath() NodePath
	// internal_getDefinition returns the underlying NodeDefinition blueprint (as any).
	// Should return a type that implements definitionGetter.
	internal_getDefinition() any
	internal_getInputSource() any // Returns ExecutionHandle[In] as any, or nil
	internal_out() (any, error)
	internal_setPath(NodePath)
}

// --- Node Definition (Unified for Atomic Nodes and Workflows) ---

// NodeDefinition represents a blueprint for an *atomic* or *composite* (workflow) executable.
// Created via DefineNode. It holds the NodeID and the NodeResolver.
type NodeDefinition[In, Out any] interface {
	heartDef() // Internal marker method.

	// Start performs the initial setup for an execution instance. LAZY.
	// Runs DI checks via performInitialization.
	Start(inputSource ExecutionHandle[In]) ExecutionHandle[Out]
	// internal_GetNodeID returns the ID assigned at definition time.
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
	BasePath  NodePath           // The current path within the execution graph
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
	exec       executioner
	resultOnce sync.Once
	resultVal  Out
	resultErr  error
	doneCh     chan struct{}
}

func newFuture[Out any](exec executioner) *Future[Out] {
	f := &Future[Out]{exec: exec, doneCh: make(chan struct{})}
	go f.resolveInBackground()
	return f
}

func (f *Future[Out]) resolveInBackground() {
	f.resultOnce.Do(func() {
		resultAny, execErr := f.exec.getResult()
		f.resultErr = execErr
		if execErr == nil {
			typedVal, ok := resultAny.(Out)
			if !ok {
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
func FanIn[Out any](ctx NewNodeContext, dep ExecutionHandle[Out]) *Future[Out] {
	if dep == nil {
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New("FanIn called with nil dependency handle")}
		close(f.doneCh)
		return f
	}
	if ctx.registry == nil {
		errMsg := fmt.Sprintf("internal error: FanIn called with invalid context (nil registry) for node '%s' (wf: %s)", ctx.BasePath, ctx.uuid)
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New(errMsg)}
		close(f.doneCh)
		return f
	}
	resolvedValue, resolveErr := internalResolve[Out](ctx.Context, dep)
	if resolveErr != nil {
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: resolveErr, resultVal: *new(Out)}
		close(f.doneCh)
		return f
	}
	depPath := dep.internal_getPath()
	execInstance := ctx.registry.getExecutioner(depPath)
	if execInstance == nil {
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: nil, resultVal: resolvedValue}
		close(f.doneCh)
		return f
	}
	futureResult := newFuture[Out](execInstance)
	return futureResult
}

// --- 'Into' Nodes ---
// (Remains the same as previous correct version)
type into[Out any] struct {
	val  Out
	err  error
	path NodePath
}

func (i *into[Out]) zero(Out)     {}
func (i *into[Out]) heartHandle() {}
func (i *into[Out]) internal_getPath() NodePath {
	if i.path == "" {
		if i.err != nil {
			return "/_source/intoError"
		}
		return "/_source/intoValue"
	}
	return i.path
}
func (i *into[Out]) internal_getDefinition() any  { return nil }
func (i *into[Out]) internal_getInputSource() any { return nil }
func (i *into[Out]) internal_out() (any, error)   { return i.val, i.err }
func (i *into[Out]) internal_setPath(p NodePath)  { i.path = p }
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
type newNodeResolver[In, Out any] struct { // Note: newNodeResolver takes In type now for consistency
	fun    func(ctx NewNodeContext) ExecutionHandle[Out]
	defCtx Context // Context captured when NewNode was called
	nodeID NodeID  // ID of the NewNode itself
}
type genericNodeInitializer struct{ id NodeTypeID }

func (g genericNodeInitializer) ID() NodeTypeID { return g.id }
func (r *newNodeResolver[In, Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "system:newNodeWrapper"}
}
func (r *newNodeResolver[In, Out]) Get(runCtx context.Context, in In) (Out, error) { // Added In param
	newNodeCtx := NewNodeContext{
		Context: Context{
			ctx:       runCtx,
			nodeCount: r.defCtx.nodeCount,
			store:     r.defCtx.store,
			uuid:      r.defCtx.uuid,
			registry:  r.defCtx.registry,
			BasePath:  JoinPath(r.defCtx.BasePath, r.nodeID),
			cancel:    r.defCtx.cancel,
		},
	}
	subGraphHandle := r.fun(newNodeCtx)
	if subGraphHandle == nil {
		return *new(Out), fmt.Errorf("NewNode function for '%s' returned nil handle", newNodeCtx.BasePath)
	}
	resolvedVal, err := internalResolve[Out](newNodeCtx.Context, subGraphHandle)
	if err != nil {
		return *new(Out), fmt.Errorf("error resolving NewNode sub-graph at '%s': %w", newNodeCtx.BasePath, err)
	}
	return resolvedVal, nil
}

// --- NewNode ExecutionCreator Implementation ---
func (r *newNodeResolver[In, Out]) createExecution(
	execPath NodePath,
	inputSourceAny any,
	wfCtx Context,
	nodeID NodeID,
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
	// Use newExecution for the newNode wrapper itself
	ne := newExecution[In, Out](
		inputHandle,
		wfCtx,
		execPath,
		nodeTypeID,
		initializer,
		r, // Pass the resolver itself
	)
	return ne, nil
}

var _ NodeResolver[any, any] = (*newNodeResolver[any, any])(nil)
var _ ExecutionCreator = (*newNodeResolver[any, any])(nil)

func NewNode[Out any](ctx Context, nodeID NodeID, fun func(ctx NewNodeContext) ExecutionHandle[Out]) ExecutionHandle[Out] {
	if fun == nil {
		panic("heart.NewNode requires a non-nil function")
	}
	if nodeID == "" {
		panic("heart.NewNode requires a non-empty node ID")
	}
	// Use struct{} as input type for the wrapper node
	resolver := &newNodeResolver[struct{}, Out]{fun: fun, defCtx: ctx, nodeID: nodeID}
	nodeDefinition := DefineNode[struct{}, Out](nodeID, resolver)
	wrapperHandle := nodeDefinition.Start(Into(struct{}{}))
	wrapperHandle.internal_setPath(JoinPath(ctx.BasePath, nodeID))
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
		registry:  newExecutionRegistry(),
		BasePath:  "/",
		cancel:    cancel,
	}
	result, err := internalResolve[Out](rootWorkflowCtx, handle)
	if err == nil && execCtx.Err() != nil {
		err = fmt.Errorf("execution context cancelled or timed out: %w", execCtx.Err())
	}
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
