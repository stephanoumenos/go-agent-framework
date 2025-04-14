// ./workflow.go
package heart

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"heart/store" // Assuming store interfaces/errors are defined here

	"github.com/google/uuid"
)

// WorkflowUUID alias remains the same.
type WorkflowUUID = uuid.UUID

// Context struct remains the same.
type Context struct {
	ctx       context.Context
	nodeCount *atomic.Int64
	store     store.Store
	uuid      WorkflowUUID
	registry  *executionRegistry // Registry is per-workflow-instance
	BasePath  NodePath
	cancel    context.CancelFunc // Added cancel func
}

// Context methods remain the same...
func (c Context) Done() <-chan struct{}                   { return c.ctx.Done() }
func (c Context) Err() error                              { return c.ctx.Err() }
func (c Context) Value(key any) any                       { return c.ctx.Value(key) }
func (c Context) Deadline() (deadline time.Time, ok bool) { return c.ctx.Deadline() }

// internalResolve triggers/retrieves the result for a given execution handle.
// Called by FanIn/Future.Get and by the workflow handler's final step.
func internalResolve[Out any](c Context, handle ExecutionHandle[Out]) (Out, error) {
	var zero Out

	// --- Handle 'into' nodes directly ---
	outAny, err := handle.internal_out()
	// Check if internal_out provided a definitive value/error (not the "not applicable" errors)
	isNotApplicableError := false
	if err != nil {
		// Check against known "not applicable" errors explicitly if needed
		errMsg := err.Error()
		if strings.Contains(errMsg, "internal_out called on a standard node handle") ||
			strings.Contains(errMsg, "internal_out called on a WorkflowOutput handle") { // Add workflow handle check if needed
			isNotApplicableError = true
		}
	}

	if !isNotApplicableError {
		if err != nil {
			return zero, err
		} // Return error from 'into'
		typedOut, ok := outAny.(Out)
		if !ok {
			// Handle type assertion error for direct value
			return zero, fmt.Errorf("internalResolve: type assertion failed for direct handle result (path: %s): expected %T, got %T", handle.internal_getPath(), *new(Out), outAny)
		}
		return typedOut, nil
	}
	// --- End handle 'into' nodes ---

	// --- Handle standard atomic nodes via registry ---
	if c.registry == nil {
		panic(fmt.Sprintf("internal error: execution registry is nil in context for workflow %s (resolving path: %s)", c.uuid, handle.internal_getPath()))
	}

	// Get or create the executioner instance for the handle.
	execAny := getOrCreateExecution[Out](c.registry, handle, c) // registry handles lazy creation
	exec, okExec := execAny.(executioner)
	if !okExec {
		panic(fmt.Sprintf("internal error: registry returned non-executioner type %T for path %s", execAny, handle.internal_getPath()))
	}

	// Trigger/await execution via the executioner interface's getResult method.
	resultAny, execErr := exec.getResult() // This triggers sync.Once for nodeExecution
	if execErr != nil {
		return zero, execErr
	}

	// Type assert the 'any' result from getResult to the specific Out type.
	resultTyped, okAssert := resultAny.(Out)
	if !okAssert {
		return zero, fmt.Errorf("internalResolve: type assertion failed for node execution result (path: %s): expected %T, got %T", handle.internal_getPath(), *new(Out), resultAny)
	}
	return resultTyped, nil
}

// --- Workflow Definition ---

// HandlerFunc remains the same: takes Context, typed input, returns final ExecutionHandle.
type HandlerFunc[In, Out any] func(ctx Context, in In) ExecutionHandle[Out]

// WorkflowDefinition defines a workflow, started EAGERLY via New.
// It does NOT implement NodeDefinition.
type WorkflowDefinition[In, Out any] struct {
	handler HandlerFunc[In, Out]
	store   store.Store
	// defPath NodePath // Optional path/ID for the definition itself
}

// New starts a new workflow instance EAGERLY.
func (w WorkflowDefinition[In, Out]) New(parentCtx context.Context, in In) WorkflowResultHandle[Out] {
	workflowUUID := WorkflowUUID(uuid.New())
	result := &WorkflowOutput[Out]{ // Note: WorkflowOutput only needs Out generic now
		done: make(chan struct{}),
		uuid: workflowUUID,
		// value/err start as zero
	}

	// Create execution context based on parent context
	execCtx, cancel := context.WithCancel(parentCtx)

	// --- Eagerly Launch Workflow Handler Goroutine ---
	go func() {
		// Ensure done is closed and context is cancelled on exit/panic
		defer close(result.done)
		defer cancel()

		// Panic recovery for the entire workflow goroutine
		defer func() {
			if r := recover(); r != nil {
				panicErr := fmt.Errorf("panic recovered during workflow execution %s: %v", workflowUUID, r)
				result.err = panicErr
				result.value = *new(Out) // Ensure zero value on panic
			}
		}()

		// Create graph record in store
		graphErr := w.store.Graphs().CreateGraph(execCtx, workflowUUID.String())
		if graphErr != nil {
			result.err = fmt.Errorf("failed to create graph record for workflow %s: %w", workflowUUID, graphErr)
			result.value = *new(Out)
			return // Exit goroutine
		}

		// Create the root workflow context for this run.
		workflowCtx := Context{
			ctx:       execCtx, // Use the cancellable context for this run
			nodeCount: &atomic.Int64{},
			store:     w.store,
			uuid:      workflowUUID,
			registry:  newExecutionRegistry(), // Each workflow run gets its own registry
			BasePath:  "/",                    // Default root path
			cancel:    cancel,
		}

		// --- Execute the Handler ---
		// The handler defines the graph and returns the handle to the final node.
		finalNodeHandle := w.handler(workflowCtx, in) // Pass input value directly

		if finalNodeHandle == nil {
			result.err = fmt.Errorf("workflow handler returned a nil final node handle (workflow: %s)", workflowUUID)
			result.value = *new(Out)
			return
		}

		// --- Resolve Final Node's Output ---
		// Use internalResolve to trigger execution of the final node and its dependencies.
		finalValue, execErr := internalResolve[Out](workflowCtx, finalNodeHandle)

		// Store the final result or error in the WorkflowOutput handle.
		result.err = execErr
		if execErr == nil {
			result.value = finalValue // Store the successfully resolved value
		} else {
			result.value = *new(Out) // Ensure zero value on error
		}

		// Check for context cancellation after resolution but before signalling done.
		if execCtx.Err() != nil && result.err == nil {
			result.err = execCtx.Err() // Report context error if no primary execution error occurred
		}
		// Goroutine exits, deferred close(result.done) runs.
	}() // --- End Workflow Handler Goroutine ---

	return result // Return the WorkflowOutput handle immediately.
}

// --- Workflow Result Handle ---

// WorkflowOutput implements WorkflowResultHandle.
// It stores the final result computed by the eager background goroutine.
type WorkflowOutput[Out any] struct {
	value Out
	err   error
	done  chan struct{} // Closed by the workflow goroutine upon completion
	uuid  WorkflowUUID
}

// Out waits for the workflow to complete and returns the result.
// It respects the deadline/cancellation of the context passed to it *for the wait*.
func (r *WorkflowOutput[Out]) Out(ctx context.Context) (Out, error) {
	select {
	case <-r.done:
		// Workflow completed (successfully or with error)
		return r.value, r.err
	case <-ctx.Done():
		// Waiting was cancelled or timed out by the caller's context
		return *new(Out), fmt.Errorf("context cancelled while waiting for workflow result: %w", ctx.Err())
	}
}

// Done returns the workflow's completion channel.
func (r *WorkflowOutput[Out]) Done() <-chan struct{} {
	return r.done
}

// GetUUID returns the workflow instance's UUID.
func (r *WorkflowOutput[Out]) GetUUID() WorkflowUUID {
	return r.uuid
}

// Compile-time check for WorkflowResultHandle implementation.
var _ WorkflowResultHandle[any] = (*WorkflowOutput[any])(nil)

// --- DefineWorkflow Function ---
type workflowOptions struct{ store store.Store }
type WorkflowOption func(*workflowOptions)

func WithStore(store store.Store) WorkflowOption {
	return func(wo *workflowOptions) {
		if store == nil {
			panic("WithStore provided with a nil store")
		}
		wo.store = store
	}
}

var defaultStore store.Store = store.NewMemoryStore() // Ensure defaultStore is defined

// DefineWorkflow creates a WorkflowDefinition.
func DefineWorkflow[In, Out any](handler HandlerFunc[In, Out], options ...WorkflowOption) WorkflowDefinition[In, Out] {
	opts := workflowOptions{store: defaultStore}
	for _, option := range options {
		option(&opts)
	}
	if handler == nil {
		panic("DefineWorkflow requires a non-nil handler function")
	}
	if opts.store == nil {
		panic("DefineWorkflow requires a non-nil store")
	}

	return WorkflowDefinition[In, Out]{
		store:   opts.store,
		handler: handler,
	}
}
