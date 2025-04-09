// ./workflow.go
package heart

import (
	"context"
	"heart/store"
	"io"

	// Removed "sync" import as Mutex is gone
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
	// Removed lock/unlock: Access is safe now
	return r.value, r.err
}

// Done returns a channel that is closed when the workflow completes.
// Successive calls to Done return the same channel.
func (r *WorkflowResult[Out]) Done() <-chan struct{} {
	return r.done
}

type WorkflowDefinition[In, Out any] struct {
	handler HandlerFunc[In, Out]
	store   store.Store
}

type getter struct{}

func (g getter) heart() {}

// New starts a new workflow instance in a separate goroutine and returns a WorkflowResult immediately.
// The WorkflowResult can be used to block and wait for the final output using the Get method,
// or check for completion using the Done channel.
func (w WorkflowDefinition[In, Out]) New(ctx context.Context, in In) *WorkflowResult[Out] {
	workflowUUID := WorkflowUUID(uuid.New())

	result := &WorkflowResult[Out]{
		done: make(chan struct{}),
	}

	// Create the graph entry immediately (or handle error)
	// We do this synchronously to ensure the graph exists before the async execution starts.
	err := w.store.Graphs().CreateGraph(ctx, workflowUUID.String())
	if err != nil {
		// If we can't even create the graph record, fail fast.
		result.err = err
		close(result.done) // Signal immediate completion (with error)
		return result
	}

	workflowCtx := Context{
		ctx:       ctx,
		nodeCount: &atomic.Int64{},
		store:     w.store,
		uuid:      workflowUUID,
		scheduler: newWorkflowScheduler(),
		start:     make(chan struct{}), // start channel usage seems specific to internal scheduling, keeping it for now.
	}

	// Run the actual workflow execution in a goroutine.
	go func() {
		// Ensure the done channel is closed on exit, signalling completion or error.
		// All writes to result.value and result.err below happen *before* this deferred close.
		defer close(result.done)

		// Execute the handler and get the final output. This blocks within the goroutine.
		finalOutput, execErr := w.handler(workflowCtx, in).Out(getter{})

		// Store the result directly. Reads in Get() after <-done will see these writes.
		// Removed lock/unlock
		result.value = finalOutput
		result.err = execErr
	}()

	return result // Return the result handle immediately.
}

type WorkflowUUID = uuid.UUID

type Context struct {
	ctx       context.Context
	nodeCount *atomic.Int64
	store     store.Store
	uuid      WorkflowUUID
	start     chan struct{}
	scheduler *workflowScheduler
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
