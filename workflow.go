// ./workflow.go
package heart

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"heart/store"

	"github.com/google/uuid"
)

// defaultStore, WorkflowOutput, WorkflowDefinition remain the same...
var defaultStore store.Store = store.NewMemoryStore()

type WorkflowOutput[Out any] struct { /* ... */
	value Out
	err   error
	done  chan struct{}
	uuid  WorkflowUUID
}

func (r *WorkflowOutput[Out]) Out() (Out, error)     { /* ... */ <-r.done; return r.value, r.err }
func (r *WorkflowOutput[Out]) Done() <-chan struct{} { /* ... */ return r.done }
func (r *WorkflowOutput[Out]) GetUUID() WorkflowUUID { /* ... */ return r.uuid }

var _ Output[any] = (*WorkflowOutput[any])(nil)

type WorkflowDefinition[In, Out any] struct { /* ... */
	handler HandlerFunc[In, Out]
	store   store.Store
}

// New starts a new workflow instance.
func (w WorkflowDefinition[In, Out]) New(parentCtx context.Context, in In) Output[Out] {
	// ... (UUID, result struct, execCtx, graph creation logic remain the same) ...
	workflowUUID := WorkflowUUID(uuid.New())
	result := &WorkflowOutput[Out]{done: make(chan struct{}), uuid: workflowUUID}
	execCtx, cancel := context.WithCancel(parentCtx)
	err := w.store.Graphs().CreateGraph(parentCtx, workflowUUID.String())
	if err != nil {
		result.err = fmt.Errorf("failed to create graph record for workflow %s: %w", workflowUUID, err)
		close(result.done)
		cancel()
		return result
	}

	// Create the root workflow context with the execution registry.
	workflowCtx := Context{ /* ... */ ctx: execCtx, nodeCount: &atomic.Int64{}, store: w.store, uuid: workflowUUID, registry: newExecutionRegistry(), BasePath: "/", cancel: cancel}

	// --- Run Workflow Handler Goroutine ---
	go func() {
		// ... (defers for done, cancel, panic handling remain the same) ...
		defer close(result.done)
		defer cancel() // Ensure context is cancelled when workflow goroutine exits
		defer func() {
			if r := recover(); r != nil {
				panicErr := fmt.Errorf("panic recovered during workflow execution %s: %v", workflowUUID, r)
				result.err = panicErr
				result.value = *new(Out) // Ensure zero value on panic
			}
		}()

		finalNodeBlueprint := w.handler(workflowCtx, in) // Returns Node[Out]

		if finalNodeBlueprint == nil { /* ... */
			result.err = fmt.Errorf("workflow handler returned a nil final node blueprint handle (workflow: %s)", workflowUUID)
			return
		}

		// Trigger resolution of the final blueprint using the registry.
		// internalResolve now correctly returns the specific Out type or an error.
		finalValue, execErr := internalResolve[Out](workflowCtx, finalNodeBlueprint)

		// Store the result or error.
		result.err = execErr
		if execErr == nil {
			// Resolution successful, finalValue is already the correct type Out.
			result.value = finalValue
		} else {
			// Ensure result.value is the zero value if there was an error
			result.value = *new(Out)
		}

		// Check for context cancellation after resolution but before signalling done.
		if execCtx.Err() != nil && result.err == nil {
			result.err = execCtx.Err() // Report context error if no primary execution error occurred
		}

	}()

	return result // Return the WorkflowOutput handle immediately.
}

// WorkflowUUID alias remains the same.
type WorkflowUUID = uuid.UUID

// Context struct remains the same.
type Context struct { /* ... */
	ctx       context.Context
	nodeCount *atomic.Int64
	store     store.Store
	uuid      WorkflowUUID
	registry  *executionRegistry
	BasePath  NodePath
	cancel    context.CancelFunc // Added cancel func
}

// internalResolve uses the registry to trigger node execution and returns the specific Out type.
// Parameter type changed to accept any node blueprint.
func internalResolve[Out any](c Context, nodeBlueprint any) (Out, error) {
	var zero Out // Zero value for return on error

	// Handle `into` nodes directly
	if _, isInto := nodeBlueprint.(*into[Out]); isInto {
		// Use interface assertion for internal_out
		type internalOutGetter interface{ internal_out() (any, error) }
		if getter, okAssert := nodeBlueprint.(internalOutGetter); okAssert {
			outAny, err := getter.internal_out()
			if err != nil {
				return zero, err // Return the error from the 'into' node
			}
			// Type assert the 'any' result from internal_out to the specific Out type.
			typedOut, ok := outAny.(Out)
			if !ok {
				var expectedType Out
				actualType := "nil"
				if outAny != nil {
					actualType = fmt.Sprintf("%T", outAny)
				}
				return zero, fmt.Errorf("internalResolve: type assertion failed for 'into' node result: expected %T, got %s", expectedType, actualType)
			}
			return typedOut, nil
		}
		// Fallthrough if assertion fails (should not happen for *into)
	}

	// Cast to a Node type we can work with to get path etc.
	// Assert to Node[Out] specifically to ensure type compatibility.
	nodeBlueprintNode, okCast := nodeBlueprint.(Node[Out])
	if !okCast {
		// This indicates a programming error - the blueprint is not compatible with the expected Out type.
		return zero, fmt.Errorf("internalResolve: provided node blueprint (%T) is not compatible with expected output type Node[%T]", nodeBlueprint, zero)
	}

	// For regular nodes, delegate to the registry
	if c.registry == nil {
		panic(fmt.Sprintf("internal error: execution registry is nil in context for workflow %s", c.uuid))
	}

	// Get or create the execution instance for the blueprint
	// Pass the correctly asserted Node[Out] blueprint handle.
	execAny := getOrCreateExecution(c.registry, nodeBlueprintNode, c)

	// Assert the returned 'any' to the 'executioner' interface
	exec, okExec := execAny.(executioner)
	if !okExec {
		// This indicates a failure in createGenericExecution or registry logic
		panic(fmt.Sprintf("internal error: registry returned non-executioner type %T for path %s", execAny, nodeBlueprintNode.internal_getPath()))
	}

	// Trigger/await execution via the executioner interface
	// getResult returns (any, error).
	resultAny, execErr := exec.getResult()
	if execErr != nil {
		return zero, execErr // Return the error from execution directly.
	}

	// Type assert the 'any' result from getResult to the specific Out type.
	resultTyped, okAssert := resultAny.(Out)
	if !okAssert {
		var expectedType Out // Use zero value to get type name
		actualType := "nil"
		if resultAny != nil {
			actualType = fmt.Sprintf("%T", resultAny)
		}
		// This indicates an internal error - the execution returned the wrong type.
		return zero, fmt.Errorf("internalResolve: type assertion failed for node execution result (path: %s): expected %T, got %s", nodeBlueprintNode.internal_getPath(), expectedType, actualType)
	}

	// Return the successfully resolved and type-asserted value.
	return resultTyped, nil
}

// Standard context methods remain the same...
func (c Context) Done() <-chan struct{}                   { /* ... */ return c.ctx.Done() }
func (c Context) Err() error                              { /* ... */ return c.ctx.Err() }
func (c Context) Value(key any) any                       { /* ... */ return c.ctx.Value(key) }
func (c Context) Deadline() (deadline time.Time, ok bool) { /* ... */ return c.ctx.Deadline() }

// HandlerFunc signature updated to return Node[Out].
type HandlerFunc[In, Out any] func(ctx Context, in In) Node[Out] // Changed: return Node[Out]

// Workflow options remain the same...
type workflowOptions struct{ store store.Store }
type WorkflowOption func(*workflowOptions)

func WithStore(store store.Store) WorkflowOption { /* ... */
	return func(wo *workflowOptions) {
		if store == nil {
			panic("WithStore provided with a nil store")
		}
		wo.store = store
	}
}

// DefineWorkflow remains the same structurally.
func DefineWorkflow[In, Out any](handler HandlerFunc[In, Out], options ...WorkflowOption) WorkflowDefinition[In, Out] { /* ... */
	opts := workflowOptions{store: defaultStore}
	for _, option := range options {
		option(&opts)
	}
	if handler == nil {
		panic("DefineWorkflow requires a non-nil handler function")
	}
	if opts.store == nil {
		panic("DefineWorkflow requires a non-nil store (internal error: defaultStore was nil or WithStore provided nil)")
	}
	return WorkflowDefinition[In, Out]{store: opts.store, handler: handler}
}
