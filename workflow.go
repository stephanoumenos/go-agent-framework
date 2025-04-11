// ./workflow.go
package heart

import (
	"context"
	"fmt"
	"heart/store"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

var defaultStore = store.NewMemoryStore()

// WorkflowOutput holds the eventual result of a workflow execution.
// It implements the Output[Out] interface.
type WorkflowOutput[Out any] struct {
	value Out
	err   error
	done  chan struct{}
}

// Out blocks until the workflow completes and returns the final output and error.
func (r *WorkflowOutput[Out]) Out() (Out, error) {
	<-r.done // Wait for the workflow goroutine to signal completion
	return r.value, r.err
}

// Done returns a channel that is closed when the workflow completes.
func (r *WorkflowOutput[Out]) Done() <-chan struct{} {
	return r.done
}

// Ensure WorkflowOutput implements Output interface.
var _ Output[any] = (*WorkflowOutput[any])(nil)

// WorkflowDefinition holds the definition of a workflow, including its handler and store.
type WorkflowDefinition[In, Out any] struct {
	handler HandlerFunc[In, Out]
	store   store.Store
}

// New starts a new workflow instance in a separate goroutine and returns a WorkflowOutput immediately.
// The WorkflowOutput can be used to block and wait for the final output using the Out method,
// or check for completion using the Done channel.
// It accepts a parent context which will be used for the workflow execution.
func (w WorkflowDefinition[In, Out]) New(parentCtx context.Context, in In) Output[Out] {
	workflowUUID := WorkflowUUID(uuid.New())

	result := &WorkflowOutput[Out]{
		done: make(chan struct{}),
	}

	execCtx, cancel := context.WithCancel(parentCtx)

	// Create the graph entry immediately.
	err := w.store.Graphs().CreateGraph(execCtx, workflowUUID.String())
	if err != nil {
		result.err = fmt.Errorf("failed to create graph record for workflow %s: %w", workflowUUID, err)
		close(result.done) // Signal immediate completion (with error)
		cancel()
		return result
	}

	// Create the initial root context for the workflow.
	workflowCtx := Context{
		ctx:       execCtx, // Use the cancellable execution context
		nodeCount: &atomic.Int64{},
		store:     w.store,
		uuid:      workflowUUID,
		scheduler: newWorkflowScheduler(),
		BasePath:  "/", // Initialize BasePath for the root context
		cancel:    cancel,
	}

	// Run the actual workflow execution in a goroutine.
	go func() {
		// Ensure the done channel is closed and context is cancelled on exit.
		defer close(result.done)
		defer cancel()

		// Execute the handler and get the final output handle.
		finalOutput := w.handler(workflowCtx, in)

		// Call Out() on the final Output returned by the handler.
		// This blocks until the entire workflow graph resolves.
		finalValue, execErr := finalOutput.Out()

		// Store the result.
		result.value = finalValue
		result.err = execErr

		// Check if the execution context was cancelled.
		if execCtx.Err() != nil && execErr == nil {
			result.err = execCtx.Err()
		}
	}()

	return result // Return the WorkflowOutput handle immediately.
}

// WorkflowUUID is an alias for uuid.UUID for clarity.
type WorkflowUUID = uuid.UUID

// Context carries workflow execution state and context, including hierarchical path information.
type Context struct {
	ctx       context.Context // The standard Go context
	nodeCount *atomic.Int64
	store     store.Store
	uuid      WorkflowUUID
	scheduler *workflowScheduler
	BasePath  NodePath // The base path for nodes defined within this context
	cancel    context.CancelFunc
}

// Done returns a channel that's closed when the workflow execution context is cancelled.
func (c Context) Done() <-chan struct{} {
	return c.ctx.Done()
}

// Err returns the reason for the context cancellation after Done is closed.
func (c Context) Err() error {
	return c.ctx.Err()
}

// Value returns the value associated with this context for key, or nil.
func (c Context) Value(key any) any {
	return c.ctx.Value(key)
}

// Deadline returns the time when work done on behalf of this context should be canceled.
func (c Context) Deadline() (deadline time.Time, ok bool) {
	return c.ctx.Deadline()
}

// HandlerFunc defines the signature for user-provided workflow logic functions.
// It now returns Output[Out].
type HandlerFunc[In, Out any] func(Context, In) Output[Out]

type workflowOptions struct {
	store store.Store
}

// WorkflowOption is a functional option for configuring workflow definitions.
type WorkflowOption func(*workflowOptions)

// WithStore provides a storage backend for the workflow definition.
func WithStore(store store.Store) WorkflowOption {
	return func(wo *workflowOptions) {
		wo.store = store
	}
}

// DefineWorkflow creates a WorkflowDefinition with the specified handler and options.
func DefineWorkflow[In, Out any](handler HandlerFunc[In, Out], options ...WorkflowOption) WorkflowDefinition[In, Out] {
	var opts workflowOptions
	for _, option := range options {
		option(&opts)
	}

	store := opts.store
	if store == nil {
		store = defaultStore // Default to in-memory store
	}

	return WorkflowDefinition[In, Out]{
		store:   store,
		handler: handler,
	}
}
