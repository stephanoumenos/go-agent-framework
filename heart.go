// ./heart.go
package heart

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	// store "heart/store" // Keep if needed
)

// --- Core Identifiers ---
type NodeID string
type NodePath string

func JoinPath(base NodePath, id NodeID) NodePath {
	joined := path.Join(string(base), string(id))
	if base == "/" && id != "" && !strings.HasPrefix(joined, "/") {
		return NodePath("/" + joined)
	}
	return NodePath(joined)
}

// --- Execution Handle (Internal Graph Nodes) ---

// ExecutionHandle represents a handle to a lazily-initialized execution instance
// of an *atomic* NodeDefinition (created via DefineNode).
// It does NOT expose public Out/Done methods to trigger execution directly.
// Results are obtained internally via FanIn/Future.Get or internalResolve.
type ExecutionHandle[Out any] interface {
	// zero is needed for inference to work, even though it's unused. DON'T REMOVE THIS.
	zero(Out)
	// heartHandle is an internal marker method.
	heartHandle()
	// internal_getPath returns the unique path identifier for this execution handle.
	internal_getPath() NodePath
	// internal_getDefinition returns the underlying NodeDefinition blueprint (as any).
	internal_getDefinition() any // Returns NodeDefinition[In, Out] as any
	// internal_getInputSource returns the ExecutionHandle for the input (as any), or nil.
	internal_getInputSource() any // Returns ExecutionHandle[In] as any, or nil
	// internal_out provides direct access to the value for specific handle types
	// like 'into' nodes, bypassing the standard execution flow.
	internal_out() (any, error)
}

// Node is the user-facing alias for ExecutionHandle for atomic nodes within graph definitions.
// Consider removing this alias in future versions for clarity.
// type Node[Out any] = ExecutionHandle[Out] // <<< REMOVED/COMMENTED OUT

// --- Workflow Result Handle (Top-Level Workflow) ---

// WorkflowResultHandle represents a handle to a running workflow instance
// created via DefineWorkflow(...).New(...).
// It allows retrieving the final result using context-aware methods.
type WorkflowResultHandle[Out any] interface {
	// Out retrieves the final result of the workflow, blocking until completion.
	// It respects the provided context for cancellation/timeout during the wait.
	Out(ctx context.Context) (Out, error)
	// Done returns a channel that closes when the workflow execution completes
	// (successfully or with an error). This channel itself doesn't respect
	// the external context passed to Out().
	Done() <-chan struct{}
	// GetUUID returns the unique identifier for this workflow instance.
	GetUUID() WorkflowUUID
}

// --- Node Definition (Atomic Nodes) ---

// --- Node Definition (Atomic Nodes) ---
// NodeDefinition represents a blueprint for an *atomic* executable node.
// Created via DefineNode.
type NodeDefinition[In, Out any] interface {
	// heartDef is an internal marker method.
	heartDef()

	// Embed the capability to create an execution instance.
	ExecutionCreator

	// Start performs the initial setup for an execution instance. LAZY.
	Start(inputSource ExecutionHandle[In]) ExecutionHandle[Out]
	// internal_GetPath returns the base path associated with this definition.
	internal_GetPath() NodePath
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

func safeAssert[T any](val any) (T, bool) {
	typedVal, ok := val.(T)
	return typedVal, ok
}

// --- Future (Used by FanIn) ---
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
	resultAny, execErr := f.exec.getResult() // Blocks until executioner is done
	f.resultOnce.Do(func() {
		f.resultErr = execErr
		if execErr == nil {
			typedVal, ok := resultAny.(Out)
			if !ok {
				f.resultErr = fmt.Errorf("Future: type assertion failed: expected %T, got %T", *new(Out), resultAny)
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
func (f *Future[Out]) Out() (Out, error)     { return f.Get() } // Alias for convenience

// --- NewNodeContext ---
type NewNodeContext struct {
	Context // Embed the standard workflow context
}

// --- FanIn ---
func FanIn[Out any](ctx NewNodeContext, dep ExecutionHandle[Out]) *Future[Out] {
	if dep == nil {
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New("FanIn called with nil dependency handle")}
		close(f.doneCh)
		return f
	}
	// depPath := dep.internal_getPath() // Removed unused variable

	if ctx.registry == nil {
		errMsg := fmt.Sprintf("internal error: FanIn called with invalid context (nil registry) for dependency '%s' in node '%s' (wf: %s)", dep.internal_getPath(), ctx.BasePath, ctx.uuid)
		f := &Future[Out]{doneCh: make(chan struct{}), resultErr: errors.New(errMsg)}
		close(f.doneCh)
		return f
	}

	// Get or create the executioner instance for the dependency handle.
	// Explicitly provide the type parameter [Out].
	execInstance := getOrCreateExecution[Out](ctx.registry, dep, ctx.Context) // Pass handle type param

	futureResult := newFuture[Out](execInstance)
	return futureResult
}

// --- 'Into' Nodes ---
type into[Out any] struct {
	val Out
	err error
}

func (i *into[Out]) zero(Out)     {}
func (i *into[Out]) heartHandle() {}
func (i *into[Out]) internal_getPath() NodePath {
	if i.err != nil {
		return "/_source/intoError"
	}
	return "/_source/intoValue"
}
func (i *into[Out]) internal_getDefinition() any  { return nil }
func (i *into[Out]) internal_getInputSource() any { return nil }
func (i *into[Out]) internal_out() (any, error)   { return i.val, i.err }

func Into[Out any](val Out) ExecutionHandle[Out] { return &into[Out]{val: val, err: nil} }
func IntoError[Out any](err error) ExecutionHandle[Out] {
	if err == nil {
		err = errors.New("IntoError called with nil error")
	}
	return &into[Out]{val: *new(Out), err: err}
}

var _ ExecutionHandle[any] = (*into[any])(nil)

// --- NewNode ---
type newNodeResolver[Out any] struct {
	fun    func(ctx NewNodeContext) ExecutionHandle[Out]
	defCtx Context
}
type genericNodeInitializer struct{ id NodeTypeID }

func (g genericNodeInitializer) ID() NodeTypeID { return g.id }
func (r *newNodeResolver[Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "system:newNodeWrapper"}
}
func (r *newNodeResolver[Out]) Get(stdCtx context.Context, _ struct{}) (Out, error) {
	newNodeCtx := NewNodeContext{Context: r.defCtx}
	newNodeCtx.ctx = stdCtx
	subGraphHandle := r.fun(newNodeCtx)
	if subGraphHandle == nil {
		return *new(Out), fmt.Errorf("NewNode function for '%s' returned nil handle", newNodeCtx.BasePath)
	}
	resolvedVal, err := internalResolve[Out](newNodeCtx.Context, subGraphHandle) // internalResolve in workflow.go
	if err != nil {
		return *new(Out), fmt.Errorf("error resolving NewNode sub-graph at '%s': %w", newNodeCtx.BasePath, err)
	}
	return resolvedVal, nil
}

var _ NodeResolver[struct{}, any] = (*newNodeResolver[any])(nil) // NodeResolver in node.go

func NewNode[Out any](ctx Context, nodeID NodeID, fun func(ctx NewNodeContext) ExecutionHandle[Out]) ExecutionHandle[Out] {
	if fun == nil {
		panic("heart.NewNode requires a non-nil function")
	}
	if nodeID == "" {
		panic("heart.NewNode requires a non-empty node ID")
	}
	resolver := &newNodeResolver[Out]{fun: fun, defCtx: ctx}
	nodeDefinition := DefineNode[struct{}, Out](ctx, nodeID, resolver) // DefineNode in node.go
	wrapperHandle := nodeDefinition.Start(Into(struct{}{}))
	return wrapperHandle
}
