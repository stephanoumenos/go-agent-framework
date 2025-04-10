package heart

import (
	"context"
	"fmt"
	"heart/store"
	"io"
	"time"

	"sync/atomic"

	"github.com/google/uuid"
)

var defaultStore = store.NewMemoryStore()

// WorkflowResult holds the eventual result of a workflow execution.
// It allows retrieving the result blockingly or checking for completion via a channel.
type WorkflowResult[Out any] struct {
	value Out
	err   error
	done  chan struct{}
}

// Get blocks until the workflow completes and returns the final output and error.
// Reads are safe after <-r.done because the writes happened before close(r.done).
func (r *WorkflowResult[Out]) Get() (Out, error) {
	<-r.done // Wait for the workflow goroutine to signal completion
	return r.value, r.err
}

// Done returns a channel that is closed when the workflow completes.
// Successive calls to Done return the same channel.
func (r *WorkflowResult[Out]) Done() <-chan struct{} {
	return r.done
}

// CancelFunc allows cancelling the workflow execution context.
// type CancelFunc context.CancelFunc

type WorkflowDefinition[In, Out any] struct {
	handler HandlerFunc[In, Out]
	store   store.Store
}

// New starts a new workflow instance in a separate goroutine and returns a WorkflowResult immediately.
// The WorkflowResult can be used to block and wait for the final output using the Get method,
// or check for completion using the Done channel.
// It also accepts a parent context which will be used for the workflow execution.
func (w WorkflowDefinition[In, Out]) New(parentCtx context.Context, in In) *WorkflowResult[Out] {
	workflowUUID := WorkflowUUID(uuid.New())

	result := &WorkflowResult[Out]{
		done: make(chan struct{}),
	}

	// Create a new context for this specific workflow run, derived from the parent.
	// This allows cancellation of individual workflow runs.
	// TODO: Consider adding timeout options to DefineWorkflow or New.
	execCtx, cancel := context.WithCancel(parentCtx)
	// We don't return the cancel func directly from New, but it's used internally.
	// If cancellation is needed from outside, the parentCtx should be cancelled.

	// Create the graph entry immediately.
	// Use the execution context `execCtx` for the store operation.
	err := w.store.Graphs().CreateGraph(execCtx, workflowUUID.String())
	if err != nil {
		// If we can't create the graph record, fail fast.
		result.err = fmt.Errorf("failed to create graph record for workflow %s: %w", workflowUUID, err)
		close(result.done) // Signal immediate completion (with error)
		cancel()           // Cancel the derived context as we are aborting
		return result
	}

	// Create the initial root context for the workflow.
	workflowCtx := Context{
		ctx:       execCtx, // Use the cancellable execution context
		nodeCount: &atomic.Int64{},
		store:     w.store,
		uuid:      workflowUUID,
		scheduler: newWorkflowScheduler(),
		// start: make(chan struct{}), // 'start' channel seems unused, consider removing
		BasePath: "/",    // Initialize BasePath for the root context
		cancel:   cancel, // Store cancel func for potential internal use (e.g., cleanup)
	}

	// Run the actual workflow execution in a goroutine.
	go func() {
		// Ensure the done channel is closed on exit, signalling completion or error.
		// Also ensure the execution context is cancelled if not already done.
		defer close(result.done)
		defer cancel() // Ensure context is cancelled on handler completion/panic

		// Execute the handler and get the final output Noder.
		// The handler receives the context with the initial BasePath.
		finalOutputer := w.handler(workflowCtx, in)

		// Call Out() on the final Outputer returned by the handler.
		// This blocks until the entire workflow graph leading to the final output resolves.
		// Use the execution context `execCtx` for the blocking Out() call.
		// finalValue, execErr := finalOutputer.Out(execCtx) // If Out needed context
		finalValue, execErr := finalOutputer.Out() // Updated Out() signature

		// Store the result directly. Reads in Get() after <-done will see these writes.
		result.value = finalValue
		result.err = execErr

		// Check if the execution context was cancelled during the handler execution
		if execCtx.Err() != nil && execErr == nil {
			// If the context was cancelled but the handler didn't return an error,
			// set the result error to the context error (e.g., context.Canceled).
			result.err = execCtx.Err()
		}

	}()

	return result // Return the result handle immediately.
}

type WorkflowUUID = uuid.UUID

// Context carries workflow execution state and context.
// It now includes BasePath to support hierarchical node identification.
type Context struct {
	ctx       context.Context // The standard Go context for this workflow/node execution
	nodeCount *atomic.Int64
	store     store.Store
	uuid      WorkflowUUID
	// start chan struct{} // Seems unused, removed for now
	scheduler *workflowScheduler
	BasePath  NodePath           // The base path for nodes defined within this context
	cancel    context.CancelFunc // Cancel func for the workflow's execution context
}

// Done returns a channel that's closed when the workflow execution context is cancelled.
// This is useful for nodes (like inside NewNode) to detect cancellation.
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

type HandlerFunc[In, Out any] func(Context, In) Outputer[Out]

type WorkflowInput[Req any] NodeDefinition[io.ReadCloser, Req]

type workflowOptions struct {
	store store.Store
}

type WorkflowOption func(*workflowOptions)

func WithStore(store store.Store) WorkflowOption {
	return func(wo *workflowOptions) {
		wo.store = store
	}
}

func DefineWorkflow[In, Out any](handler HandlerFunc[In, Out], options ...WorkflowOption) WorkflowDefinition[In, Out] {
	var opts workflowOptions
	for _, option := range options {
		option(&opts)
	}

	store := opts.store
	// Default to in memory
	if store == nil {
		store = defaultStore
	}

	return WorkflowDefinition[In, Out]{
		store:   store,
		handler: handler,
	}
}
